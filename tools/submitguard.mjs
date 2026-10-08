// 提交守卫一致性检查。
//
// 背景：2026-10-09 实测出一个真实缺陷 —— 赠送页连点5 次发出 5 次 POST。
// 根因是 setBusy(true) 是异步状态更新，React 要等事件处理完才重渲染，
// 连续 click 都读到上一次的 busy=false，于是全部进入提交函数。
//
// 修法是加一个同步 ref 守卫。但这套模式必须全站统一，否则只修一页，
// 其它页仍能重复提交（保存凭据、改价格、存出站…）。
//
// 这个脚本检查每一处提交入口是否都有同步守卫，并把只读操作单独归类 ——
// 查询类连点没有副作用，而且那两处还用了 sequence ref 做竞态防护
//（后发请求覆盖先发结果），比单纯禁止重复提交更完备。算成"缺守卫"
// 是误报。
//
// 用法：node tools/submitguard.mjs
import { readFile, readdir } from "node:fs/promises";
import { join } from "node:path";

const SRC = "frontend/src";

const isSubmission = (line) =>
  /setBusy\(true\)/.test(line) || /setSaving\(true\)/.test(line) || /setWorking\(true\)/.test(line);

const FN_HINT = /(?:const|function)\s+([A-Za-z_][\w]*)\s*(?:=\s*async|=\s*\(|async\s*\()/;

const files = (await readdir(SRC)).filter((f) => f.endsWith(".tsx"));
const findings = [];
const linesCache = {};

for (const file of files) {
  const text = await readFile(join(SRC, file), "utf8");
  const lines = text.split("\n");
  linesCache[file] = lines;

  lines.forEach((line, i) => {
    if (!isSubmission(line)) return;
    let fname = "";
    for (let k = i; k >= Math.max(0, i - 60); k--) {
      const m = lines[k].match(FN_HINT);
      if (m) {
        fname = m[1];
        break;
      }
    }
    const win = lines.slice(Math.max(0, i - 12), Math.min(lines.length, i + 45)).join("\n");
    const guarded =
      /\.current\s*&&\s*return/.test(win) || // if (submitting.current) return
      /\.current\s*=\s*true/.test(win) || // submitting.current = true
      /if\s*\(\s*busy\s*\)\s*return/.test(win) || // 同步的局部 busy
      /if\s*\(\s*inFlight\s*\)/.test(win);
    findings.push({ file, line: i + 1, fname, guarded, snippet: line.trim(), win });
  });
}

// 只读判据：函数体里有竞态防护标记，或端点属于查询类且不含写操作。
const READ_ONLY_ENDPOINT = /(lookup|query|search|refresh|reload|load|preview|stats|recovery\/)/i;
const WRITE_ENDPOINT = /\/save|\/settings|\/cards|\/recovery|\/folders|\/codes|\/outbounds/g;

function isReadOnly(f) {
  if (/sequence\.current|inFlight\.current|latest\.current/.test(f.win)) return true;
  if (WRITE_ENDPOINT.test(f.win)) return false;
  return READ_ONLY_ENDPOINT.test(f.win);
}

const unguarded = findings.filter((f) => !f.guarded && !isReadOnly(f));
const readOnlyOk = findings.filter((f) => !f.guarded && isReadOnly(f));
const guarded = findings.filter((f) => f.guarded);

console.log(`扫描 ${files.length} 个组件文件，找到 ${findings.length} 处提交入口`);
console.log(`有同步守卫: ${guarded.length}`);
console.log(`只读操作（无需守卫）: ${readOnlyOk.length}`);
console.log(`缺守卫（连点会重复提交）: ${unguarded.length}`);

if (readOnlyOk.length) {
  console.log("\n按只读处理的位置（连点无副作用，且有竞态防护）:");
  for (const f of readOnlyOk) console.log(`  ${f.file} 第 ${f.line} 行`);
}

if (unguarded.length) {
  console.log("\n缺同步守卫的位置（连点会重复提交）:");
  const byFile = {};
  for (const f of unguarded) (byFile[f.file] ||= []).push(f);
  for (const [file, list] of Object.entries(byFile)) {
    console.log(`\n  ${file}`);
    for (const f of list) {
      console.log(`    第 ${f.line} 行  函数 ${f.fname || "(匿名)"}`);
      console.log(`      ${f.snippet}`);
    }
  }
  console.log("\n修法：在组件里加同步 ref，进入提交前守卫。");
  console.log("例：");
  console.log("  const submitting = useRef(false);");
  console.log("  if (submitting.current) return;");
  console.log("  submitting.current = true;");
  console.log("  try { ... } finally { submitting.current = false; }");
  process.exitCode = 1;
} else {
  console.log("\n全部有副作用的提交入口都有同步守卫。");
}