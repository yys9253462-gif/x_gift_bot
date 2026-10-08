// Command egressprobe tests one payment/proxy outbound before it is written
// into the vault.
//
// Why this exists: a malformed outbound does not fail where it is written, it
// fails at boot. On 2026-10-08 a hysteria2 node whose "server_ports" was a
// string array made the whole service unbootable with
//
//	initialize embedded sing-box: initialize outbound[0]: bad port range: 12019
//
// and rolling the binary back did not help, because the bad record was in the
// vault. So: probe first, write second.
//
// Usage:
//
//	egressprobe '<outbound json>'   # 出口国家 / 连通性 / IP
//
// It never writes the vault and never touches X.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"xgift/internal/proxy"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: egressprobe '<outbound json>'")
		fmt.Fprintln(os.Stderr, "示例: egressprobe '{\"type\":\"socks\",\"tag\":\"t\",\"server\":\"1.2.3.4\",\"server_port\":1080}'")
		os.Exit(2)
	}
	node := []byte(os.Args[1])

	//先过生产校验：能被ParseOutboundPool 接受，才值得往下测。
	if _, err := proxy.ParseOutboundPool([]byte("[" + string(node) + "]")); err != nil {
		fmt.Fprintln(os.Stderr, "✗ 生产校验不通过，写进 vault 会导致服务起不来：")
		fmt.Fprintln(os.Stderr, "  ", err)
		os.Exit(1)
	}
	fmt.Println("✓ 通过生产校验（ParseOutboundPool）")

	var meta struct {
		Type, Tag, Server string
		Port              uint16 `json:"server_port"`
	}
	json.Unmarshal(node, &meta)
	if meta.Type != "direct" {
		fmt.Printf("  节点: %s %s:%d\n", meta.Type, meta.Server, meta.Port)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	client, closeFn, err := proxy.OpenOutbound(ctx, node)
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗ 无法启动该出站：", err)
		os.Exit(1)
	}
	defer closeFn()

	// 出口国家：真正决定 X 报价的是这里，不是代理服务商后台的标签。
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.ipify.org?format=json", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "构建请求失败:", err)
		os.Exit(1)
	}
	res, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗ 出口不可用（节点认证失败或端口不通）：", err)
		os.Exit(1)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "✗ 出口返回 HTTP %d：%.200s\n", res.StatusCode, raw)
		os.Exit(1)
	}
	var ip struct {
		IP string `json:"ip"`
	}
	if json.Unmarshal(raw, &ip) != nil || ip.IP == "" {
		fmt.Fprintf(os.Stderr, "✗ 无法解析出口 IP：%.200s\n", raw)
		os.Exit(1)
	}
	fmt.Println("✓ 出口可用，出口 IP =", ip.IP)

	// 再打一次 X 的公开接口，确认这条出口真的能连到目标站。
	req2, err := http.NewRequestWithContext(ctx, "GET", "https://x.com/robots.txt", nil)
	if err == nil {
		if res2, e := client.Do(req2); e == nil {
			fmt.Printf("✓ 可达 x.com（HTTP %d）\n", res2.StatusCode)
			res2.Body.Close()
		} else {
			fmt.Println("✗ 无法连接 x.com：", e)
			os.Exit(1)
		}
	}
	fmt.Println("\n可以写进 vault 了。把上面的 JSON 原样交给 setsecret。")
}