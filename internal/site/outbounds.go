package site

// Payment outbound management. The payment path reads the vault record
// "payment-outbounds" (an array of independent sing-box outbound objects) and
// X prices depend on the exit country of that node, so an operator has to be
// able to edit it from the browser instead of SSH-ing in for every change.
//
// The form covers the common proxy types; anything else (anytls, hysteria2,
// vless with a full transport block, …) goes through the raw-JSON escape hatch,
// which is the same validator the CLI uses.

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"xgift/internal/checkout"
	"xgift/internal/proxy"
	"xgift/internal/vault"
)

// outboundNodeForm is one row of the outbound editor.
type outboundNodeForm struct {
	Type     string `json:"type"`               // http | socks | shadowsocks | vmess | vless | trojan | hysteria2 | anytls | direct
	Tag      string `json:"tag"`                // unique label
	Server   string `json:"server"`             // host or IP
	Port     string `json:"server_port"`        // kept as text so an empty box is detectable
	Username string `json:"username,omitempty"` // http / socks5
	Password string `json:"password,omitempty"`
	Method   string `json:"method,omitempty"`   // shadowsocks cipher
	UUID     string `json:"uuid,omitempty"`     // vmess / vless / trojan(password) / anytls(password)
	Security string `json:"security,omitempty"` // vless tls/reality
	SNI      string `json:"sni,omitempty"`
	Flow     string `json:"flow,omitempty"`    // vless flow
	Raw      string `json:"raw,omitempty"`     // per-node raw JSON escape hatch
	TLS      bool   `json:"tls,omitempty"`     // trojan / http-with-tls
	Network  string `json:"network,omitempty"` // vless ws/grpc
	WSPath   string `json:"ws_path,omitempty"`
}

type outboundsForm struct {
	Nodes   []outboundNodeForm `json:"nodes"`
	RawJSON string             `json:"raw_json"`
}

var (
	outboundTagPattern  = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	outboundHostPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,255}$`)
	outboundUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{8,64}$`)
	outboundCipherOK    = map[string]bool{
		"aes-128-gcm": true, "aes-192-gcm": true, "aes-256-gcm": true,
		"aes-128-cfb": true, "aes-192-cfb": true, "aes-256-cfb": true,
		"chacha20-ietf-poly1305": true, "xchacha20-ietf-poly1305": true,
		"2022-blake3-aes-128-gcm": true, "2022-blake3-aes-256-gcm": true,
		"2022-blake3-chacha20-poly1305": true, "none": true,
	}
)

