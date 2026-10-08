// 前端全面排查：9 页 × 明暗，逐项检查交互与可访问性。
//
// 为什么不靠看截图：截图只能看出"长什么样"，看不出"能不能用"。
// 这里检查的是机械可判定的项 —— 对比度、焦点顺序、可点面积、
// 标签关联、触摸目标、横向溢出、动画偏好 —— 每项都能给出数字或
// 具体元素，报告可以直接定位到要改哪一行。
//
// 用法：node tools/a11y.mjs [--only=<页面>] [--scheme=light|dark|both]
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

const only = arg("only", "");
const schemeArg = arg("scheme", "both");
const schemes = schemeArg === "both" ? ["light", "dark"] : [schemeArg];
const VIEWPORTS = [
  { name: "桌面", width: 1440, height: 1000 },
  { name: "窄屏", width: 768, height: 1000 },
];

// 注意：audit 会被序列化后送进浏览器上下文执行，所以它不能引用
// 模块作用域里的任何辅助函数 —— 那些在页面里不存在。第一版就这么
// 踩了（parseRGB is not defined），颜色相关辅助必须定义在 audit 内部。
const audit = () => {
  const srgb = (c) => {
    const v = c / 255;
    return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  };
  const parseRGB = (s) => {
    const m = s.match(/rgba?\(([^)]+)\)/);
    if (!m) return null;
    const [r, g, b, a = "1"] = m[1].split(",").map((x) => parseFloat(x.trim()));
    return { r, g, b, a };
  };
  const luminance = ({ r, g, b }) => 0.2126 * srgb(r) + 0.7152 * srgb(g) + 0.0722 * srgb(b);
  const ratio = (fg, bg) => {
    if (!fg || !bg) return null;
    const a = luminance(fg);
    const b = luminance(bg);
    const [hi, lo] = a > b ? [a, b] : [b, a];
    return (hi + 0.05) / (lo + 0.05);
  };
  // 逐层往上找不透明的背景色，模拟实际合成结果。
  const effectiveBg = (el) => {
    let node = el;
    while (node && node !== document.documentElement) {
      const c = parseRGB(getComputedStyle(node).backgroundColor);
      if (c && c.a > 0.85) return c;
      node = node.parentElement;
    }
    return parseRGB(getComputedStyle(document.body).backgroundColor) || { r: 255, g: 255, b: 255, a: 1 };
  };

  const out = { issues: [], stats: {} };
  const push = (level, kind, msg, el) =>
    out.issues.push({
      level,
      kind,
      msg,
      where: el
        ? `${el.tagName.toLowerCase()}${el.className ? "." + String(el.className).split(" ")[0] : ""}`
        : "",
      text: el ? (el.textContent || "").replace(/\s+/g, " ").trim().slice(0, 40) : "",
    });

  // 1. 正文对比度：只查实际有文字的元素，跳过纯装饰。
  let lowContrast = 0;
  for (const el of document.querySelectorAll("p,span,td,th,label,button,a,h1,h2,h3,li,div")) {
    // 必须有直接文本节点
    const own = [...el.childNodes]
      .filter((n) => n.nodeType === 3)
      .map((n) => n.textContent.trim())
      .join("");
    if (!own) continue;
    const cs = getComputedStyle(el);
    if (cs.visibility === "hidden" || cs.display === "none" || parseFloat(cs.opacity) < 0.3) continue;
    const r = el.getBoundingClientRect();
    if (r.width < 2 || r.height < 2) continue;
    const fg = parseRGB(cs.color);
    const bg = effectiveBg(el);
    const cr = ratio(fg, bg);
    const size = parseFloat(cs.fontSize);
    const bold = parseInt(cs.fontWeight, 10) >= 700;
    const need = size >= 18.66 || (bold && size >= 14) ? 3 : 4.5;
    if (cr && cr < need) {
      lowContrast++;
      if (lowContrast <= 3) push("warn", "对比度", `${cr.toFixed(2)}:1 < ${need}:1 "${own}"`, el);
    }
  }
  out.stats.低对比元素 = lowContrast;

  // 2. 交互元素的可点面积。
  //    阈值按 WCAG 2.5.8的 24×24。第一版用 32×32，于是把 FilterBar 里
  //    30px 高的筛选 token 报成问题—— 那个高度是刻意选的，注释里写明
  //    已按 24×24 扩过删除按钮命中区。阈值该按标准走，不是按我的偏好。
  let small = 0;
  for (const el of document.querySelectorAll("button,a[href],[role=button],input,select,textarea")) {
    const r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) continue;
    const cs = getComputedStyle(el);
    if (cs.display === "none" || cs.opacity === "0") continue;
    // 同上：MUI 藏起来的原生控件不是可点目标。
    if (r.width < 2 || r.height < 2) continue;
    if (r.width < 24 || r.height < 24) {
      small++;
      if (small <= 3)
        push("warn", "可点面积", `${Math.round(r.width)}×${Math.round(r.height)} 小于 24×24`, el);
    }
  }
  out.stats.过小交互元素 = small;

  // 3. 输入控件必须有可访问名称（label / aria-label / title）
  let unlabeled = 0;
  for (const el of document.querySelectorAll("input,select,textarea")) {
    if (el.type === "hidden") continue;
    const cs = getComputedStyle(el);
    if (cs.display === "none") continue;
    // MUI 会把真正的原生 input 藏起来，另画一个可视控件代替
    // （Select 的 nativeInput、Switch 的 input）。它们拿到焦点，
    // 也可能没有独立标签 —— 但 MuiSwitch 的标签在父级 SwitchBase 上。
    const r = el.getBoundingClientRect();
    const hiddenByMUI =
      r.width < 2 || r.height < 2 ||
      cs.opacity === "0" ||
      (el.closest(".MuiSwitch-root,.MuiSelect-root,.MuiCheckbox-root,.MuiRadio-root") != null);
    if (hiddenByMUI) continue;
    const has = el.getAttribute("aria-label") || el.getAttribute("aria-labelledby") || el.title;
    // MUI 的 outlined input 用 <label for> 关联
    const id = el.getAttribute("id");
    const labelFor = id && document.querySelector(`label[for="${CSS.escape(id)}"]`);
    if (!has && !labelFor) {
      unlabeled++;
      if (unlabeled <= 3) push("warn", "标签", "输入控件没有可访问名称", el);
    }
  }
  out.stats.未命名输入 = unlabeled;

  // 4. 图标按钮必须有 aria-label（只有图标时读屏读不出）
  let iconNoName = 0;
  for (const el of document.querySelectorAll("button,[role=button]")) {
    // 用整棵子树的可见文本，不只是直接文本节点：MUI 的 ListItemButton
    // 把文字放在内层 Typography 里，第一版只看直接子文本，
    // 于是把 9 个侧栏导航全报成"无名图标按钮"。
    const anyText = (el.textContent || "").replace(/\s+/g, "").trim();
    if (anyText) continue;
    if (el.querySelector("svg") && !el.getAttribute("aria-label") && !el.title) {
      iconNoName++;
      if (iconNoName <= 3) push("warn", "图标按钮", "只有图标但没有 aria-label", el);
    }
  }
  out.stats.无名图标按钮 = iconNoName;

  // 5. 横向溢出（窄屏下最常见的布局问题）
  const de = document.documentElement;
  if (de.scrollWidth > de.clientWidth + 2) {
    push("error", "横向溢出", `scrollWidth ${de.scrollWidth} > clientWidth ${de.clientWidth}`, null);
  }
  // 找出真正超出右边界的元素。
  // 注意：祖先里有 overflow-x:auto/scroll/hidden 的不算问题——
  // 宽表格放在 TableContainer 里横向滚动是正确设计，窄屏下就该能滑。
  // 第一版没做这个区分，把TableContainer 里的 24 个单元格全报成溢出。
  const hasScrollableAncestor = (el) => {
    let a = el.parentElement;
    while (a && a !== document.body) {
      const ox = getComputedStyle(a).overflowX;
      if (ox === "auto" || ox === "scroll" || ox === "hidden") return true;
      a = a.parentElement;
    }
    return false;
  };
  const wide = [];
  for (const el of document.querySelectorAll("body *")) {
    const r = el.getBoundingClientRect();
    if (hasScrollableAncestor(el)) continue;
    if (r.width > 0 && r.right > de.clientWidth + 2 && getComputedStyle(el).position !== "fixed") {
      wide.push(
      `${el.tagName.toLowerCase()}` +
        `.${String(el.className || "").split(" ").filter(Boolean).slice(0, 2).join(".")}` +
        ` right=${Math.round(r.right)} w=${Math.round(r.width)}` +
        ` 祖先可横向滚动=${(() => {
          let a = el.parentElement, ok = false;
          while (a && a !== document.body) {
            const s = getComputedStyle(a);
            if (s.overflowX === "auto" || s.overflowX === "scroll" || s.overflowX === "hidden") { ok = true; break; }
            a = a.parentElement;
          }
          return ok ? "是" : "否";
        })()}`,
    );
    }
  }
  if (wide.length) {
    push("warn", "溢出元素", `${wide.length} 个超出视口，如 ${wide.slice(0, 3).join("; ")}`, null);
  }

  // 6. 焦点可见性：按钮获得焦点时应有轮廓或阴影变化
  const btn = document.querySelector("button");
  if (btn) {
    const before = getComputedStyle(btn);
    const b4 = before.boxShadow + "|" + before.outlineWidth;
    btn.focus();
    const after = getComputedStyle(btn);
    const af = after.boxShadow + "|" + after.outlineWidth;
    if (b4 === af) {
      push("warn", "焦点样式", "按钮聚焦后视觉无变化，键盘用户看不到焦点位置", btn);
    }
    btn.blur();
  }

  // 7. 语言与文档标题
  if (!document.documentElement.lang) push("warn", "i18n", "<html> 没有 lang 属性", null);
  if (!document.title) push("warn", "标题", "document.title 为空", null);

  // 8. prefers-reduced-motion 下是否仍在跑动画
  const rm = window.matchMedia("(prefers-reduced-motion: reduce)");
  if (rm.matches) {
    let animating = 0;
    for (const el of document.querySelectorAll("*")) {
      const cs = getComputedStyle(el);
      const d = cs.animationDuration;
      if (d && d !== "0s" && parseFloat(d) > 0.05) animating++;
    }
    if (animating > 0)
      push("warn", "动画", `用户偏好减少动画，但仍有 ${animating} 个元素带动画`, null);
  }

  // 9. h1 唯一性：多个 h1 或没有 h1 都不利于导航
  const h1s = document.querySelectorAll("h1").length;
  if (h1s !== 1) push("warn", "标题层级", `h1 数量 = ${h1s}，应为 1`, null);

  return out;
};

