// 构建产物校验：确认新增的赠送功能真的进了 admin.js，并顺手挡一道凭据。
//
// 匹配方式：esbuild 把非ASCII 字符输出成大写十六进制的 \uXXXX 转义
// （例如"立"→ \u7ACB），所以要按大写形式匹配。早先一版用小写、
// 又一版直接匹配原文，两次都是全MISSING —— 一次是大小写不对，
// 一次是根本没转义，两种失败长得一样，所以顺手加一条自检。
import fs from "node:fs";

const path = process.argv[2] || "internal/site/assets/admin.js";
const src = fs.readFileSync(path, "utf8");
const BACKSLASH = String.fromCharCode(92);

/** 按 esbuild 的实际输出形式（大写 \uXXXX）把中文串转成可匹配的字面量。 */
function escapeNonAscii(text) {
  return Array.from(text)
    .map((ch) => {
      const cp = ch.codePointAt(0);
      if (cp <= 127) return ch;
      return BACKSLASH + "u" + cp.toString(16).toUpperCase().padStart(4, "0");
    })
    .join("");
}

// 自检：转义形式必须能在产物里命中，否则后面的检查全是假阴性。
const probe = escapeNonAscii("立即赠送");
if (!src.includes(probe) && src.includes("立即赠送")) {
  console.error("自检失败：产物保留中文原文，未做转义。");
  process.exit(2);
}
if (!src.includes(probe)) {
  console.error(`自检失败：产物中找不到 ${probe}，请确认 GiftNowPanel 已被构建。`);
  process.exit(2);
}

// 新功能必须在产物里出现的字面量。
const checks = [
  "立即赠送",
  "发送账号被 X 限制",
  "该怎么改",
  "流程轨迹",
  "会真实扣款",
  "出错阶段",
  "接收账号",
  "套餐时长",
  "/api/admin/gift",
  "结论来源",
  "向 X 的下单接口核实",
  "不是只看premium_gifting_eligible",
];

const bad = [];
for (const c of checks) {
  const lit = escapeNonAscii(c);
  const n = src.split(lit).length - 1;
  if (n === 0) bad.push(c);
  console.log(c.padEnd(22), n > 0 ? `YES (${n})` : "MISSING");
}

// 产物里不该出现真实凭据。stripe 公钥常量来自 X 的 main.js 而非本仓库，
// 因此这里查代理口令与 cookie 形态。
for (const sec of ["socks5://", "billing_number"]) {
  const n = src.split(sec).length - 1;
  if (n > 0) bad.push(sec);
  console.log(`无 ${sec}`.padEnd(22), n === 0 ? "OK" : `发现 ${n} 处`);
}

console.log(bad.length === 0 ? "\n校验通过" : `\n校验失败 ${bad.length} 项：${bad.join("、")}`);
process.exit(bad.length === 0 ? 0 : 1);