// buildOutbound turns one form row into a sing-box outbound object.
func buildOutbound(n outboundNodeForm) (map[string]any, error) {
	if strings.TrimSpace(n.Raw) != "" {
		var obj map[string]any
		if err := json.Unmarshal([]byte(n.Raw), &obj); err != nil {
			return nil, fmt.Errorf("第 %s 项的原始 JSON 无法解析", n.Tag)
		}
		return obj, nil
	}
	typ := strings.ToLower(strings.TrimSpace(n.Type))
	if typ == "" {
		typ = "socks"
	}
	tag := strings.TrimSpace(n.Tag)
	if tag == "" {
		return nil, errors.New("每一项都需要一个唯一标签")
	}
	if !outboundTagPattern.MatchString(tag) {
		return nil, fmt.Errorf("标签 %q 只能包含字母、数字与 _ . : -", tag)
	}
	if typ == "direct" {
		return map[string]any{"type": "direct", "tag": tag}, nil
	}
	server := strings.TrimSpace(n.Server)
	if server == "" || !outboundHostPattern.MatchString(server) {
		return nil, fmt.Errorf("标签 %q 的服务器地址无效", tag)
	}
	port, err := strconv.Atoi(strings.TrimSpace(n.Port))
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("标签 %q 的端口必须是 1-65535", tag)
	}

	obj := map[string]any{"type": typ, "tag": tag, "server": server, "server_port": port}
	user := strings.TrimSpace(n.Username)
	pass := n.Password

	switch typ {
	case "http":
		if user != "" || pass != "" {
			obj["username"] = user
			obj["password"] = pass
		}
		if n.TLS {
			obj["tls"] = map[string]any{"enabled": true, "server_name": strings.TrimSpace(n.SNI)}
		}
	case "socks":
		// Default to SOCKS5; sing-box requires an explicit version.
		obj["version"] = "5"
		if user != "" || pass != "" {
			obj["username"] = user
			obj["password"] = pass
		}
	case "shadowsocks":
		method := strings.ToLower(strings.TrimSpace(n.Method))
		if !outboundCipherOK[method] {
			return nil, fmt.Errorf("标签 %q 的加密方式 %q 不被支持", tag, n.Method)
		}
		if pass == "" {
			return nil, fmt.Errorf("标签 %q 需要密码", tag)
		}
		obj["method"] = method
		obj["password"] = pass
	case "vmess":
		uuid := strings.TrimSpace(n.UUID)
		if !outboundUUIDPattern.MatchString(uuid) {
			return nil, fmt.Errorf("标签 %q 的 UUID 无效", tag)
		}
		obj["uuid"] = uuid
		obj["security"] = "auto"
		obj["alter_id"] = 0
	case "vless":
		uuid := strings.TrimSpace(n.UUID)
		if !outboundUUIDPattern.MatchString(uuid) {
			return nil, fmt.Errorf("标签 %q 的 UUID 无效", tag)
		}
		obj["uuid"] = uuid
		if flow := strings.TrimSpace(n.Flow); flow != "" {
			obj["flow"] = flow
		}
		tls := map[string]any{"enabled": true, "server_name": strings.TrimSpace(n.SNI)}
		switch strings.ToLower(strings.TrimSpace(n.Security)) {
		case "", "tls":
		case "none":
			tls = map[string]any{"enabled": false}
		case "reality":
			tls["reality"] = map[string]any{"enabled": true}
		default:
			return nil, fmt.Errorf("标签 %q 的 security 只支持 tls / reality / none", tag)
		}
		obj["tls"] = tls
		if net := strings.ToLower(strings.TrimSpace(n.Network)); net == "ws" {
			obj["transport"] = map[string]any{
				"type": "ws",
				"path": orDefault(strings.TrimSpace(n.WSPath), "/"),
			}
		} else if net == "grpc" {
			obj["transport"] = map[string]any{"type": "grpc", "service_name": strings.TrimSpace(n.WSPath)}
		}
	case "trojan":
		if pass == "" {
			return nil, fmt.Errorf("标签 %q 需要密码", tag)
		}
		obj["password"] = pass
		obj["tls"] = map[string]any{"enabled": true, "server_name": strings.TrimSpace(n.SNI)}
	case "anytls":
		if pass == "" {
			return nil, fmt.Errorf("标签 %q 需要密码", tag)
		}
		obj["password"] = pass
		obj["tls"] = map[string]any{"enabled": true, "server_name": strings.TrimSpace(n.SNI)}
	case "hysteria2":
		if pass == "" {
			return nil, fmt.Errorf("标签 %q 需要密码", tag)
		}
		obj["password"] = pass
		obj["tls"] = map[string]any{"enabled": true, "server_name": strings.TrimSpace(n.SNI)}
	default:
		return nil, fmt.Errorf("标签 %q 的类型 %q 不支持，请改用「原始 JSON」", tag, n.Type)
	}
	return obj, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// outboundsRecord validates the submitted set and returns the array the vault