const browser = await puppeteer.launch({
  executablePath: CHROME,
  headless: "shell",
  args: ["--no-sandbox", "--disable-dev-shm-usage", "--hide-scrollbars", "--force-color-profile=srgb"],
});

let totalIssues = 0;
const summary = [];

for (const scheme of schemes) {
  for (const vp of VIEWPORTS) {
    const page = await browser.newPage();
    await page.setViewport({ width: vp.width, height: vp.height, deviceScaleFactor: 1 });
    await page.evaluateOnNewDocument((s) => {
      try {
        localStorage.setItem("xgift-mode", s);
      } catch {}
    }, scheme);

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
      const clicked = await page.evaluate((lbl) => {
        const items = [...document.querySelectorAll('[role="button"],button')];
        const hit = items.find((el) => (el.textContent || "").replace(/\s+/g, "").startsWith(lbl.replace(/\s+/g, "")));
        if (hit) {
          hit.click();
          return true;
        }
        return false;
      }, label);
      if (!clicked) {
        summary.push(`[${scheme}/${vp.name}] ${label}: 侧栏未找到`);
        continue;
      }
      await new Promise((r) => setTimeout(r, 700));
      const r = await page.evaluate(audit);
      const errs = r.issues.filter((i) => i.level === "error").length;
      const warns = r.issues.length - errs;
      totalIssues += r.issues.length;
      summary.push(
        `[${scheme}/${vp.name}] ${label}: ${errs} 错 ${warns} 警` +
          `  低对比=${r.stats.低对比元素} 过小=${r.stats.过小交互元素} ` +
          `未命名=${r.stats.未命名输入} 无名图标=${r.stats.无名图标按钮}`,
      );
      for (const i of r.issues) {
        summary.push(`    - [${i.level}] ${i.kind}: ${i.msg}${i.where ? ` (${i.where} "${i.text}")` : ""}`);
      }
    }
    await page.close();
  }
}
await browser.close();

console.log(summary.join("\n"));
console.log(`\n合计问题 ${totalIssues} 项`);