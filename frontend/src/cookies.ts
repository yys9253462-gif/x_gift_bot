// Cookie 粘贴解析：把操作者手上各种形态的凭据文本，归一成 auth_token / ct0。
//
// 真实场景里拿到的东西五花八门，所以这里尽量宽容：
//   1. auth_token=abc; ct0=def                      请求头原文（最常见）
//   2. {"auth_token":"abc","ct0":"def"}             JSON 对象
//   3. [{"name":"auth_token","value":"abc"}, ...]   浏览器扩展导出的数组
//   4. 混杂 _ga、guest_id 等无关项的整段文本        只挑需要的两项
//   5. 换行分隔、带引号、带 "Cookie: " 前缀
//
// 引导页（手写 setup.js）与后台设置面板共用同一套规则，
// 两边行为必须一致，否则同一个粘贴内容在两处结果不同。
export type ParsedCookies = {
  auth_token: string;
  ct0: string;
  authorization: string;
  user_agent: string;
};

const EMPTY: ParsedCookies = { auth_token: "", ct0: "", authorization: "", user_agent: "" };

const WANTED = ["auth_token", "ct0", "authorization", "user_agent"] as const;

export function parseCookies(text: string): ParsedCookies {
  const out: ParsedCookies = { ...EMPTY };
  if (!text) return out;
  const raw = text.trim();
  if (!raw) return out;

  if (raw.startsWith("{") || raw.startsWith("[")) {
    try {
      const doc = JSON.parse(raw) as unknown;
      const list = Array.isArray(doc)
        ? doc
        : Array.isArray((doc as { cookies?: unknown }).cookies)
          ? ((doc as { cookies: unknown[] }).cookies as unknown[])
          : [doc];
      for (const item of list) {
        if (!item || typeof item !== "object") continue;
        const rec = item as Record<string, unknown>;
        // 扩展导出常用 name/value
        if (typeof rec.name === "string" && rec.value !== undefined) {
          const k = rec.name.toLowerCase();
          if (k === "auth_token" || k === "ct0") out[k] = String(rec.value);
        }
        // 有的格式直接是键值对
        for (const key of WANTED) {
          if (rec[key] !== undefined) out[key] = String(rec[key]);
        }
      }
      // 兼容 {cookies:{auth_token:..}} 这种嵌套对象
      const nested = (doc as { cookies?: unknown }).cookies;
      if (nested && !Array.isArray(nested) && typeof nested === "object") {
        const rec = nested as Record<string, unknown>;
        for (const key of ["auth_token", "ct0"] as const) {
          if (rec[key] !== undefined) out[key] = String(rec[key]);
        }
      }
      return out;
    } catch {
      // 不是合法 JSON，落回文本解析
    }
  }

  for (const pair of raw.split(/[;\n]+/)) {
    const i = pair.indexOf("=");
    if (i < 0) continue;
    const k = pair
      .slice(0, i)
      .trim()
      .toLowerCase()
      .replace(/^cookie:\s*/, "");
    const v = pair
      .slice(i + 1)
      .trim()
      .replace(/^"|"$/g, "");
    if (k === "auth_token" || k === "ct0") out[k] = v;
  }
  return out;
}

/** 从解析结果里挑出真正识别到的字段，用于给用户反馈。 */
export function filledFields(p: ParsedCookies): string[] {
  return WANTED.filter((k) => p[k].length > 0);
}
