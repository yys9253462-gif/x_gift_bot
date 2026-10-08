package site

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"xgift/internal/checkout"
	"xgift/internal/proxy"
	"xgift/internal/vault"
)

// 付款出站的节点级操作：列出、探测、删除、��定。
//
// 为什么要有节点级端点而不是让前端拼整池提交：
//   - 删除要确认"删的是哪一个"，而 tag 可以重复，前端用 tag 匹配就可能
//     删错节点；用内容指纹才对得上
//   - 并发：整池覆盖是"读—改—写"，两个管理员同时操作会互相覆盖；
//     节点级操作只碰自己那一条
//   - 删除是危险操作，单独端点才能做二次确认与安全校验

// outboundsNodes 返回节点清单，供界面渲染表格。
func (s *server) outboundsNodes(w http.ResponseWriter, r *http.Request) {
	view, err := describeOutbounds(s.vault)
	if err != nil {
		message(w, 500, "读取出站配置失败："+err.Error())
		return
	}
	nodes := []outboundNode{}
	pinned := ""
	if p, perr := checkout.PinnedPaymentNode(s.vault); perr == nil {
		pinned = p
	}
	if raw, gerr := s.vault.Get("payment-outbounds"); gerr == nil && len(raw) > 0 {
		if list, lerr := outboundsDetailList(raw); lerr == nil {
			nodes = list
			for i := range nodes {
				nodes[i].Pinned = nodes[i].ID == pinned
			}
		}
	}
	reply(w, 200, map[string]any{
		"nodes":  nodes,
		"pinned": pinned,
		"summary": map[string]any{
			"configured": view.Configured,
			"nodes":      view.Nodes,
			"mode":       view.Mode,
			"available":  view.Available,
			"cooling":    view.Cooling,
		},
	})
}

// probeOutboundNode tests one node without storing anything.
//
// 前端只传节点 id，不传节点内容 —— 节点里有代理密码，回给浏览器再送回来
// 等于让凭据在界面上过一圈（也容易被 devtools、扩展、截图带出去）。
// 后端按 id 从vault 取原始节点自己测。
func (s *server) probeOutboundNode(w http.ResponseWriter, r *http.Request) {
	var f struct {
		ID string `json:"id"`
	}
	if !decodeSetup(w, r, &f) {
		return
	}
	if f.ID == "" {
		message(w, 400, "缺少要测试的节点 id。")
		return
	}
	node, err := s.outboundByID(f.ID)
	if err != nil {
		message(w, 404, err.Error())
		return
	}
	res := probeOutbound(r.Context(), node)
	reply(w, 200, res)
}

// outboundByID 从当前池子里取出原始节点。
func (s *server) outboundByID(id string) (json.RawMessage, error) {
	raw, err := s.vault.Get("payment-outbounds")
	if err != nil {
		return nil, errors.New("当前没有配置出站节点。")
	}
	defer clear(raw)
	nodes, perr := proxy.ParseOutboundPool(raw)
	if perr != nil {
		return nil, errors.New("当前出站配置无法解析。")
	}
	for _, n := range nodes {
		if outboundNodeID(n) == id {
			return append(json.RawMessage(nil), n...), nil
		}
	}
	return nil, errors.New("没有找到这个节点（可能已被删除，或刷新过页面）。")
}

// deleteOutboundNode removes one node by id, and clears the pin when it was
// the pinned one.
func (s *server) deleteOutboundNode(w http.ResponseWriter, r *http.Request) {
	var f struct {
		ID string `json:"id"`
	}
	if !decodeSetup(w, r, &f) {
		return
	}
	if f.ID == "" {
		message(w, 400, "缺少要删除的节点 id。")
		return
	}
	raw, err := s.vault.Get("payment-outbounds")
	if err != nil {
		message(w, 400, "当前没有配置出站节点。")
		return
	}
	defer clear(raw)
	nodes, perr := proxy.ParseOutboundPool(raw)
	if perr != nil {
		message(w, 500, "当前出站配置无法解析，请先重新保存一次。")
		return
	}
	kept := make([]json.RawMessage, 0, len(nodes))
	removed := 0
	for _, n := range nodes {
		if outboundNodeID(n) == f.ID {
			removed++
			continue
		}
		kept = append(kept, n)
	}
	if removed == 0 {
		message(w, 404, "没有找到这个节点（可能已被删除，或刷新过页面）。")
		return
	}

	// 重写成新池子前先校验：删剩的配置若不合法，服务下次启动会起不来。
	next, merr := json.Marshal(kept)
	if merr != nil {
		message(w, 500, "序列化剩余节点失败。")
		return
	}
	if len(kept) > 0 {
		if _, verr := proxy.ParseOutboundPool(next); verr != nil {
			message(w, 500, "删除后剩余配置不合法，已放弃删除："+verr.Error())
			return
		}
	}
	defer clear(next)
	if serr := s.vault.Put("payment-outbounds", next); serr != nil {
		message(w, 500, "保存出站池失败。")
		return
	}

	// 删掉的正是被指定的节点时要清掉指定，否则后续付款会一直找不到它，
	// 靠 fallback 兜底虽然能跑，但运营会以为指定还生效。
	if pin, perr := checkout.PinnedPaymentNode(s.vault); perr == nil && pin == f.ID {
		if serr := checkout.SetPinnedPaymentNode(s.vault, ""); serr != nil {
			message(w, 500, "已删除节点，但清除指定失败，请到付款出站页手动改回轮转。")
			return
		}
	}
	reply(w, 200, map[string]any{"ok": true, "removed": removed, "remaining": len(kept)})
}

// pinOutboundNode pins the next payments to one node, or restores rotation.
func (s *server) pinOutboundNode(w http.ResponseWriter, r *http.Request) {
	var f struct {
		ID string `json:"id"`
	}
	if !decodeSetup(w, r, &f) {
		return
	}
	if f.ID != "" {
		raw, err := s.vault.Get("payment-outbounds")
		if err != nil {
			message(w, 400, "当前没有配置出站节点，无法指定。")
			return
		}
		defer clear(raw)
		nodes, perr := proxy.ParseOutboundPool(raw)
		if perr != nil {
			message(w, 500, "当前出站配置无法解析。")
			return
		}
		found := false
		for _, n := range nodes {
			if outboundNodeID(n) == f.ID {
				found = true
				break
			}
		}
		if !found {
			// 指定一个不存在的节点是典型的静默失败：界面显示"已指定"，
			// 实际每次付款都走轮转。所以这里明确报错。
			message(w, 404, "没有找到这个节点，无法指定。")
			return
		}
	}
	if err := checkout.SetPinnedPaymentNode(s.vault, f.ID); err != nil {
		message(w, 500, "保存指定失败。")
		return
	}
	reply(w, 200, map[string]any{"ok": true, "pinned": f.ID})
}

// outboundsStatusExtras exposes cooling details so the panel can show why a
// node is unavailable without the operator guessing.
func (s *server) outboundsStatusExtras(w http.ResponseWriter, r *http.Request) {
	net, err := checkout.PaymentNetworkStatus(s.vault)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		message(w, 500, "读取付款节点状态失败。")
		return
	}
	reply(w, 200, net)
}

var _ = vault.Vault{}
