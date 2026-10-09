package site

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"xgift/internal/checkout"
	"xgift/internal/proxy"
)

// 出口探测：测一个节点到底能不能用、出口 IP 是什么。
//
// 抽成包是因为原来只有 tools/egressprobe 这个命令行版本，需要手动
// 把 JSON 传给命令、再手动把结果抄回后台。放进后台之后，运营在页面上
// 点一下就能知道"这个节点值不值得留"，不必开 SSH。
//
// 探测要回答三件事，缺一不可：
//  1. 能不能写进 vault（过 ParseOutboundPool）—— 不通过就是废节点，
//     写进去会导致下一笔订单失败
//  2. 出口通不通、IP 是多少 —— 真正决定 X 报价的是出口 IP，不是
//     代理服务商后台标签写的国家
//  3. 能不能连到 x.com —— 通到 ipify 但连不上 X，对本项目毫无意义
type probeResult struct {
	OK        bool   `json:"ok"`
	Node      string `json:"node"`
	Type      string `json:"type"`
	Server    string `json:"server,omitempty"`
	IP        string `json:"ip,omitempty"`
	ReachX    bool   `json:"reach_x"`
	XStatus   int    `json:"x_status,omitempty"`
	ElapsedMs int64  `json:"elapsed_ms"`
	// Stage 说明失败发生在哪一步，前端据此给出可执行的建议。
	Stage   string `json:"stage,omitempty"`
	Message string `json:"message"`
}

// probeOutbound 测一个节点。绝不写 vault、绝不调X 的写接口。
// 结果用命名返回值，这样 defer 结算的耗时才会落到调用方真正收到的那份上。
// 之前用 defer + 普通返回值，赋值发生在 reply(w, 200, res) 拷贝之后，
// 界面永远显示 0ms（生产实测发现）。
func probeOutbound(ctx context.Context, node []byte) (res probeResult) {
	var meta struct {
		Type, Tag, Server string
		Port              uint16 `json:"server_port"`
	}
	_ = json.Unmarshal(node, &meta)
	res = probeResult{
		Node:   meta.Tag,
		Type:   meta.Type,
		Server: meta.Server,
	}
	// 命名返回值 + defer：赋值落在调用方真正收到的那个值上。
	start := time.Now()
	defer func() { res.ElapsedMs = time.Since(start).Milliseconds() }()

	// 第一关：过生产校验。不通过就别测了 —— 测出来的"通"也没意义，
	// 因为它根本进不了 vault。
	if _, err := proxy.ParseOutboundPool([]byte("[" + string(node) + "]")); err != nil {
		res.Stage = "config"
		res.Message = "配置不合法，写进 vault 会让下一笔订单失败：" + err.Error()
		return
	}
	if meta.Type != "direct" {
		res.Server = fmt.Sprintf("%s:%d", meta.Server, meta.Port)
	}

	pctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	client, closeFn, err := proxy.OpenOutbound(pctx, node)
	if err != nil {
		res.Stage = "start"
		res.Message = "无法启动该出站：" + err.Error()
		return
	}
	defer closeFn()
	return probeOutboundHTTP(pctx, client, res)
}

// probeOutboundHTTP checks both destinations using the same outbound client.
// Keeping HTTP checks separate also lets regression tests run without external traffic.
func probeOutboundHTTP(pctx context.Context, client *http.Client, initial probeResult) (res probeResult) {
	res = initial
	req, err := http.NewRequestWithContext(pctx, "GET", "https://api.ipify.org?format=json", nil)
	if err != nil {
		res.Stage = "build"
		res.Message = "构建请求失败：" + err.Error()
		return
	}
	httpRes, err := client.Do(req)
	if err != nil {
		res.Stage = "exit"
		res.Message = "出口不可用（节点认证失败或端口不通）：" + err.Error()
		return
	}
	defer httpRes.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(httpRes.Body, 4096))
	if httpRes.StatusCode != 200 {
		res.Stage = "exit"
		res.Message = fmt.Sprintf("出口返回 HTTP %d", httpRes.StatusCode)
		return
	}
	var ip struct{ IP string }
	if json.Unmarshal(raw, &ip) != nil || ip.IP == "" {
		res.Stage = "exit"
		res.Message = "无法解析出口 IP"
		return
	}
	res.IP = ip.IP

	// 能连到 ipify 不代表能连到 X，所以直接打目标站。
	req2, err := http.NewRequestWithContext(pctx, "GET", "https://x.com/robots.txt", nil)
	if err != nil {
		res.Stage = "build"
		res.Message = "构建 X 请求失败：" + err.Error()
		return
	}
	res2, err := client.Do(req2)
	if err != nil {
		res.Stage = "x"
		res.Message = "出口能连通，但无法连接 x.com：" + err.Error()
		return
	}
	res.XStatus = res2.StatusCode
	res.ReachX = res2.StatusCode < 500
	res2.Body.Close()
	if !res.ReachX {
		res.Stage = "x"
		res.Message = fmt.Sprintf("出口能连通，但 x.com 返回 HTTP %d", res2.StatusCode)
		return
	}

	res.OK = true
	res.Message = "出口可用，出口 IP " + res.IP
	return
}

// outboundsDetail 返回给界面的节点清单。
//
// 为什么不能只给 tags 字符串：手动选择节点需要稳定的标识，
// 而 tag 可以重名、也可以被改。这里给每节点算一个内容指纹
// （id），前端用它来选择和删除 —— 不依赖 tag。
type outboundNode struct {
	ID      string `json:"id"`
	Tag     string `json:"tag"`
	Type    string `json:"type"`
	Server  string `json:"server,omitempty"`
	Port    uint16 `json:"server_port,omitempty"`
	Country string `json:"country,omitempty"`
	// Pinned 表示这是被手动指定的节点；轮转不会选它。
	Pinned bool `json:"pinned"`
}

// outboundsDetailList 读取当前池子并附上指纹。
func outboundsDetailList(raw []byte) ([]outboundNode, error) {
	nodes, err := proxy.ParseOutboundPool(raw)
	if err != nil {
		return nil, err
	}
	out := make([]outboundNode, 0, len(nodes))
	for _, n := range nodes {
		var meta struct {
			Tag, Type, Server, Country, Remark string
			Port                               uint16 `json:"server_port"`
		}
		if json.Unmarshal(n, &meta) != nil {
			continue
		}
		item := outboundNode{
			ID:      outboundNodeID(n),
			Tag:     meta.Tag,
			Type:    meta.Type,
			Server:  meta.Server,
			Port:    meta.Port,
			Country: meta.Country,
		}
		// sing-box 的 remark 常常写了服务商标注的国家，但那不可信 ——
		// 真正决定 X 报价的是实测出口 IP，所以只作为提示展示。
		if item.Country == "" {
			item.Country = firstLine(meta.Remark)
		}
		out = append(out, item)
	}
	return out, nil
}

// outboundNodeID 用节点内容的哈希当标识。tag 可改可重复，内容不会。
//
// 关键：必须与 checkout.outboundID 用完全相同的算法（解码→重新编码→
// sha256→全量hex），否则界面选中的"节点A"传给后端会找不到 ——
// 前端用短哈希而后端用长哈希是最容易犯且最难发现的错，所以这里显式
// 调用 checkout 导出的同一个函数，不自己实现一遍。
func outboundNodeID(node json.RawMessage) string {
	return checkout.OutboundID(node)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
