// 量出页面里到底谁把高度撑起来了。
//
// 背景：运维页整页高 2435px，目测底部有一大段空白。之前怀疑是侧栏的
// maxHeight:calc(100vh - 32px) 截断，去掉 maxHeight 后整页高度完全没变，
// 说明猜错了。这个脚本直接量：逐个 block 级元素报出 offsetHeight 与
// 累计偏移，把"空白属于谁"变成可读的数字，而不是靠看图猜。
import puppeteer from "puppeteer-core";

const CHROME = "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";
const BASE = process.env.PREVIEW_BASE || "http://127.0.0.1:4173";
const arg = (n, d) => {
  const hit = process.argv.find((a) => a.startsWith(`--${n}=`));
  return hit ? hit.split("=")[1] : d;
};
const target = arg("page", "运维");
const scheme = arg("scheme", "light");
const width = Number(arg("width", 1440));
const height = Number(arg("height", 1000));

const browser = await puppeteer.launch({
  executablePath: CHROME,
  headless: "shell",
  args: ["--no-sandbox", "--disable-dev-shm-usage", "--hide-scrollbars"],
});
const page = await browser.newPage();
await page.setViewport({ width, height, deviceScaleFactor: 1 });
await page.evaluateOnNewDocument((s) => {
  try {
    localStorage.setItem("xgift-mode", s);
  } catch {}
}, scheme);
await page.goto(`${BASE}/admin`, { waitUntil: "networkidle2", timeout: 30000 });
await page.evaluate((s) => {
  if (s === "dark") {
    document.documentElement.setAttribute("data-dark", "");
    document.documentElement.removeAttribute("data-light");
  } else {
    document.documentElement.setAttribute("data-light", "");
    document.documentElement.removeAttribute("data-dark");
  }
}, scheme);
await page.evaluate((label) => {
  const items = [...document.querySelectorAll('[role="button"],button')];
  const hit = items.find((el) => {
    const t = (el.textContent ?? "").replace(/\s+/g, "");
    return t.startsWith(label.replace(/\s+/g, ""));
  });
  if (hit) hit.click();
}, target);
await new Promise((r) => setTimeout(r, 900));

