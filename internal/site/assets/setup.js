"use strict";
const form = document.getElementById("setup-form");
const note = document.getElementById("note");
const button = document.getElementById("submit");
let saved = false;
async function post(path, body) {
  const response = await fetch(path, {
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body), credentials: "same-origin"
  });
  const data = await response.json();
  if (!response.ok) throw new Error(data.message || data.error || "操作失败，请重试。");
  return data;
}
form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const setupPassword = document.getElementById("setup-password").value;
  const password = document.getElementById("admin-password").value;
  if (!saved && password !== document.getElementById("admin-confirm").value) {
    note.textContent = "两次后台密码不一致。"; return;
  }
  if (!saved && (new TextEncoder().encode(password).length < 32 || password.trim() !== password)) {
    note.textContent = "后台密码至少 32 字节，且不能包含首尾空格。"; return;
  }
  button.disabled = true;
  try {
    if (!saved) {
      await post("/api/setup/apply", { setup_password: setupPassword, admin_password: password });
      saved = true;
      document.getElementById("admin-password").value = "";
      document.getElementById("admin-confirm").value = "";
      document.getElementById("admin-password").required = false;
      document.getElementById("admin-confirm").required = false;
      form.querySelectorAll("input").forEach((input) => { input.readOnly = true; });
    }
    note.textContent = "管理员已创建，正在重启服务。登录用户名：admin。";
    await post("/api/setup/restart", { setup_password: setupPassword });
    for (let i = 0; i < 40; i++) {
      await new Promise((resolve) => setTimeout(resolve, 1000));
      try {
        const response = await fetch("/api/setup/status", { cache: "no-store" });
        if (response.status === 404) { window.location.assign("/admin"); return; }
      } catch (_) { /* The service may be restarting. */ }
    }
    note.textContent = "管理员已创建。服务重启尚未完成，请稍后打开后台；必要时运行 systemctl restart xgift。";
    document.getElementById("admin-link").hidden = false;
  } catch (error) {
    note.textContent = (saved ? "密码已保存，请勿重复初始化。点击按钮可重试重启。\n" : "") + error.message;
  } finally {
    button.disabled = false;
    if (saved) button.textContent = "重试重启服务";
  }
});
