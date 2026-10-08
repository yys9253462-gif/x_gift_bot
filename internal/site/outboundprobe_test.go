package site

import (
	"context"
	"testing"
	"time"
)

// 🔴 探测结果的耗时必须真的被结算。
//
// 生产实测发现界面一直显示 0ms：probeOutbound 用普通返回值 + defer 结算，
// 而调用方 reply(w, 200, res) 传的是**值拷贝**，defer 的赋值发生在拷贝
// 之后，于是永远丢在本地变量上。连接被拒不应该是 0ms —— 那会让
// "瞬间失败"和"超时失败"在界面上看起来一样。
func TestProbeElapsedIsMeasured(t *testing.T) {
	// 一个必然失败的节点（端口不通），走完整条失败路径。
	node := []byte(`{"type":"socks","tag":"t","server":"127.0.0.1","server_port":1}`)
	start := time.Now()
	res := probeOutbound(context.Background(), node)
	wall := time.Since(start)

	if res.ElapsedMs < 1 {
		t.Fatalf("耗时没有被结算：res.ElapsedMs=%d，实际墙钟 %dms", res.ElapsedMs, wall.Milliseconds())
	}
	// 不要求精确，但要落在实际耗时的合理范围内 —— 说明量的是同一段时间。
	if res.ElapsedMs > wall.Milliseconds()+200 {
		t.Fatalf("耗时 %dms 明显大于实际 %dms，量的不是同一次调用", res.ElapsedMs, wall.Milliseconds())
	}
}

// 失败的节点必须给出可执行的信息，不能只说"失败"。
// 运营看到"出口不可用"不知道该换节点还是该找服务商。
func TestProbeFailureExplainsWhere(t *testing.T) {
	node := []byte(`{"type":"socks","tag":"t","server":"127.0.0.1","server_port":1}`)
	res := probeOutbound(context.Background(), node)
	if res.OK {
		t.Fatal("不存在的端口不应报告可用")
	}
	if res.Stage == "" {
		t.Error("失败必须标明阶段（config/start/exit/x），否则界面无法给出针对性建议")
	}
	if res.Message == "" {
		t.Error("失败必须带说明")
	}
}

// 配置不合法的节点在探测前就该被拦下 —— 不值得为它花 40 秒测连通性。
func TestProbeRejectsInvalidConfigFirst(t *testing.T) {
	// type 不在支持列表里。
	node := []byte(`{"type":"not-a-real-protocol","tag":"t","server":"1.2.3.4","server_port":1080}`)
	start := time.Now()
	res := probeOutbound(context.Background(), node)
	wall := time.Since(start)

	if res.OK {
		t.Fatal("非法协议不应报告可用")
	}
	if res.Stage != "config" {
		t.Errorf("应停在config 阶段，得到 %q", res.Stage)
	}
	// 配置错误应该瞬间返回，不该跑满连通性探测。
	if wall > 3*time.Second {
		t.Errorf("配置不合法却花了 %dms 才返回，应立即拦下", wall.Milliseconds())
	}
}

// 直连节点是合法配置（表示不走代理），不能被当成非法。
func TestProbeAcceptsDirect(t *testing.T) {
	res := probeOutbound(context.Background(), []byte(`{"type":"direct","tag":"d"}`))
	if res.Stage == "config" {
		t.Errorf("direct 是合法配置，不该被拦在 config 阶段：%s", res.Message)
	}
}
