"use strict";

// First-run setup form. Collects every credential in one submission, then
// asks the server to restart itself and waits until /setup is gone.

const $ = (id) => document.getElementById(id);
const note = $("note");
const submit = $("submit");

function say(text, bad) {
  note.textContent = text;
  note.className = bad ? "note bad" : "note ok";
}

function val(id) {
  const el = $(id);
  return el ? el.value.trim() : "";
}

// 解析粘贴进来的 Cookie。操作者手上拿到的形态五花八门，这里尽量宽容：
//   1. auth_token=abc; ct0=def          （curl / 浏览器请求头原文）
//   2. {"auth_token":"abc","ct0":"def"} （JSON）
//   3. [{"name":"auth_token","value":"abc"}, ...] （浏览器扩展导出的数组）
//   4. 整段 Cookie 文本（含其它无关 cookie），只要里面能找到这两项
function parseCookies(text) {
  const out = { auth_token: "", ct0: "", authorization: "", user_agent: "" };
  if (!text) return out;
  const raw = text.trim();
  if (!raw) return out;

  // 形态 2/3：先按 JSON 试
  if (raw.startsWith("{") || raw.startsWith("[")) {
    try {
      const doc = JSON.parse(raw);
      const list = Array.isArray(doc) ? doc : (Array.isArray(doc.cookies) ? doc.cookies : [doc]);
      for (const item of list) {
        if (!item || typeof item !== "object") continue;
        // 有的导出格式用 name/value，有的直接是键值对
        if (item.name && item.value !== undefined) {
          const k = String(item.name).toLowerCase();
          if (k === "auth_token" || k === "ct0") out[k] = String(item.value);
        }
        for (const key of ["auth_token", "ct0", "authorization", "user_agent"]) {
          if (item[key] !== undefined) out[key] = String(item[key]);
        }
      }
      // 兼容 {cookies:{...}} 这种嵌套
      if (doc.cookies && !Array.isArray(doc.cookies) && typeof doc.cookies === "object") {
        for (const key of ["auth_token", "ct0"]) {
          if (doc.cookies[key] !== undefined) out[key] = String(doc.cookies[key]);
        }
      }
      return out;
    } catch (e) {
      // 不是合法 JSON，继续按文本处理
    }
  }

  // 形态 1/4：按 cookie 串扫描
  const pairs = raw.split(/[;\n]+/);
  for (const pair of pairs) {
    const i = pair.indexOf("=");
    if (i < 0) continue;
    const k = pair.slice(0, i).trim().toLowerCase().replace(/^cookie:\s*/, "");
    const v = pair.slice(i + 1).trim().replace(/^"|"$/g, "");
    if (k === "auth_token" || k === "ct0") out[k] = v;
  }
  // 形态 1 若带 "Cookie: " 前缀，上面那步已通过 replace 处理
  return out;
}

const pasteBox = $("cookie_paste");
const pasteNote = $("paste_note");

function applyPaste() {
  const parsed = parseCookies(pasteBox.value);
  const filled = [];
  for (const key of ["auth_token", "ct0"]) {
    if (parsed[key]) {
      $(key).value = parsed[key];
      filled.push(key);
    }
  }
  // Authorization / User-Agent 是可选项，粘到了就顺手填上，不覆盖用户已填内容
  for (const key of ["authorization", "user_agent"]) {
    if (parsed[key] && !$(key).value.trim()) {
      $(key).value = parsed[key];
      filled.push(key);
    }
  }
  if (!pasteBox.value.trim()) {
    pasteNote.textContent = "";
    pasteNote.className = "hint";
    return;
  }
  if (filled.length === 0) {
    pasteNote.textContent = "没能从这段内容里认出 auth_token / ct0，请检查粘贴的原文。";
    pasteNote.className = "note bad";
    return;
  }
  pasteNote.textContent = "已识别并填入：" + filled.join("、") +
    (filled.length === 2 ? "。" : "（另一项请手动补上）。");
  pasteNote.className = "note ok";
}

