// 防重复提交探测：真的连点，看有没有节流。
//
// 这是付款类操作最容易出事的地方 —— 界面卡一下、用户以为没点上、
// 又点一次，就可能下两单。所以"点了之后立刻再点"必须无效，
// 而且界面上要有可见的"正在处理中"。
//
// 用法：node tools/doublesubmit.mjs
import puppeteer from "puppeteer-core";

const CHROME = "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";
const BASE = process.env.PREVIEW_BASE || "http://127.0.0.1:4173";

const browser = await puppeteer.launch({
  executablePath: CHROME,
  headless: "shell",
  args: ["--no-sandbox", "--disable-dev-shm-usage", "--hide-scrollbars"],
});

const page = await browser.newPage();
await page.setViewport({ width: 1440, height: 1000 });

let posts = 0;
const postUrls = [];
page.on("request", (r) => {
  if (r.method() !== "POST") return;
  posts++;
  postUrls.push(r.url().replace(BASE, ""));
});

await page.goto(`${BASE}/admin`, { waitUntil: "networkidle2", timeout: 30000 });

// 走到「立即赠送」—— 最容易重复扣款的一页。
await page.evaluate(() => {
  const items = [...document.querySelectorAll('[role="button"],button')];
  const hit = items.find((el) =>
    (el.textContent || "").replace(/\s+/g, "").startsWith("立即赠送"),
  );
  if (hit) hit.click();
});
await new Promise((r) => setTimeout(r, 800));

// 填账号，让提交按钮变为可用。
// React 是受控组件：必须走原生 setter + input 事件，直接赋值会被
// 下一次渲染覆盖。第一版忘了这点，结果按钮始终 disabled，狂点 5 次
// 发出 0 个请求，却被我读成"防重复提交正常" —— 假通过。
const filled = await page.evaluate(() => {
  const input = [...document.querySelectorAll("input")].find(
    (i) =>
      (i.placeholder || "").includes("例如") ||
      (i.getAttribute("aria-label") || "").includes("接收账号") ||
      // MUI outlined 的 label 会渲染成 <label>，用文字反查最稳。
      [...document.querySelectorAll("label")].some((l) => l.textContent.includes("接收账号")),
  );
  if (!input) {
    const seen = [...document.querySelectorAll("input")]
      .map((i) => i.placeholder || i.type || "?")
      .slice(0, 6);
    return { ok: false, why: "找不到接收账号输入框，页面上有: " + seen.join(" | ") };
  }
  const setter = Object.getOwnPropertyDescriptor(
    window.HTMLInputElement.prototype,
    "value",
  ).set;
  setter.call(input, "test_user_dup");
  input.dispatchEvent(new Event("input", { bubbles: true }));
  return { ok: input.value === "test_user_dup", why: input.value };
});
console.log(`填值结果: ${JSON.stringify(filled)}`);
await new Promise((r) => setTimeout(r, 400));

// 读提交按钮状态。这个函数在页面上下文执行，不能引用外部变量。
const readSubmit = () => {
  // 不能按文案找：请求期间按钮文案会变成"正在赠送…"，按"开始赠送"
  // 匹配就会miss掉，那正是我们要观察的中间态。
  // 改成记住初始按钮的位置，再按位置取。
  const btns = [...document.querySelectorAll("button")];
  const idx = window.__submitIdx;
  const btn = idx >= 0 ? btns[idx] : undefined;
  if (!btn) return null;
  return {
    text: btn.textContent.replace(/\s+/g, " ").trim(),
    disabled: btn.disabled === true || btn.getAttribute("aria-disabled") === "true",
    hasSpinner: !!btn.querySelector(".MuiCircularProgress-root"),
  };
};

// 记录提交按钮在按钮列表中的位置，供后续读取复用。
await page.evaluate(() => {
  const btns = [...document.querySelectorAll("button")];
  const i = btns.findIndex((b) => (b.textContent || "").includes("开始赠送"));
  window.__submitIdx = i;
});
const before = await page.evaluate(readSubmit);
console.log(`提交按钮初始: ${JSON.stringify(before)}`);
if (!before || before.disabled) {
  console.log("\n结论[INCONCLUSIVE]: 按钮不可点，本次不构成有效测试");
  await browser.close();
  process.exit(2);
}

// 连点 5 次 —— 模拟用户不耐烦地狂点。
await page.evaluate(() => {
  const btns = [...document.querySelectorAll("button")];
  const btn = btns[window.__submitIdx];
  for (let i = 0; i < 5; i++) btn.click();
});
console.log("已连点 5 次");

const immediately = await page.evaluate(readSubmit);
console.log(`点击后立刻: ${JSON.stringify(immediately)}`);

await new Promise((r) => setTimeout(r, 5000));
const settled = await page.evaluate(readSubmit);
console.log(`稳定后: ${JSON.stringify(settled)}`);

console.log(`\nPOST 请求数: ${posts}`);
console.log(`POST 目标: ${[...new Set(postUrls)].join(", ") || "（无）"}`);

const ok = posts <= 1;
const showedFeedback = !!(immediately && (immediately.disabled || immediately.hasSpinner));
let verdict = "PASS";
if (!ok) verdict = "FAIL";
else if (!showedFeedback) verdict = "PASS_NO_FEEDBACK";

console.log(`\n结论[${verdict}]`);
if (!ok) console.log(`  产生了 ${posts} 次 POST，可能重复下单/重复扣款`);
else if (!showedFeedback)
  console.log(`  只发了 ${posts} 次请求，但按钮没有可见的禁用/加载反馈 —— 用户不知道是否已提交`);
else console.log(`  只发 ${posts} 次请求，且按钮有明确反馈`);

await browser.close();
process.exit(ok ? 0 : 1);