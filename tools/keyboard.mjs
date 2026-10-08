// 纯键盘可操作性探测：只用 Tab / Enter / 空格，能走完核心流程吗。
//
// 理由：很多界面鼠标一切正常，键盘完全走不通 —— 侧栏项是 div、
// 弹窗不锁焦点、关闭按钮顺序不对。这类问题靠读代码看不出来，
// 必须真的按 Tab 看焦点落在哪。
//
// 用法：node tools/keyboard.mjs
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

const describe = () => {
  const el = document.activeElement;
  if (!el || el === document.body) return { tag: "(body)", text: "", focusVisible: false };
  const cs = getComputedStyle(el);
  const r = el.getBoundingClientRect();
  return {
    tag: el.tagName.toLowerCase(),
    role: el.getAttribute("role") || "",
    text: (el.textContent || el.getAttribute("aria-label") || el.placeholder || "")
      .replace(/\s+/g, " ")
      .trim()
      .slice(0, 30),
    tabIndex: el.tabIndex,
    disabled: el.disabled === true,
    // 焦点可见性：轮廓或阴影必须与未聚焦时有区别
    focusVisible:
      cs.outlineStyle !== "none" && parseFloat(cs.outlineWidth) > 0
        ? cs.outlineWidth + " " + cs.outlineColor
        : cs.boxShadow !== "none"
          ? "box-shadow"
          : "无",
    inViewport: r.top >= 0 && r.bottom <= window.innerHeight,
    size: `${Math.round(r.width)}×${Math.round(r.height)}`,
  };
};

await page.goto(`${BASE}/admin`, { waitUntil: "networkidle2", timeout: 30000 });
await new Promise((r) => setTimeout(r, 500));

// 焦点必须落在文档里某处，不能一开始就在 body（否则 Tab 起点不可控）
await page.evaluate(() => document.body.focus());

console.log("=== Tab 遍历（前 40 站）===");
const stops = [];
for (let i = 0; i < 40; i++) {
  await page.keyboard.press("Tab");
  const d = await page.evaluate(describe);
  stops.push(d);
}

let invisible = 0;
let inBody = 0;
const seen = [];
for (const s of stops) {
  if (s.focusVisible === "无") invisible++;
  if (s.tag === "(body)") inBody++;
  const key = `${s.tag}:${s.text}`;
  if (seen.includes(key)) continue;
  seen.push(key);
}
console.log(`共 40 站，去重后 ${seen.length} 个不同落点`);
console.log(`焦点不可见的站: ${invisible}，焦点丢失回 body 的站: ${inBody}`);
console.log("\n=== 焦点不可见的位置（要修）===");
for (const [i, st] of stops.entries()) {
  if (st.focusVisible === "无") console.log(`  第 ${i + 1} 站: ${st.tag} "${st.text}"`);
}
console.log("\n前 18 站:");
for (const s of stops.slice(0, 18)) {
  const flag = s.focusVisible === "无" ? " ✗无焦点样式" : "";
  console.log(`  ${s.tag.padEnd(8)} ${s.size.padEnd(9)} ${s.text || "(无文字)"}${flag}`);
}

// 侧栏必须能用键盘到达并激活
console.log("\n=== 侧栏键盘可达性 ===");
const sidebarReach = await page.evaluate(() => {
  const items = [...document.querySelectorAll('[role="button"]')].filter((el) =>
    (el.textContent || "").includes("立即赠送"),
  );
  if (!items.length) return { ok: false, why: "侧栏项不是 role=button" };
  const el = items[0];
  return {
    ok: true,
    tabIndex: el.tabIndex,
    tag: el.tagName.toLowerCase(),
    focusable: el.tabIndex >= 0,
  };
});
console.log(`  ${JSON.stringify(sidebarReach)}`);

