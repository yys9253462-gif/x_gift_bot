// 边界与异常输入探测。
//
// 前九轮查的都是"正常路径下有没有问题"。这轮专门找异常路径：
// 超长文本、大量数据、极端值、接口失败、网络中断 —— 这些时候
// 界面往往最先崩（溢出、错位、卡死、报一个看不懂的错）。
//
// 用法：node tools/edge.mjs
import puppeteer from "puppeteer-core";

const CHROME = "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";
const BASE = process.env.PREVIEW_BASE || "http://127.0.0.1:4173";

const browser = await puppeteer.launch({
  executablePath: CHROME,
  headless: "shell",
  args: ["--no-sandbox", "--disable-dev-shm-usage", "--hide-scrollbars"],
});

// 探测项必须在页面上下文执行，所以整体定义成一个函数。
const probeOverflow = () => {
  const de = document.documentElement;
  const scrollable = (el) => {
    let a = el.parentElement;
    while (a && a !== document.body) {
      const ox = getComputedStyle(a).overflowX;
      if (ox === "auto" || ox === "scroll" || ox === "hidden") return true;
      a = a.parentElement;
    }
    return false;
  };
  const over = [];
  for (const el of document.querySelectorAll("body *")) {
    const r = el.getBoundingClientRect();
    if (scrollable(el)) continue;
    if (r.width > 0 && r.right > de.clientWidth + 2) {
      over.push(
        `${el.tagName.toLowerCase()}.${String(el.className || "")
          .split(" ")
          .filter(Boolean)
          .slice(0, 1)
          .join(".")} right=${Math.round(r.right)}`,
      );
    }
  }
  return {
    pageOverflow: de.scrollWidth - de.clientWidth,
    overCount: over.length,
    samples: over.slice(0, 3),
    // 文字被容器裁掉（而不是换行）也算问题：用户读不到内容。
    // 只查真正承载内容的元素。label / legend / caption 这类会被
    // textOverflow:ellipsis 命中，但它们本来就会裁 —— label 是控件的
    // 名字，视觉上截断不影响使用。第一版把它们算成"文字被裁切"，
    // 报出"批次名称（可选）"这种毫无问题的内容。
    clipped: [...document.querySelectorAll("body *")]
      .filter((el) => {
        if (["LABEL", "LEGEND", "CAPTION", "TITLE"].includes(el.tagName)) return false;
        const cs = getComputedStyle(el);
        if (cs.overflow === "hidden" || cs.overflowY === "hidden" || cs.textOverflow === "ellipsis") {
          return el.scrollWidth > el.clientWidth + 4 && el.textContent.trim().length > 6;
        }
        return false;
      })
      .map((el) => (el.textContent || "").replace(/\s+/g, " ").trim().slice(0, 30))
      .slice(0, 3),
  };
};

const page = await browser.newPage();
await page.setViewport({ width: 1440, height: 1000 });
await page.goto(`${BASE}/admin`, { waitUntil: "networkidle2" });

const issues = [];

// ---------- 边界 1：超长文本 ----------
console.log("=== 边界：超长文本 ===");
await page.evaluate(() => {
  const items = [...document.querySelectorAll('[role="button"],button')];
  const hit = items.find((el) => (el.textContent || "").includes("立即赠送"));
  if (hit) hit.click();
});
await new Promise((r) => setTimeout(r, 900));

const longText = "a".repeat(60);
await page.evaluate((v) => {
  const input = [...document.querySelectorAll("input")].find(
    (i) => (i.placeholder || "").includes("例如"),
  );
  if (input) input.focus();
}, longText);
// 用真实键盘输入来测 maxLength。
// 原生 maxLength 只拦键盘/粘贴，不拦程序化 setter赋值 —— 第一版用
// setter 直接塞 500 字符，然后读出length=500，就断言"maxLength 未生效"。
// 那是测试方法错了，不是代码错了：真实用户打不出 500 个字符。
await page.keyboard.type(longText, { delay: 0 });
await new Promise((r) => setTimeout(r, 300));

