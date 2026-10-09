package site

// 后台"立即赠送"：填接收账号和套餐时长，跑完整链路直到付款。
//
// 与 /api/admin/manual-link 的区别：那条链路只建单并返回 Stripe 链接，
// 付款由人工在浏览器里点。这条链路自己把付款提交完，因此
//   - 必须持有订单锁，避免和补单任务、公开兑换同时下单；
//   - 必须以任务形式运行，因为一次完整下单要十几秒，超过同步请求的耐心；
//   - 必须把失败归因到具体阶段。原先 manual_link.go 把所有失败压成一句
//     "该账号目前无法接收 Premium 赠送"，但 2026-10-08 实测同一句话下面
//     可能是"发送方被 X 限流"（code 37）、"X 接口改了"（错误信封）、
//     "出口不可用"、"套餐没配"等完全不同的情况。运营必须知道改什么。
//
// 所以这里保留 X 的原始 code 与 message，只在展示层翻译成可读中文。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"xgift/internal/checkout"
)

// giftStage 是一个赠送任务的阶段快照。
type giftStage struct {
	Percent  int    `json:"percent"`
	Message  string `json:"message"`
	At       int64  `json:"at"`
	Done     bool   `json:"done"`
	Failed   bool   `json:"failed"`
	Error    string `json:"error,omitempty"`
	Category string `json:"category,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

// giftTask 是一次赠送任务的完整状态。存在内存里而不是 vault：
// 任务是一次性的运行状态，重启后重新发起即可，落库只会留下垃圾。
type giftTask struct {
	// ID 必须出现在响应里：前端要靠它继续轮询。第一版只POST 时返回，
	// 列表接口不给，于是切换页面再回来就找不到该轮询哪个任务了
	// （前端只能拿 username+created 拼，而那不是唯一键）。
	ID        string      `json:"id"`
	Username  string      `json:"username"`
	Recipient string      `json:"recipient,omitempty"`
	Months    int         `json:"months"`
	Amount    int         `json:"amount,omitempty"`
	Currency  string      `json:"currency,omitempty"`
	State     string      `json:"state"` // queued | running | succeeded | failed
	Stage     giftStage   `json:"stage"`
	Stages    []giftStage `json:"stages"`
	Created   int64       `json:"created"`
	Updated   int64       `json:"updated"`
	Session   string      `json:"session_id,omitempty"`
	// Card4 只保存末四位。完整卡号永远不进这个结构，也不进日志。
	Card4 string `json:"card_last4,omitempty"`
	// Message 是成功时的结论；Diagnosis 才是失败时的归因。
	Message   string         `json:"message,omitempty"`
	Diagnosis *giftDiagnosis `json:"diagnosis,omitempty"`
}

var (
	giftMu    sync.Mutex
	giftTasks = map[string]*giftTask{}
)

// giftIDPattern 匹配 giftNewID 的两种输出：随机 16 位 hex，
// 以及随机源不可用时的时间戳回退。
var giftIDPattern = regexp.MustCompile(`^gift-[a-f0-9]{16}$|^gift-\d+$`)

// 错误文本要回显到后台页面，而它来自网络库与sing-box —— 这两者都可能
// 在错误里带上带认证信息的 URL（socks5://user:pass@host、含 sk_/pk_ 的
// Stripe 地址）。实测当前版本的代理错误只含本地端口号，但那是碰巧，
// 不是保证。所以回显前统一过一遍脱敏。
//
// 只改展示文本，不改判断逻辑：脱敏后的字符串仍保留足够定位的信息
// （错误类型、主机名、状态码）。
var sensitivePatterns = []*regexp.Regexp{
	// scheme://user:password@host  →  scheme://***@host
	regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]*:[^/@\s]*@`),
	// Stripe 密钥形态
	regexp.MustCompile(`\b(sk|pk|rk)_(live|test)_[A-Za-z0-9]+`),
	// Bearer / Basic 令牌
	regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{12,}`),
	// vault 记录里的长 hex / base64 串（可能是凭据指纹）
	regexp.MustCompile(`\b[0-9a-f]{40,}\b`),
	regexp.MustCompile(`\b[A-Za-z0-9_-]{60,}\b`),
}

// redactSecrets 把错误文本里的疑似凭据替换掉，供后台展示。
func redactSecrets(s string) string {
	s = sensitivePatterns[0].ReplaceAllString(s, "${1}***@")
	for _, re := range sensitivePatterns[1:] {
		s = re.ReplaceAllString(s, "[redacted]")
	}
	return s
}

// giftTaskTTL 是任务在内存里的保留时长。超过就清理，页面刷新也不会
// 一直挂着半个月前的记录。
const giftTaskTTL = 48 * time.Hour

func giftPruneLocked(now time.Time) {
	for id, t := range giftTasks {
		if now.Sub(time.Unix(t.Updated, 0)) > giftTaskTTL {
			delete(giftTasks, id)
		}
	}
}

func giftView(t *giftTask) *giftTask {
	if t == nil {
		return nil
	}
	out := *t
	out.Stages = append([]giftStage{}, t.Stages...)
	return &out
}

// giftStageTracker 把 checkout 通过 context 推上来的进度收集成阶段列表，
// 这样页面能显示"卡在哪一步"，而不是只有一个转圈。
type giftStageTracker struct {
	mu     sync.Mutex
	task   *giftTask
	ticket string
}

func (g *giftStageTracker) advance(percent int, message string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.task.Stage.Message == message {
		return
	}
	st := giftStage{Percent: percent, Message: message, At: time.Now().Unix()}
	g.task.Stages = append(g.task.Stages, st)
	g.task.Stage = st
	g.task.Updated = time.Now().Unix()
	g.ticket = message
}

func (s *server) giftPlans(w http.ResponseWriter, r *http.Request) {
	cat, err := checkout.ReadCatalog(s.vault)
	if err != nil {
		message(w, 503, "套餐配置暂不可用。")
		return
	}
	type plan struct {
		Months   int    `json:"months"`
		Amount   int    `json:"amount"`
		Currency string `json:"currency"`
	}
	plans := make([]plan, 0, len(cat.Plans))
	for _, p := range cat.Plans {
		plans = append(plans, plan{p.Months, p.Amount, strings.ToUpper(cat.Currency)})
	}
	reply(w, 200, map[string]any{"plans": plans})
}

// giftNow 接受任务、立即返回，付款在后台跑完。
func (s *server) giftNow(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Username string `json:"username"`
		Months   int    `json:"months"`
	}
	if !decode(w, r, &q) {
		return
	}
	q.Username = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q.Username), "@"))
	if !usernamePattern.MatchString(q.Username) {
		message(w, 400, "请填写正确的 X 用户名（不含 @，最多 15 个字母数字下划线）。")
		return
	}
	if q.Months < 1 || q.Months > 24 {
		message(w, 400, "请选择套餐时长。")
		return
	}
	cat, err := checkout.ReadCatalog(s.vault)
	if err != nil {
		message(w, 503, "套餐配置暂不可用。")
		return
	}
	if _, err = cat.PlanFor(q.Months); err != nil {
		message(w, 400, "该套餐时长未配置，请先在「商品与价格」里配置。")
		return
	}
	// An incomplete card pool is the boot-time verdict this server carries, so a
	// queued gift cannot enter a payment path that is known to be unusable yet.
	// Manual gifting is admin-only and needs no card, but this endpoint spends
	// real money, so it stays closed until the configuration is finished.
	if !s.payments {
		message(w, 503, "充值未开放，站点未启用付款。")
		return
	}
	if s.paymentBlocked.Load() {
		message(w, 503, "充值未开放：付款配置未完成或尚未重启，请在后台「设置」补齐支付卡与节点后重启服务。")
		return
	}
	select {
	case s.work <- struct{}{}:
	default:
		reply(w, 409, map[string]any{"message": "有订单正在处理，请等当前订单结束再发起。"})
		return
	}
	id := giftNewID()
	task := &giftTask{
		ID:       id,
		Username: q.Username,
		Months:   q.Months,
		State:    "queued",
		Created:  time.Now().Unix(),
		Updated:  time.Now().Unix(),
		Stage:    giftStage{Percent: 0, Message: "已排队，正在等待订单锁…", At: time.Now().Unix()},
	}
	giftMu.Lock()
	giftPruneLocked(time.Now())
	giftTasks[id] = task
	giftMu.Unlock()

	// 订单锁在后台任务里拿：后台任务不随 HTTP 请求结束而取消，
	// 拿不到锁就写明原因，绝不并发下单。
	//
	// 用 s.ctx 而不是 r.Context()：请求返回后 r.Context() 会被 cancel，
	// 而这里是请求返回**之后**才执行的。另外在 goroutine 内读 r.Context()
	// 属于请求对象上的数据竞争。用服务级 context 也让服务能通过
	// ctx.Done() 感知退出，与 reconcile.go / recovery.go 的做法一致。
	//
	// 注册进 s.jobs：site.go 关闭时 jobs.Wait() 等所有后台任务结束，
	// 不注册的话正在提交的付款会在关闭时被截断。
	// jobs.Add 必须在 go 之前调用。若放在goroutine 内部，Add 可能发生在
	// 主流程已经进入 jobs.Wait() 之后，那样这个任务不会被等待，
	// 关闭时正在提交的付款会被截断。go vet 的 WaitGroup.Add 检查
	// 正是报这一点。
	s.jobs.Add(1)
	go func() {
		defer func() { s.jobs.Done(); <-s.work }()
		s.runGift(s.ctx, id, task, q.Username, q.Months)
	}()
	reply(w, 202, map[string]any{"task_id": id, "username": task.Username, "months": task.Months, "state": task.State})
}

func giftNewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("gift-%d", time.Now().UnixNano())
	}
	return "gift-" + hex.EncodeToString(b[:])
}

func (s *server) giftTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !giftIDPattern.MatchString(id) {
		message(w, 400, "任务编号无效。")
		return
	}
	giftMu.Lock()
	t, ok := giftTasks[id]
	giftMu.Unlock()
	if !ok {
		message(w, 404, "任务不存在或已过期（内存中保留 48 小时）。")
		return
	}
	reply(w, 200, giftView(t))
}

func (s *server) giftTasksList(w http.ResponseWriter, r *http.Request) {
	giftMu.Lock()
	giftPruneLocked(time.Now())
	list := make([]*giftTask, 0, len(giftTasks))
	for _, t := range giftTasks {
		list = append(list, giftView(t))
	}
	giftMu.Unlock()
	// 新的在前
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	if len(list) > 50 {
		list = list[:50]
	}
	reply(w, 200, map[string]any{"tasks": list})
}

// runGift 持锁跑完一次赠送，并把每一步都写进 task。
func (s *server) runGift(ctx context.Context, id string, task *giftTask, username string, months int) {
	giftUpdate(task, func(t *giftTask) {
		t.State = "running"
		t.Stage = giftStage{Percent: 5, Message: "正在获取订单锁…", At: time.Now().Unix()}
	})
	lock, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		giftFail(task, giftDiagnose(err, 5, "获取订单锁"), nil)
		return
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		giftFail(task, &giftDiagnosis{
			Category: "order_busy",
			Summary:  "已有订单占用锁",
			Detail:   "另一个订单正在处理中。系统不会并发下单，以免重复扣款。",
			Hint:     "等当前订单结束后重新发起。",
			Stage:    "获取订单锁",
		}, nil)
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	// 一次完整下单（询价 + 建单 + tokenize + 提交 + 核实）实测 15 秒左右，
	// 网络慢时更久。给 4 分钟，与 site 侧其它任务的量级一致。
	ctx, cancel := context.WithTimeout(ctx, 240*time.Second)
	defer cancel()

	tracker := &giftStageTracker{task: task}
	ctx = checkout.WithProgress(ctx, tracker.advance)

	rec, err := checkout.Run(ctx, s.vault, username, true, s.port, months)
	if err != nil {
		stage := ""
		if len(task.Stages) > 0 {
			stage = task.Stages[len(task.Stages)-1].Message
		}
		// Run 在 identity() 处就因为 premium_gifting_eligible=false 退出了，
		// 而那只是一个布尔值，把"接收方不可接收""发送账号被限""接口变更"
		// 压成了同一个false。2026-10-08 实测四个不同接收方都返回同一个
		// code 37，真实原因是发送账号被限，但界面显示的是接收方不可接收 ——
		// 运营会去换接收账号，而那完全没用。
		//
		// 所以这里不直接采信本地结论，先向 X 的下单接口核实一次。
		if errors.Is(err, checkout.ErrNotEligible) {
			tracker.advance(30, "资格预检未通过，正在向 X 核实真实原因…")
			verdict, perr := checkout.ProbeGiftEligibility(ctx, s.vault, username, s.port, months)
			switch {
			case perr != nil:
				// 探测本身失败不掩盖原始错误，两个原因都报出来。
				d := giftDiagnose(err, task.Stage.Percent, stage)
				d.Hint += fmt.Sprintf("（额外尝试向 X 核实判据时也失败：%s）", redactSecrets(perr.Error()))
				giftFail(task, d, rec)
			case !verdict.SenderAuthorised:
				// X 拒的是发送账号（原文 current user），换接收方没有用。
				giftFail(task, &giftDiagnosis{
					Category: "sender_not_authorised",
					Summary:  "发送账号被 X 限制，无权赠送（不是接收方的问题）",
					Detail: fmt.Sprintf("本地预检显示 premium_gifting_eligible=false；"+
						"向 X 下单接口核实后得到：%s", verdict.Reason),
					Hint:  verdict.Hint,
					XCode: verdict.XCode, XMessage: verdict.XMessage,
					Stage: stage,
				}, rec)
			case !verdict.RecipientEligible:
				// X 的原文明确指向接收方（原文 recipient user）。这次是核实过的
				// 结论，不是从布尔字段推测出来的。
				giftFail(task, &giftDiagnosis{
					Category: "recipient_ineligible",
					Summary:  "接收账号被 X 拒绝，无法接收赠送（已向 X 核实）",
					Detail: fmt.Sprintf("本地预检显示 premium_gifting_eligible=false；"+
						"向 X 下单接口核实后，X 的原文指向接收账号：%s", verdict.XMessage),
					Hint:  verdict.Hint,
					XCode: verdict.XCode, XMessage: verdict.XMessage,
					Stage: stage,
				}, rec)
			default:
				// X 其实接受了为该接收方建单：本地布尔值不可信，按X 的答复走。
				giftUpdate(task, func(x *giftTask) {
					x.Recipient = verdict.Recipient
					st := giftStage{Percent: 35, Message: "资格预检与 X 实际判据不一致，已以 X 的答复为准继续下单…", At: time.Now().Unix()}
					x.Stages = append(x.Stages, st)
					x.Stage = st
				})
				rec, err = checkout.Run(ctx, s.vault, username, true, s.port, months)
				if err == nil {
					giftSucceed(task, rec, s)
					return
				}
				stage = task.Stage.Message
				giftFail(task, giftDiagnose(err, task.Stage.Percent, stage), rec)
			}
			return
		}
		giftFail(task, giftDiagnose(err, task.Stage.Percent, stage), rec)
		return
	}
	giftSucceed(task, rec, s)
}

// giftSucceed 记录一次成功付款。
func giftSucceed(task *giftTask, rec *checkout.Record, s *server) {
	giftUpdate(task, func(t *giftTask) {
		t.State = "succeeded"
		t.Recipient = rec.RecipientID
		t.Amount = rec.Amount
		t.Currency = rec.Currency
		t.Session = rec.SessionID
		if cards, e := checkout.CardsStatus(s.vault); e == nil && len(cards) > 0 {
			// 只显示末四位；完整卡号永远不进这个结构，也不进日志。
			t.Card4 = cards[0].Last4
		}
		st := giftStage{Percent: 100, Message: "赠送完成，付款已提交。", At: time.Now().Unix(), Done: true}
		t.Stages = append(t.Stages, st)
		t.Stage = st
		t.Message = fmt.Sprintf("已付款：%s %d 个月，接收方 @%s（%s），尾号 %s。",
			rec.Currency, rec.Months, rec.Username, rec.RecipientID, t.Card4)
	})
}

func giftUpdate(t *giftTask, f func(*giftTask)) {
	giftMu.Lock()
	defer giftMu.Unlock()
	f(t)
	t.Updated = time.Now().Unix()
}

func giftFail(t *giftTask, d *giftDiagnosis, rec *checkout.Record) {
	giftUpdate(t, func(x *giftTask) {
		x.State = "failed"
		x.Diagnosis = d
		st := giftStage{
			Percent:  x.Stage.Percent,
			Message:  d.Summary,
			At:       time.Now().Unix(),
			Failed:   true,
			Error:    d.Detail,
			Category: d.Category,
			Hint:     d.Hint,
		}
		x.Stages = append(x.Stages, st)
		x.Stage = st
		x.Message = d.Summary
		// 有付款证据就必须原样告诉运营：可能已经扣款，不能只看"失败"。
		if rec != nil && (rec.SubmittedAt != 0 || rec.PaymentMethod != "" || rec.SessionID != "") {
			x.Stage.Hint = "注意：这单已产生付款证据（session " + rec.SessionID + "），请先核实 Stripe 后台是否已扣款，再决定是否重试。"
		}
	})
}

// giftDiagnosis 把一个 error 归因到具体阶段与可执行动作。
type giftDiagnosis struct {
	Category string `json:"category"`
	Summary  string `json:"summary"`
	Detail   string `json:"detail"`
	Hint     string `json:"hint"`
	XCode    int    `json:"x_code,omitempty"`
	XMessage string `json:"x_message,omitempty"`
	Stage    string `json:"stage,omitempty"`
}

// xCodeOf 从错误文本里取出 X 的 GraphQL code。底层用
// fmt.Errorf("...[code %d]") 包装，这个解析只依赖那个稳定格式。
var xCodePattern = regexp.MustCompile(`\[code (\d+)\]`)

func giftDiagnose(err error, percent int, stage string) *giftDiagnosis {
	d := &giftDiagnosis{
		Category: "unknown",
		Summary:  "赠送未完成",
		Detail:   redactSecrets(err.Error()),
		Hint:     "请查看服务日志中的对应记录，或用 giftprobe create 复现一次拿 X 的原始判据。",
		Stage:    stage,
	}
	if m := xCodePattern.FindStringSubmatch(err.Error()); m != nil {
		fmt.Sscanf(m[1], "%d", &d.XCode)
	}
	switch {
	// 发送方账号被 X 限制：2026-10-08 实测 code 37 就是这个，
	// 与"接收方不可接收"是完全不同的问题，运营要改的是发送账号。
	case errors.Is(err, checkout.ErrGiftNotAuthorised):
		d.Category = "sender_not_authorised"
		d.Summary = "发送账号被 X 拒绝，无权赠送"
		d.XMessage = "Current user is not eligible to gift"
		d.Detail = redactSecrets(err.Error())
		d.Hint = "这不是接收方的问题，是发送账号自身被 X 限制。常见原因：赠送额度/频率超限、账号过新或未完成手机验证、账号本身受限。改接收账号没有用，只能等额度恢复或换一个已验证的发送账号。"
	// X 接口标识变了：需要改代码里的 operation ID，不是账号问题。
	case errors.Is(err, checkout.ErrOperationRejected):
		d.Category = "operation_stale"
		d.Summary = "X 拒绝了请求本身（不是账号问题）"
		d.Detail = redactSecrets(err.Error())
		d.Hint = "X 返回了 GraphQL 错误信封但内容不是权限拒绝，通常是 GraphQL operation ID 已变更或接口改名。需要更新 internal/checkout/xapi.go 里的 DefaultOp* 常量并重新部署。"
	case errors.Is(err, checkout.ErrNotEligible):
		d.Category = "recipient_ineligible"
		d.Summary = "接收账号目前无法接收赠送"
		d.Detail = redactSecrets(err.Error())
		d.Hint = "X 返回 premium_gifting_eligible=false。常见原因：该账号已是订阅用户（不能再收）、被地区限制、或账号受限。注意这个字段把多种原因压成一个布尔值，要看确切原因请用 giftprobe create 探测。"
	case errors.Is(err, checkout.ErrUserNotFound):
		d.Category = "recipient_not_found"
		d.Summary = "未找到该 X 用户名"
		d.Detail = redactSecrets(err.Error())
		d.Hint = "检查用户名拼写。X 的用户名最多 15 个字母数字下划线，不含 @。"
	case errors.Is(err, checkout.ErrXReadFailure):
		d.Category = "x_read_failure"
		d.Summary = "向 X 查询账号或价格失败"
		d.Detail = redactSecrets(err.Error())
		d.Hint = "读取类接口用的是「查询出口」。先跑 curl -x <查询出口> https://api.ipify.org 确认出口通，再检查凭据是否过期。"
	// 出口：mutation 必须走付款出口，直连会被 X 判 Permissions。
	// 这是 2026-10-08 整个事故的根因，所以单独一类。
	case strings.Contains(err.Error(), "regional") || strings.Contains(err.Error(), "regional checkout proxy"):
		d.Category = "egress_unavailable"
		d.Summary = "付款出站不可用"
		d.Detail = redactSecrets(err.Error())
		d.Hint = "X 的下单与询价强制走 payment-outbounds，直连会被判权限错误。检查后台「付款出站」是否已配置、节点是否可用、出口国家是否与预期一致。"
	case strings.Contains(err.Error(), "price is not exactly") || strings.Contains(err.Error(), "amount"):
		d.Category = "price_mismatch"
		d.Summary = "X 返回的价格与配置不一致"
		d.Detail = redactSecrets(err.Error())
		d.Hint = "X 按请求出口所在国家定价。确认付款出站所在的国家，以及「商品与价格」里的金额与该国家 X 实际收取的金额一致。"
	case errors.Is(err, checkout.ErrPaymentPaused):
		d.Category = "payment_paused"
		d.Summary = "付款已被暂停"
		d.Detail = redactSecrets(err.Error())
		d.Hint = "后台的付款开关被关闭，或付款节点全部被标记为失败。检查后台「运维」页的付款节点状态。"
	// 付款结果类。这四个原来全部落进 default，页面只显示"未归类"，
	// 而它们恰恰是运营最需要立刻知道的一组：卡被拒、需要 3DS、
	// 没有可用卡、节点全冷却。收款失败与"资格不符"必须区别对待 ——
	// 前者要换卡或等冷却，后者要改账号。
	case errors.Is(err, checkout.ErrPaymentDeclined):
		d.Category = "payment_declined"
		d.Summary = "付款被拒（发卡行或 Stripe 拒绝了这笔扣款）"
		d.Detail = redactSecrets(err.Error())
		d.Hint = "订单本身有效，是卡被拒了。后台「支付卡」里移除或替换这张卡；" +
			"重试前先用客户查询确认原订单状态，避免重复扣款。"
	case errors.Is(err, checkout.ErrPaymentActionRequired):
		d.Category = "payment_action_required"
		d.Summary = "付款需要额外验证（3DS / 银行验证），无法自动完成"
		d.Detail = redactSecrets(err.Error())
		d.Hint = "这类订单不能无人值守付款。需要人工在 Stripe 完成验证，" +
			"或改用不需要 3DS 的卡。注意订单可能已占用一次额度。"
	case errors.Is(err, checkout.ErrNoUsableCard):
		d.Category = "no_usable_card"
		d.Summary = "卡池里没有可用的卡"
		d.Detail = redactSecrets(err.Error())
		d.Hint = "所有卡都被标记为不可用或仍在冷却。后台「支付卡」查看每张卡的状态" +
			"与剩余冷却时间；被拒的卡必须先移出池子（会自动跳过）。"
	case errors.Is(err, checkout.ErrPaymentNodesCooling):
		d.Category = "payment_nodes_cooling"
		d.Summary = "所有付款出站都在冷却中"
		d.Detail = redactSecrets(err.Error())
		d.Hint = "出口连续失败会进入冷却以保护节点。等冷却结束，" +
			"或在后台「付款出站」补一个可用节点。"
	default:
		if d.XCode != 0 {
			d.Category = "x_error"
			d.Summary = fmt.Sprintf("X 返回错误 code %d", d.XCode)
		}
	}
	return d
}