pasteBox.addEventListener("input", applyPaste);
pasteBox.addEventListener("paste", function () {
  // 粘贴事件里读不到新值，等浏览器填完再解析
  setTimeout(applyPaste, 0);
});

$("proxy_mode").addEventListener("change", (e) => {
  $("proxy_json_wrap").hidden = e.target.value !== "json";
});

// 页面加载时下拉框已有一个默认选中项，change 事件不会自己触发，
// 所以这里必须手动同步一次，否则默认「直连」时 JSON 框仍然露在外面。
function syncProxy() {
  $("proxy_json_wrap").hidden = $("proxy_mode").value !== "json";
}
syncProxy();

function readPlans() {
  const plans = [];
  for (const n of [1, 2]) {
    const months = val("plan" + n + "_months");
    if (!months) continue;
    plans.push({
      months: Number(months),
      amount: val("plan" + n + "_amount"),
      product: val("plan" + n + "_product"),
    });
  }
  return plans;
}

submit.addEventListener("click", async () => {
  const admin = $("admin_password").value;
  if (admin !== $("admin_password_confirm").value) {
    say("两次输入的管理员密码不一致。", true);
    return;
  }
  if (admin.length < 32) {
    say("管理员密码至少需要 32 个字符。", true);
    return;
  }
  const body = {
    setup_password: $("setup_password").value,
    auth_token: val("auth_token"),
    ct0: val("ct0"),
    authorization: val("authorization"),
    user_agent: val("user_agent"),
    cards: [
      {
        number: val("card_number"),
        exp_month: val("card_exp_month"),
        exp_year: val("card_exp_year"),
        cvc: val("card_cvc"),
        billing_name: val("card_name"),
        email: val("card_email"),
        billing_country: val("card_country"),
        billing_postal_code: val("card_postal"),
        billing_city: val("card_city"),
        billing_state: val("card_state"),
        billing_address_line1: val("card_line1"),
        billing_address_line2: val("card_line2"),
      },
    ],
    stripe_key: val("stripe_key"),
    merchant: val("merchant"),
    currency: val("currency"),
    plans: readPlans(),
    proxy_mode: val("proxy_mode") || "direct",
    proxy_json: $("proxy_json").value,
    admin_password: admin,
  };

  submit.disabled = true;
  say("正在保存…", false);
  try {
    const res = await fetch("/api/setup/apply", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    const data = await res.json().catch(function () { return {}; });
    if (!res.ok) {
      say(data.message || "保存失败（HTTP " + res.status + "）。", true);
      submit.disabled = false;
      return;
    }
    say("凭据已保存，正在重启服务使配置生效…", false);
    submit.textContent = "重启中…";
    await fetch("/api/setup/restart", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ setup_password: body.setup_password }),
    });
    for (let i = 0; i < 40; i += 1) {
      await new Promise(function (r) { setTimeout(r, 1000); });
      try {
        const probe = await fetch("/api/setup/status", { cache: "no-store" });
        if (probe.ok) {
          const state = await probe.json();
          if (state.bootstrap === false) {
            say("初始化完成，正在跳转到站点首页…", false);
            setTimeout(function () { location.href = "/"; }, 800);
            return;
          }
        }
      } catch (ignored) {
        // 服务正在重启，连接被拒绝是预期结果，继续等。
      }
    }
    say("服务仍在重启，请稍后手动刷新页面。", true);
  } catch (err) {
    say("网络错误：" + err.message, true);
    submit.disabled = false;
  }
});

fetch("/api/setup/status", { cache: "no-store" })
  .then(function (r) { return r.ok ? r.json() : null; })
  .then(function (state) {
    if (state && state.bootstrap === false) {
      say("本站点已完成初始化，本页面不再可用。", true);
      submit.disabled = true;
    }
  })
  .catch(function () { /* 状态探测失败不阻塞填写 */ });