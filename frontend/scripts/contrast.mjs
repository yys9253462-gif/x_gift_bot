// WCAG 对比度验算：把 colors.json 的每个前景/背景配对跑一遍，输出真实比值。
// 用法：node frontend/scripts/contrast.mjs
import { readFile } from "node:fs/promises";

const colors = JSON.parse(await readFile("frontend/src/colors.json", "utf8"));

const srgb = (c) => {
  const v = c / 255;
  return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
};
const lum = (hex) => {
  const h = hex.replace("#", "");
  const r = parseInt(h.slice(0, 2), 16);
  const g = parseInt(h.slice(2, 4), 16);
  const b = parseInt(h.slice(4, 6), 16);
  return 0.2126 * srgb(r) + 0.7152 * srgb(g) + 0.0722 * srgb(b);
};
const ratio = (a, b) => {
  const [x, y] = [lum(a), lum(b)].sort((m, n) => n - m);
  return (x + 0.05) / (y + 0.05);
};

// [前景路径, 背景路径, 最低比值, 说明]
// 正文 4.5，大字/图标/边框 3.0。
// 每个"文字 × 底色"组合都要跑：控件可能落在页面底、面板白、面板头、
// 甚至内嵌容器上，只验纸白那一处会漏掉真实场景里的失败。
const LAYERS = [
  "background.paper",
  "background.default",
  "surfaceContainerLow",
  "surfaceContainer",
  "surfaceContainerHigh",
  "surfaceContainerHighest",
];

const PAIRS = [
  ["text.primary", "background.paper", 4.5, "正文 / 面板"],
  ["text.primary", "background.default", 4.5, "正文 / 页面底"],
  ["text.primary", "surfaceContainerLow", 4.5, "正文 / 面板头"],
  ["text.secondary", "background.paper", 4.5, "次要文字 / 面板"],
  ["text.secondary", "background.default", 4.5, "次要文字 / 页面底"],
  ["text.secondary", "surfaceContainerLow", 4.5, "次要文字 / 面板头"],
  ["text.secondary", "surfaceContainer", 4.5, "次要文字 / 内嵌容器"],
  ["text.secondary", "surfaceContainerHigh", 4.5, "次要文字 / 强内嵌"],
  ["text.secondary", "surfaceContainerHighest", 4.5, "次要文字 / 最强内嵌"],
  ["text.secondary", "primary.light", 4.5, "次要文字 / 选中导航底"],
  ["primary.main", "background.paper", 4.5, "主色文字 / 面板"],
  ["primary.main", "primary.light", 4.5, "选中导航项文字"],
  ["primary.main", "surfaceContainerLow", 4.5, "主色 / 面板头（图标位）"],
  ["primary.main", "background.default", 4.5, "主色文字 / 页面底"],
  ["primary.contrastText", "primary.main", 4.5, "主按钮文字"],
  ["success.main", "background.paper", 4.5, "成功色文字 / 面板"],
  ["warning.main", "background.paper", 4.5, "警告色文字 / 面板"],
  ["error.main", "background.paper", 4.5, "错误色文字 / 面板"],
  ["info.main", "background.paper", 4.5, "信息色文字 / 面板"],
  ["success.dark", "success.light", 4.5, "成功提示条文字"],
  ["warning.dark", "warning.light", 4.5, "警告提示条文字"],
  ["error.dark", "error.light", 4.5, "错误提示条文字"],
  ["info.dark", "info.light", 4.5, "信息提示条文字"],
  ["secondary.main", "background.paper", 4.5, "次色文字 / 面板"],
  ["divider", "background.paper", 1.0, "分隔线（仅装饰，不设下限）"],
  ["outlineVariant", "background.paper", 1.0, "弱分隔线（仅装饰）"],
  // 控件边框必须在它可能出现的每一种底色上都 ≥3:1（WCAG 1.4.11）。
  ["outline", "background.paper", 3.0, "输入框边框 / 面板"],
  ["outline", "background.default", 3.0, "输入框边框 / 页面底"],
  ["outline", "surfaceContainerLow", 3.0, "输入框边框 / 面板头"],
];

const get = (obj, path) => path.split(".").reduce((o, k) => o?.[k], obj);

let failures = 0;

// 逐一验算"每个文字色落在每一层底色上"，避免只验纸白而漏掉真实场景。
for (const scheme of ["light", "dark"]) {
  console.log(`\n===== ${scheme} =====`);
  for (const [fg, bg, min, note] of PAIRS) {
    const a = get(colors[scheme], fg);
    const b = get(colors[scheme], bg);
    if (!a || !b) {
      console.log(`  ?? 缺失 ${fg} 或 ${bg}`);
      failures++;
      continue;
    }
    const r = ratio(a, b);
    const ok = r >= min;
    if (!ok) failures++;
    console.log(
      `  ${ok ? "OK  " : "FAIL"} ${r.toFixed(2).padStart(5)}:1  (需≥${min})  ${fg} / ${bg}  ${a} ${b}  ${note}`,
    );
  }
  // 正文在最深/最浅的内嵌层上也不能塌。
  for (const layer of LAYERS) {
    for (const fg of ["text.primary", "text.secondary"]) {
      const a = get(colors[scheme], fg);
      const b = get(colors[scheme], layer);
      if (!a || !b) continue;
      const r = ratio(a, b);
      const ok = r >= 4.5;
      if (!ok) failures++;
      console.log(
        `  ${ok ? "OK  " : "FAIL"} ${r.toFixed(2).padStart(5)}:1  (需≥4.5)  ${fg} / ${layer}  ${a} ${b}  全层扫描`,
      );
    }
  }
}
console.log(`\n${failures === 0 ? "全部达标。" : failures + " 项未达标。"}`);
process.exit(failures === 0 ? 0 : 1);