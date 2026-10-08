// 交互体验探测：真的去点按钮，测量反馈时间与状态覆盖。
//
// 前面五轮查的是"有没有 bug"（对比度、面积、标签）。这轮查的是
// "用起来顺不顺手"——点下去多久有反应、加载时有没有遮住界面、
// 能不能重复点、有没有空状态引导。
//
// 这些没法靠读代码判断：按钮的 disabled 时机、状态覆盖范围、
// 请求耗时，都只有真的点下去才知道。
//
// 用法：node tools/interact.mjs [--page=立即赠送] [--scheme=light]
import puppeteer from "puppeteer-core";

const CHROME = "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";
const BASE = process.env.PREVIEW_BASE || "http://127.0.0.1:4173";

const arg = (n, d) => {
  const hit = process.argv.find((a) => a.startsWith(`--${n}=`));
  return hit ? hit.split("=")[1] : d;
};

const PAGES = [
  ["codes", "兑换码"],
  ["lookup", "查询"],
  ["credentials", "X 登录凭据"],
  ["cards", "支付卡"],
  ["catalog", "商品与价格"],
  ["outbounds", "付款出站"],
  ["proxy", "查询出口"],
  ["gift", "立即赠送"],
  ["ops", "运维"],
];

const only = arg("page", "");
const scheme = arg("scheme", "light");

// 在页面上下文执行：找出所有可点按钮及其状态快照。
// 注意：辅助函数必须定义在这里内部，不能引用模块作用域——
// 这个函数会被序列化后送进浏览器。第一版就这么踩过。
const snapshot = () => {
  const pick = (el) => {
    const cs = getComputedStyle(el);
    const r = el.getBoundingClientRect();
    return {
      text: (el.textContent || "").replace(/\s+/g, " ").trim().slice(0, 24),
      disabled:
        el.disabled === true ||
        el.getAttribute("aria-disabled") === "true" ||
        cs.pointerEvents === "none",
      pointerEvents: cs.pointerEvents,
      cursor: cs.cursor,
      hasSpinner: !!el.querySelector('svg[data-testid="CircularProgress"], .MuiCircularProgress-root'),
      opacity: cs.opacity,
      width: Math.round(r.width),
      height: Math.round(r.height),
      visible: r.width > 0 && r.height > 0 && cs.visibility !== "hidden",
    };
  };
  const buttons = [...document.querySelectorAll("button")]
    .filter((b) => b.getBoundingClientRect().width > 0)
    .map(pick);
  const inputs = [...document.querySelectorAll("input,textarea")]
    .filter((i) => {
      const r = i.getBoundingClientRect();
      return r.width > 2 && getComputedStyle(i).opacity !== "0";
    })
    .map((i) => ({
      name: i.getAttribute("name") || i.id || i.type,
      disabled: i.disabled,
      required: i.required,
      value: i.value.slice(0, 20),
      placeholder: i.placeholder || "",
    }));
  // 全局忙碌遮罩：MUI 的 CircularProgress 直径变大通常是遮罩。
  const overlays = [...document.querySelectorAll(".MuiBackdrop-root, .MuiModal-root")].length;
  return { buttons, inputs, overlays, t: Date.now() };
};

const browser = await puppeteer.launch({
  executablePath: CHROME,
  headless: "shell",
  args: ["--no-sandbox", "--disable-dev-shm-usage", "--hide-scrollbars"],
});

const page = await browser.newPage();
await page.setViewport({ width: 1440, height: 1000, deviceScaleFactor: 1 });
await page.evaluateOnNewDocument((s) => {
  try {
    localStorage.setItem("xgift-mode", s);
  } catch {}
}, scheme);

const netLog = [];
page.on("response", (r) => {
  const u = r.url();
  if (u.includes("/api/")) netLog.push({ url: u.replace(BASE, ""), status: r.status() });
});

const report = [];

for (const [key, label] of PAGES) {
  if (only && only !== key) continue;
  await page.goto(`${BASE}/admin`, { waitUntil: "networkidle2", timeout: 30000 });
  await page.evaluate((s) => {
    const d = document.documentElement;
    if (s === "dark") {
      d.setAttribute("data-dark", "");
      d.removeAttribute("data-light");
    } else {
      d.setAttribute("data-light", "");
      d.removeAttribute("data-dark");
    }
  }, scheme);
  await page.evaluate((lbl) => {
    const items = [...document.querySelectorAll('[role="button"],button')];
    const hit = items.find((el) => (el.textContent || "").replace(/\s+/g, "").startsWith(lbl.replace(/\s+/g, "")));
    if (hit) hit.click();
  }, label);
  await new Promise((r) => setTimeout(r, 800));

  const before = await page.evaluate(snapshot);

  report.push(`\n=== ${label} ===`);

  // 1) 按钮状态合理性：禁用的按钮不该是 primary 色的可点样式
  const enabledNoCursor = before.buttons.filter(
    (b) => !b.disabled && b.cursor === "default" && b.hasSpinner === false && b.text,
  );
  if (enabledNoCursor.length) {
    report.push(`  [info] ${enabledNoCursor.length} 个可用按钮 cursor=default（点了没反馈？）: ${enabledNoCursor.slice(0, 3).map((b) => b.text).join(" / ")}`);
  }

  // 2) 空状态：面板有内容吗？
  const emptiness = await page.evaluate(() => {
    const panels = [...document.querySelectorAll("main section, main > div > div")].slice(0, 6);
    return panels.map((p) => ({
      h: Math.round(p.getBoundingClientRect().height),
      text: (p.textContent || "").replace(/\s+/g, " ").trim().length,
    }));
  });
  const tiny = emptiness.filter((p) => p.h > 20 && p.text < 25);
  if (tiny.length) {
    report.push(`  [warn] ${tiny.length} 个面板几乎是空的（文字 ${tiny[0].text} 字符，高 ${tiny[0].h}px）—— 缺空状态引导？`);
  }

  // 3) 所有页面都该有可用的主操作按钮
  const primary = before.buttons.filter((b) => b.visible && b.text);
  report.push(`  可点按钮 ${primary.length} 个，输入框 ${before.inputs.length} 个，遮罩 ${before.overlays}`);
  const busyByDefault = primary.filter((b) => b.disabled);
  if (busyByDefault.length === primary.length && primary.length > 0) {
    report.push(`  [warn] 全部按钮都是禁用态 —— 首屏无法操作？`);
  }

  // 4) 必填标记：表单里有没有 required 提示
  const noPlaceholder = before.inputs.filter((i) => !i.placeholder && !i.value);
  if (noPlaceholder.length) {
    report.push(`  [info] ${noPlaceholder.length} 个输入框既无 placeholder 也无值: ${noPlaceholder.slice(0, 3).map((i) => i.name).join(" / ")}`);
  }
}

report.push(`\n=== 网络请求 ===`);
const fails = netLog.filter((r) => r.status >= 400);
report.push(`  共 ${netLog.length} 个 API 请求，失败 ${fails.length} 个`);
for (const f of [...new Set(fails.map((f) => `${f.status} ${f.url}`))].slice(0, 6)) {
  report.push(`    - ${f}`);
}

await browser.close();
console.log(report.join("\n"));