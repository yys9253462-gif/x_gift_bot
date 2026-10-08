// 本地预览截图工具：puppeteer-core 驱动系统 Chrome，逐页逐主题截图。
// 只读，不改任何业务代码；产物写到 tools/shots/。
import puppeteer from "puppeteer-core";
import { mkdir } from "node:fs/promises";

const CHROME =
  "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";
const BASE = process.env.PREVIEW_BASE || "http://127.0.0.1:4173";
const OUT = "tools/shots";

// 后台 9 个页面，按侧栏顺序。
// label 用侧栏上的实际文案（注意「X 登录凭据」中间有空格）。
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

// 预览环境现已覆盖后台全部接口（2026-10-08 补齐了 /api/admin/gift/* 与
// /api/admin/settings*）。所以不再豁免任何 404：截图阶段出现任何资源
// 加载失败都当作真问题报出来，而不是"已知缺口"。
const EXPECTED_404 = [];

const arg = (name, fallback) => {
  const hit = process.argv.find((a) => a.startsWith(`--${name}=`));
  return hit ? hit.split("=")[1] : fallback;
};

const only = arg("only", "");
const tag = arg("tag", "");
const width = Number(arg("width", 1440));
const height = Number(arg("height", 1000));

await mkdir(OUT, { recursive: true });

const browser = await puppeteer.launch({
  executablePath: CHROME,
  headless: "shell",
  args: [
    "--no-sandbox",
    "--disable-dev-shm-usage",
    "--force-color-profile=srgb",
    "--hide-scrollbars",
  ],
});

const problems = [];

for (const scheme of ["light", "dark"]) {
  const page = await browser.newPage();
  await page.setViewport({ width, height, deviceScaleFactor: 1 });
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    const text = m.text();
    // 预览环境未实现的接口会返回 404，属于已知缺口，不计入本次改版的问题。
    if (/Failed to load resource/.test(text) && EXPECTED_404.length) return;
    problems.push(`[${scheme}] console.error: ${text}`);
  });
  page.on("pageerror", (e) => problems.push(`[${scheme}] pageerror: ${e.message}`));

  // 锁定主题：ThemeProvider 读 localStorage 的 xgift-mode。
  await page.evaluateOnNewDocument((s) => {
    try {
      localStorage.setItem("xgift-mode", s);
    } catch {}
  }, scheme);

  for (const [key, label] of PAGES) {
    if (only && only !== key) continue;
    await page.goto(`${BASE}/admin`, { waitUntil: "networkidle2", timeout: 30000 });
    await page.evaluate(() => {
      document.documentElement.setAttribute("data-light", "");
      document.documentElement.removeAttribute("data-dark");
    });
    if (scheme === "dark") {
      await page.evaluate(() => {
        document.documentElement.setAttribute("data-dark", "");
        document.documentElement.removeAttribute("data-light");
      });
    }
    // 侧栏切换到目标页。按 data 无关的文本精确匹配，避免 "查询" 命中 "查询出口"。
    const clicked = await page.evaluate((label) => {
      const items = [...document.querySelectorAll('[role="button"],button')];
      const hit = items.find((el) => {
        const t = (el.textContent ?? "").replace(/\s+/g, "");
        return t === label.replace(/\s+/g, "") || t.startsWith(label.replace(/\s+/g, ""));
      });
      if (hit) { hit.click(); return true; }
      return false;
    }, label);
    if (!clicked) problems.push(`[${scheme}] ${key}: 侧栏未找到「${label}」`);
    await new Promise((r) => setTimeout(r, 900));

    const file = `${OUT}/${tag ? tag + "-" : ""}${scheme}-${key}.png`;
    await page.screenshot({ path: file, fullPage: true });
    console.log("saved", file);

    //顺带量一下实际渲染出的关键颜色，供文字核对
    if (key === "codes" && !tag) {
      const probe = await page.evaluate(() => {
        const cs = getComputedStyle(document.body);
        return { bg: cs.backgroundColor, color: cs.color, scheme: document.documentElement.dataset.dark ? "dark" : "light" };
      });
      console.log("body probe", JSON.stringify(probe));
    }
  }
  await page.close();
}

await browser.close();
if (problems.length) {
  console.log("\n--- 页面报错 ---");
  for (const p of problems) console.log(p);
} else {
  console.log("\n无 console/page 错误。");
}