const r1 = await page.evaluate(probeOverflow);
console.log(`  60 次键盘输入后：页面横向溢出 ${r1.pageOverflow}px，越界元素 ${r1.overCount} 个`);
if (r1.pageOverflow > 2) {
  issues.push(`超长输入导致页面横向溢出 ${r1.pageOverflow}px`);
  console.log(`    ${r1.samples.join("; ")}`);
} else {
  console.log(`  ✓ 无溢出`);
}
const inputLen = await page.evaluate(() => {
  const input = [...document.querySelectorAll("input")].find((i) => i.maxLength > 0);
  return { maxLength: input ? input.maxLength : null, length: input ? input.value.length : null };
});
console.log(`  maxLength=${inputLen.maxLength}，键盘输入后长度=${inputLen.length}`);
if (inputLen.maxLength && inputLen.length > inputLen.maxLength) {
  issues.push(`键盘输入可超过 maxLength=${inputLen.maxLength}（实际 ${inputLen.length}）`);
} else if (inputLen.maxLength) {
  console.log(`  ✓ maxLength 拦住了键盘输入`);
}

// ---------- 边界 2：纯空白输入 ----------
console.log("\n=== 边界：纯空白输入 ===");
await page.evaluate(() => {
  const input = [...document.querySelectorAll("input")].find((i) => i.maxLength > 0);
  if (!input) return;
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
  setter.call(input, "   ");
  input.dispatchEvent(new Event("input", { bubbles: true }));
});
await new Promise((r) => setTimeout(r, 300));
const wsState = await page.evaluate(() => {
  const btn = [...document.querySelectorAll("button")].find((b) =>
    (b.textContent || "").includes("开始赠送"),
  );
  return btn ? btn.disabled : null;
});
console.log(`  纯空格时按钮禁用: ${wsState}`);
if (wsState === false) {
  issues.push("纯空白输入时提交按钮仍可用 —— 会被后端拒，体验差");
} else {
  console.log(`  ✓ 正确禁用`);
}

// ---------- 边界 3：接口失败 ----------
console.log("\n=== 边界：接口失败 ===");
await page.setRequestInterception(true);
const handler = (req) => {
  if (req.url().includes("/api/admin/gift/plans")) req.abort();
  else req.continue();
};
page.on("request", handler);
await page.reload({ waitUntil: "domcontentloaded" });
await new Promise((r) => setTimeout(r, 1200));
await page.evaluate(() => {
  const items = [...document.querySelectorAll('[role="button"],button')];
  const hit = items.find((el) => (el.textContent || "").includes("立即赠送"));
  if (hit) hit.click();
});
await new Promise((r) => setTimeout(r, 1200));
const failState = await page.evaluate(() => {
  const alerts = [...document.querySelectorAll(".MuiAlert-message")].map((a) =>
    (a.textContent || "").replace(/\s+/g, " ").trim(),
  );
  const btn = [...document.querySelectorAll("button")].find((b) =>
    (b.textContent || "").includes("开始赠送"),
  );
  return { alerts, btnDisabled: btn ? btn.disabled : null, btnFound: !!btn };
});
console.log(`  提示: ${JSON.stringify(failState.alerts)}`);
console.log(`  提交按钮: 存在=${failState.btnFound} 禁用=${failState.btnDisabled}`);
if (failState.alerts.length === 0) {
  issues.push("套餐接口失败时没有任何提示 —— 用户不知道发生了什么");
} else {
  console.log(`  ✓ 有明确提示`);
}
if (failState.btnFound && failState.btnDisabled === false) {
  issues.push("套餐加载失败时提交按钮仍可用 —— 点了必然失败");
}
page.off("request", handler);
await page.setRequestInterception(false);

// ---------- 边界 4：大量数据下的布局 ----------
console.log("\n=== 边界：任务列表大量数据 ===");
await page.goto(`${BASE}/admin`, { waitUntil: "networkidle2" });
await page.evaluate(() => {
  const items = [...document.querySelectorAll('[role="button"],button')];
  const hit = items.find((el) => (el.textContent || "").includes("兑换码"));
  if (hit) hit.click();
});
await new Promise((r) => setTimeout(r, 900));
const r4 = await page.evaluate(probeOverflow);
console.log(`  兑换码页：横向溢出 ${r4.pageOverflow}px，越界 ${r4.overCount} 个`);
console.log(`  文字被裁切: ${r4.clipped.length} 处${r4.clipped.length ? " → " + r4.clipped.join(" | ") : ""}`);

await browser.close();

console.log("\n=== 汇总 ===");
if (issues.length === 0) console.log("边界测试未发现问题。");
else issues.forEach((i, n) => console.log(`  ${n + 1}. ${i}`));
process.exitCode = issues.length ? 1 : 0;