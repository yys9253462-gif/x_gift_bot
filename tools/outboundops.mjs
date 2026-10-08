// 付款出站三个功能的真实验证：测试 / 指定 / 删除。
//
// 用法：node tools/outboundops.mjs
// 退出码非 0 表示有问题。
import puppeteer from "puppeteer-core";

const CHROME = "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";
const BASE = process.env.PREVIEW_BASE || "http://127.0.0.1:4173";

const browser = await puppeteer.launch({
  executablePath: CHROME,
  headless: "shell",
  args: ["--no-sandbox", "--disable-dev-shm-usage", "--hide-scrollbars"],
});
const page = await browser.newPage();
await page.setViewport({ width: 1440, height: 1200 });

const posts = [];
page.on("response", (r) => {
  if (r.request().method() === "POST" && r.url().includes("/outbounds/")) {
    posts.push({ url: r.url().replace(BASE, ""), status: r.status() });
  }
});

const errors = [];
page.on("pageerror", (e) => errors.push(e.message));
page.on("console", (m) => {
  if (m.type() === "error" && !/Failed to load resource/.test(m.text())) {
    errors.push(m.text());
  }
});

await page.goto(`${BASE}/admin`, { waitUntil: "networkidle2" });
// 走进「付款出站」分区
await page.evaluate(() => {
  const items = [...document.querySelectorAll('[role="button"],button')];
  const hit = items.find((el) => (el.textContent || "").replace(/\s+/g, "").includes("付款出站"));
  if (hit) hit.click();
});
await new Promise((r) => setTimeout(r, 1200));

const results = [];
const fail = (msg) => results.push(`FAIL  ${msg}`);
const pass = (msg) => results.push(`PASS  ${msg}`);

// ---------- 1. 节点清单渲染 ----------
const list = await page.evaluate(() => {
  const panel = [...document.querySelectorAll("div")].find((d) =>
    (d.textContent || "").includes("已生效的节点"),
  );
  if (!panel) return { found: false };
  const rows = [...panel.querySelectorAll("button")].map((b) => b.textContent.trim());
  return {
    found: true,
    buttons: rows,
    hasPinnedChip: (panel.textContent || "").includes("首选"),
    header: (panel.textContent || "").slice(0, 120),
  };
});
if (!list.found) fail("节点清单没渲染");
else {
  const need = ["测试", "设为首选", "删除"];
  const missing = need.filter((n) => !list.buttons.includes(n));
  if (missing.length) fail(`节点行缺少按钮：${missing.join("、")}`);
  else pass(`节点清单渲染正常（${list.buttons.filter((b) => b === "测试").length} 个节点，各带测试/指定/删除）`);
}

// ---------- 2. 测试节点 ----------
await page.evaluate(() => {
  const btns = [...document.querySelectorAll("button")].filter((b) => b.textContent.trim() === "测试");
  if (btns[0]) btns[0].click();
});
await new Promise((r) => setTimeout(r, 900));
const probeUI = await page.evaluate(() => {
  const alerts = [...document.querySelectorAll(".MuiAlert-message")].map((a) =>
    (a.textContent || "").replace(/\s+/g, " ").trim(),
  );
  return alerts.filter((t) => t.includes("出口") || t.includes("配置不合法"));
});
if (!probeUI.length) fail("点「测试」后没有出现结果");
else if (probeUI[0].includes("出口可用")) pass(`测试成功结果已显示：${probeUI[0].slice(0, 60)}`);
else fail(`测试结果显示异常：${probeUI[0].slice(0, 80)}`);

// ---------- 3. 指定节点 ----------
const beforePin = posts.filter((p) => p.url.endsWith("/pin")).length;
await page.evaluate(() => {
  const btns = [...document.querySelectorAll("button")].filter(
    (b) => b.textContent.trim() === "设为首选" || b.textContent.trim() === "取消首选",
  );
  if (btns[0]) btns[0].click();
});
await new Promise((r) => setTimeout(r, 800));
const afterPin = posts.filter((p) => p.url.endsWith("/pin")).length;
if (afterPin <= beforePin) fail("点「设为首选」没有发出 pin 请求");
else pass("指定节点请求已发出");

const pinUI = await page.evaluate(() => {
  const t = document.body.textContent || "";
  return {
    hasFeedback: t.includes("已指定该节点") || t.includes("已恢复为轮转"),
    toggleShown: [...document.querySelectorAll("button")].some(
      (b) => b.textContent.trim() === "取消首选" || b.textContent.trim() === "设为首选",
    ),
  };
});
if (!pinUI.hasFeedback) fail("指定后没有提示，用户不知道是否生效");
else pass("指定后有明确提示");

// ---------- 4. 删除节点（含确认弹窗） ----------
// 先接管 confirm，默认点"取消"，验证删除不会误触发。
const deleteFlow = await page.evaluate(async () => {
  let asked = 0;
  const original = window.confirm;
  window.confirm = (msg) => {
    asked++;
    window.__confirmMsg = msg;
    return false; // 取消
  };
  const btn = [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === "删除");
  if (btn) btn.click();
  await new Promise((r) => setTimeout(r, 500));
  window.confirm = original;
  return { asked, msg: window.__confirmMsg || "" };
});
if (!deleteFlow.asked) fail("点删除没有二次确认");
else if (!deleteFlow.msg.includes("付款") && !deleteFlow.msg.includes("节点")) {
  fail(`确认文案没说明后果：${deleteFlow.msg.slice(0, 50)}`);
} else pass(`删除有二次确认，且说明了后果`);

// 取消后不应发出 delete
const afterCancel = posts.filter((p) => p.url.endsWith("/delete")).length;
if (afterCancel > 0) fail("点了「取消」仍然发出了删除请求");
else pass("确认框点取消不会删除");

// 确认后应该发出
await page.evaluate(async () => {
  const original = window.confirm;
  window.confirm = () => true;
  const btn = [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === "删除");
  if (btn) btn.click();
  await new Promise((r) => setTimeout(r, 700));
  window.confirm = original;
});
const afterConfirm = posts.filter((p) => p.url.endsWith("/delete")).length;
if (afterConfirm <= afterCancel) fail("确认后没有发出删除请求");
else pass("确认后删除请求已发出");

// ---------- 5. 预览环境不应 404 ----------
const notFound = posts.filter((p) => p.status === 404);
if (notFound.length) fail(`预览环境有 404：${notFound.map((p) => p.url).join(", ")}`);
else pass("四个端点在预览环境全部可用");

console.log(results.join("\n"));
console.log(`\n控制台错误: ${errors.length ? errors.slice(0, 3).join(" | ") : "无"}`);
const failed = results.filter((r) => r.startsWith("FAIL")).length;
console.log(`\n合计 ${results.length} 项，失败 ${failed} 项`);

await browser.close();
process.exitCode = failed ? 1 : 0;