const report = await page.evaluate(() => {
  const doc = document.documentElement;
  const body = document.body;
  const describe = (el) => {
    const cs = getComputedStyle(el);
    return {
      tag: el.tagName.toLowerCase(),
      role: el.getAttribute("role") || "",
      cls: (el.className?.baseVal ?? el.className ?? "").toString().slice(0, 40),
      h: Math.round(el.getBoundingClientRect().height),
      minH: cs.minHeight,
      maxH: cs.maxHeight,
      align: cs.alignSelf,
      pos: cs.position,
      text: (el.textContent ?? "").replace(/\s+/g, " ").trim().slice(0, 28),
    };
  };
  // 只看能撑高度的容器：body 的直接子元素，以及所有 <main>/<section>。
  // 侧栏单独量：它的 position/alignSelf/高度决定了"底部空白"归谁。
  const side = document.querySelector('aside, [class*="MuiBox-root"]');
  const navs = [...document.querySelectorAll("nav")].concat(
    [...document.querySelectorAll("div")].filter((el) => {
      const cs = getComputedStyle(el);
      return cs.position === "sticky";
    }),
  );
  const sidebar = navs[0];
  const sideInfo = sidebar ? (() => { const cs=getComputedStyle(sidebar); const r=sidebar.getBoundingClientRect();
    return { tag: sidebar.tagName.toLowerCase(), pos: cs.position, alignSelf: cs.alignSelf,
      h: Math.round(r.height), top: Math.round(r.top), minH: cs.minHeight, maxH: cs.maxHeight,
      offsetH: sidebar.offsetHeight, flex: cs.flex }; })() : null;
  // sticky 元素在文档流里仍占原位，getBoundingClientRect 只给可见位置，
  // 所以额外量offsetTop/offsetHeight 这类布局值。
  const boxes = [...document.querySelectorAll("body *")]
    .map((el) => ({
      el,
      top: el.offsetTop,
      h: el.offsetHeight,
      pos: getComputedStyle(el).position,
      tag: el.tagName.toLowerCase(),
      text: (el.textContent ?? "").replace(/\s+/g, " ").trim().slice(0, 24),
    }))
    .filter((b) => b.h > 400)
    .sort((a, b) => b.top + b.h - (a.top + a.h));
  // visuallyHidden 的实际计算样式：确认 contain 是否真的生效。
  // 直接找那张撑高的 table：absolute 且高度 > 400。
  const vh = [...document.querySelectorAll("table")].find((el) => {
    const cs = getComputedStyle(el);
    return cs.position === "absolute" && el.offsetHeight > 400;
  });
  const vhStyle = vh ? (() => { const cs = getComputedStyle(vh); return {
    tag: vh.tagName.toLowerCase(), h: Math.round(vh.getBoundingClientRect().height),
    offsetH: vh.offsetHeight, contain: cs.contain, clipPath: cs.clipPath,
    clip: cs.clip, position: cs.position, overflow: cs.overflow,
  }; })() : null;
  const tall = boxes.slice(0, 10).map((b) => ({
    tag: b.tag, pos: b.pos, top: b.top, h: b.h, bottom: b.top + b.h, text: b.text,
  }));
  const nodes = [...body.children, ...document.querySelectorAll("main, main > *, section")];
  const cs = getComputedStyle(doc);
  const csb = getComputedStyle(body);
  return {
    htmlH: Math.round(doc.getBoundingClientRect().height),
    htmlMinH: cs.minHeight,
    htmlDisplay: cs.display,
    htmlBg: cs.backgroundColor,
    htmlAfterContent: (() => {
      const a = getComputedStyle(doc, "::after");
      return { content: a.content, display: a.display, height: a.height };
    })(),
    bodyAfterContent: (() => {
      const a = getComputedStyle(body, "::after");
      return { content: a.content, display: a.display, height: a.height, marginTop: a.marginTop };
    })(),
    bodyMarginBottom: csb.marginBottom,
    bodyBorderBottom: csb.borderBottomWidth,
    scrollH: doc.scrollHeight,
    bodyH: Math.round(body.getBoundingClientRect().height),
    bodyMinH: getComputedStyle(body).minHeight,
    bodyH_100: getComputedStyle(body).height,
    nodes: nodes.slice(0, 24).map(describe),
    tall, vhStyle, sideInfo,
    tallest: Math.max(...boxes.map((b) => b.top + b.h)),
  };
});

console.log(`页面=${target} 主题=${scheme}`);
console.log(`html高度=${report.htmlH} minHeight=${report.htmlMinH} display=${report.htmlDisplay}`);
console.log(`html::after=${JSON.stringify(report.htmlAfterContent)}`);
console.log(`body::after=${JSON.stringify(report.bodyAfterContent)}`);
console.log(`body marginBottom=${report.bodyMarginBottom} borderBottom=${report.bodyBorderBottom}`);
console.log(`scrollHeight=${report.scrollH}  body高度=${report.bodyH}  body.minHeight=${report.bodyMinH}  body.height=${report.bodyH_100}`);
console.log(`布局盒最高底部=${report.tallest}`);
console.log("隐藏表格样式=" + JSON.stringify(report.vhStyle));
console.log("侧栏=" + JSON.stringify(report.sideInfo));
console.log("--- 高元素（offsetTop+offsetHeight>400）---");
for (const t of report.tall) console.log(`${t.tag.padEnd(8)} pos=${t.pos.padEnd(8)} top=${String(t.top).padStart(5)} h=${String(t.h).padStart(5)} bottom=${String(t.bottom).padStart(5)} ${t.text}`);
console.log("--- 主要容器 ---");
for (const n of report.nodes) {
  console.log(
    `${n.tag.padEnd(8)} h=${String(n.h).padStart(5)} min=${n.minH.padEnd(10)} max=${n.maxH.padEnd(12)} align=${n.align.padEnd(8)} pos=${n.pos.padEnd(8)} ${n.text}`,
  );
}
await browser.close();