// stores. Validation reuses the payment-path parser so the browser cannot save
// something the checkout would later refuse.
func (f *outboundsForm) outboundsRecord() ([]byte, error) {
	// 原始 JSON 模式：直接整段替换，交给同一个校验器。
	if strings.TrimSpace(f.RawJSON) != "" && len(f.Nodes) == 0 {
		raw := []byte(strings.TrimSpace(f.RawJSON))
		if _, err := proxy.ParseOutboundPool(raw); err != nil {
			return nil, err
		}
		return raw, nil
	}
	if len(f.Nodes) == 0 {
		// 空集合 = 直连。存一个空数组，语义与「没有配置」一致。
		return []byte(`[]`), nil
	}
	if len(f.Nodes) > 128 {
		return nil, errors.New("出站节点最多 128 个")
	}
	seen := map[string]bool{}
	nodes := make([]map[string]any, 0, len(f.Nodes))
	for _, n := range f.Nodes {
		obj, err := buildOutbound(n)
		if err != nil {
			return nil, err
		}
		tag, _ := obj["tag"].(string)
		if tag == "" {
			return nil, errors.New("每一项都需要一个唯一标签")
		}
		if seen[tag] {
			return nil, fmt.Errorf("标签 %q 重复", tag)
		}
		seen[tag] = true
		nodes = append(nodes, obj)
	}
	raw, err := json.Marshal(nodes)
	if err != nil {
		return nil, err
	}
	// 复用付款路径的校验器，保证浏览器存下的配置真能被下单流程加载。
	if _, err := proxy.ParseOutboundPool(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// outboundSummary is the redacted view: node tags and types only, never
// credentials. It also reports the payment path's own view so the operator sees
// whether the checkout would currently run direct or through the pool.
type outboundSummary struct {
	Configured bool `json:"configured"`
	Nodes      int  `json:"nodes"`
	// 初始化为空切片，保证序列化成 [] 而不是 null —— 前端会直接读 .length。
	Tags      []string `json:"tags"`
	Mode      string   `json:"mode"`      // direct | pool
	Available int      `json:"available"` // nodes not cooling / blocked
	Cooling   int      `json:"cooling"`
	ProxyMode string   `json:"proxy_mode"` // the account-check exit (proxy record)
}

// describeOutbounds reads the payment pool's own view plus the account-check
// exit, so the settings page shows what the checkout would really do.
func describeOutbounds(v *vault.Vault) (outboundSummary, error) {
	out := outboundSummary{Tags: []string{}}
	raw, err := v.Get("payment-outbounds")
	if err == nil && len(raw) > 0 {
		nodes, perr := proxy.ParseOutboundPool(raw)
		if perr != nil {
			return out, perr
		}
		// 空数组是「保存过但没节点」，语义等同直连 —— 别报成 configured，
		// 否则界面会显示"已配置代理池"，而付款其实走直连。
		if len(nodes) > 0 {
			out.Configured = true
			out.Nodes = len(nodes)
			out.Mode = "pool"
			for _, n := range nodes {
				var meta struct{ Tag, Type string }
				if json.Unmarshal(n, &meta) == nil {
					out.Tags = append(out.Tags, meta.Tag+" ("+meta.Type+")")
				}
			}
		}
	}
	if out.Mode == "" {
		out.Mode = "direct"
	}
	// The payment path tracks cooling/blocked nodes separately; surface it so a
	// node that is present but unusable does not look healthy.
	if net, nerr := checkout.PaymentNetworkStatus(v); nerr == nil {
		out.Available = net.Available
		out.Cooling = net.Cooling
		if net.Nodes > 0 {
			out.Nodes = net.Nodes
			out.Mode = net.Mode
		}
	}
	// The account-check exit is a separate record; show it too so the operator
	// is not misled into thinking this panel edits that one.
	if praw, perr := v.Get("proxy"); perr == nil && len(praw) > 0 {
		var doc struct {
			Outbounds []struct{ Type, Tag string } `json:"outbounds"`
		}
		if json.Unmarshal(praw, &doc) == nil {
			for _, o := range doc.Outbounds {
				out.ProxyMode = o.Type
				break
			}
		}
	}
	return out, nil
}
