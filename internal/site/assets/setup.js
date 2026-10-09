"use strict";
const form = document.getElementById("setup-form");
const note = document.getElementById("note");
const button = document.getElementById("submit");
const adminPassword = document.getElementById("admin-password");
const adminConfirm = document.getElementById("admin-confirm");
// saved means the administrator password is on disk. It is recovered from
// /api/setup/status on load, so a lost apply response no longer leaves the
// operator re-submitting a form whose work already succeeded.
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

async function status() {
  const response = await fetch("/api/setup/status", { cache: "no-store" });
  if (response.status === 404) return null;
  if (!response.ok) throw new Error("无法读取初始化状态，请稍后重试。");
  return await response.json();
}

// Once the password is stored the two fields are evidence of nothing, so they
// stop being required and the form offers only the restart.
function markSaved() {
  saved = true;
  adminPassword.value = "";
  adminConfirm.value = "";
  adminPassword.required = false;
  adminConfirm.required = false;
  form.querySelectorAll("input").forEach((input) => { input.readOnly = true; });
  button.textContent = "重试重启服务";
}

function validate(password) {
  if (password !== adminConfirm.value) {
    note.textContent = "两次后台密码不一致。";
    return false;
  }
  const bytes = new TextEncoder().encode(password).length;
  if (bytes < 32 || bytes > 256 || password.trim() !== password || /[\r\n\0]/.test(password)) {
    note.textContent = "后台密码须为 32–256 UTF-8 字节，不能包含首尾空格、换行或空字符。";
    return false;
  }
  return true;
}

async function waitForRestart() {
  for (let i = 0; i < 40; i++) {
    await new Promise((resolve) => setTimeout(resolve, 1000));
    try {
      const state = await status();
      if (state === null) { window.location.assign("/admin"); return true; }
    } catch (_) { /* The service may be restarting. */ }
  }
  return false;
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const setupPassword = document.getElementById("setup-password").value;
  const password = adminPassword.value;
  if (!saved && !validate(password)) return;
  button.disabled = true;
  try {
    if (!saved) {
      // Only a response that says ok is a saved password. A 409 stays an error
      // here; the operator retries after the page recovered the real state.
      const data = await post("/api/setup/apply", { setup_password: setupPassword, admin_password: password });
      markSaved();
      note.textContent = "管理员已创建，正在重启服务。登录用户名：admin。";
      if (data.already_saved) note.textContent = "管理员密码已存在（与本次提交一致），正在重启服务。登录用户名：admin。";
    } else {
      note.textContent = "管理员已创建，正在重启服务。登录用户名：admin。";
    }
    await post("/api/setup/restart", { setup_password: setupPassword });
    if (!await waitForRestart()) {
      note.textContent = "管理员已创建。服务重启尚未完成，请稍后打开后台；必要时运行 systemctl restart xgift。";
      document.getElementById("admin-link").hidden = false;
    }
  } catch (error) {
    note.textContent = (saved ? "密码已保存，请勿重复初始化。点击按钮可重试重启。\n" : "") + error.message;
  } finally {
    button.disabled = false;
    if (saved) button.textContent = "重试重启服务";
  }
});

(async () => {
  try {
    const state = await status();
    if (state && state.admin_saved) {
      markSaved();
      note.textContent = "管理员密码已保存。点击按钮重启服务完成初始化。";
    }
  } catch (error) {
    note.textContent = error.message;
  }
})();