// Enter 能否激活当前焦点上的按钮
console.log("\n=== Enter 激活测试 ===");
await page.goto(`${BASE}/admin`, { waitUntil: "networkidle2" });
// 必须先走到「立即赠送」页 —— 第一版忘了这步，在兑换码页上找
// "开始赠送"按钮，found:false，然后把这个 false 读成"Enter 无反应"。
// 找不到元素却仍给出结论，是这类探测最容易犯的错。
await page.evaluate(() => {
  const items = [...document.querySelectorAll('[role="button"],button')];
  const hit = items.find((el) => (el.textContent || "").includes("立即赠送"));
  if (hit) hit.click();
});
await new Promise((r) => setTimeout(r, 900));
await page.evaluate(() => {
  const btn = [...document.querySelectorAll("button")].find((b) =>
    (b.textContent || "").includes("开始赠送"),
  );
  if (btn) btn.focus();
});
const before = await page.evaluate(describe);
// btn.focus() 对disabled 按钮无效，所以焦点可能根本没落在提交按钮上。
// 这时 describe() 描述的是 body，下面的判断就会走错分支。
// 所以先显式确认焦点到底在谁身上。
const focusedTag = before.tag;
if (focusedTag === "(body)" || before.disabled) {
  // 按钮为禁用态：先填账号让它可用，否则测的是"禁用态是否响应 Enter"，
  // 那不是键盘缺陷（第一版就踩了这个，读成"Enter 无反应"）。
  console.log(`  提交按钮当前 ${before.disabled ? "禁用" : "未聚焦"}，先填账号再测`);
  await page.evaluate(() => {
    const input = [...document.querySelectorAll("input")].find(
      (i) => (i.placeholder || "").includes("例如"),
    );
    if (!input) return;
    const setter = Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype, "value").set;
    setter.call(input, "keyboard_test_user");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await new Promise((r) => setTimeout(r, 400));
  // 重新聚焦：disabled 时 focus() 无效，填值后按钮才可用。
  const focusInfo = await page.evaluate(() => {
    const btn = [...document.querySelectorAll("button")].find((b) =>
      (b.textContent || "").includes("开始赠送"),
    );
    if (!btn) return { found: false };
    btn.focus();
    return {
      found: true,
      disabled: btn.disabled,
      ariaDisabled: btn.getAttribute("aria-disabled"),
      focusedIsBtn: document.activeElement === btn,
      activeTag: document.activeElement ? document.activeElement.tagName : "none",
      inputValue: [...document.querySelectorAll("input")]
        .map((i) => i.value)
        .filter(Boolean)
        .join(","),
    };
  });
  console.log(`  聚焦详情: ${JSON.stringify(focusInfo)}`);
  const filledBtn = await page.evaluate(describe);
  console.log(`  填值并聚焦后: ${JSON.stringify(filledBtn)}`);
  if (filledBtn.disabled) {
    console.log("  结论[SKIP]: 填了值按钮仍禁用，本项不构成有效测试");
  } else {
    await page.keyboard.press("Enter");
    await new Promise((r) => setTimeout(r, 600));
    const after = await page.evaluate(describe);
    console.log(`  Enter 后: ${JSON.stringify(after)}`);
    console.log(
      `  ${after.text !== filledBtn.text || after.disabled ? "✓ Enter 激活了按钮" : "✗ Enter 无反应"}`,
    );
  }
} else {
  // 填了值但按钮文案没变，可能是：按钮仍禁用、或 Enter 确实没触发。
  // 两种情况结论完全不同，所以分别观测：请求是否发出 + 文案是否变化。
  // 分别测 Enter 与 空格：原生 button 对两者都该响应。
  // 只测一个就下结论容易误判 —— 若只有空格有效，说明是组件层级的问题。
  const probe = await page.evaluate(() => {
    window.__enterSeen = false;
    document.addEventListener("submit", () => { window.__enterSeen = true; }, { once: true });
    const f = document.querySelector("form");
    return { hasForm: !!f };
  });
  console.log(`  页面上有 form: ${probe.hasForm}`);
  await page.keyboard.press("Enter");
  await new Promise((r) => setTimeout(r, 1500));
  const sawSubmit = await page.evaluate(() => window.__enterSeen === true);
  console.log(`  Enter 后是否触发了 form submit 事件: ${sawSubmit}`);
  const after = await page.evaluate(describe);
  const alerts = await page.evaluate(() =>
    [...document.querySelectorAll(".MuiAlert-message")].map((a) =>
      (a.textContent || "").replace(/\s+/g, " ").trim().slice(0, 60),
    ),
  );
  console.log(`  页面提示: ${JSON.stringify(alerts)}`);
  console.log(`  聚焦: ${JSON.stringify(before)}`);
  console.log(`  Enter 后: ${JSON.stringify(after)}`);
  const changed = after.text !== before.text || after.disabled !== before.disabled;
  console.log(`  ${changed ? "✓ Enter 激活了按钮" : "（按钮状态未变）"}`);
  console.log(
    `  注：按钮是 type=submit 且包在 <form onSubmit> 里，Enter 走的是表单提交路径。` +
      ` 若按钮仍禁用则 Enter 不该有反应 —— 那要看disabled 而不是 Enter。`,
  );
  console.log(`  当前按钮禁用态: ${after.disabled}`);
}

// 弹窗/抽屉是否锁焦点（打开后焦点应进入其中，Esc 应能关闭）
console.log("\n=== 弹窗焦点管理 ===");
const dialog = await page.evaluate(() => {
  const dlg = document.querySelector('[role="dialog"],[role="alertdialog"],.MuiModal-root');
  if (!dlg) return { present: false };
  return {
    present: true,
    hasFocusTrap: !!dlg.querySelector('[tabindex="0"], [tabindex="-1"]') || dlg.hasAttribute("tabindex"),
    ariaModal: dlg.getAttribute("aria-modal") || dlg.querySelector('[aria-modal="true"]') ? "是" : "否",
  };
});
console.log(`  ${JSON.stringify(dialog)}`);

await browser.close();