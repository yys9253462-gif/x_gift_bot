import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  CircularProgress,
  Divider,
  FormControlLabel,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import AddRounded from "@mui/icons-material/AddRounded";
import CloudOutlined from "@mui/icons-material/CloudOutlined";
import KeyOutlined from "@mui/icons-material/KeyOutlined";
import CreditCardOutlined from "@mui/icons-material/CreditCardOutlined";
import PaymentsOutlined from "@mui/icons-material/PaymentsOutlined";
import SettingsEthernetOutlined from "@mui/icons-material/SettingsEthernetOutlined";
import NetworkCheckRounded from "@mui/icons-material/NetworkCheckRounded";
import { adminApi } from "./adminApi";
import { Panel } from "./Panel";
import { parseCookies } from "./cookies";

// 每个设置分区一张独立卡片：原来四块挤在一个大卡片里，
// 只有一行小字标题分隔，长页面滚下来容易串行、也容易漏点保存。
// 现在统一走 Panel，语义与标题层级与后台其他页面一致。
function Section({ id, icon, title, hint, children }: { id: string; icon: ReactNode; title: string; hint: string; children: ReactNode }) {
  return (
    <Panel id={id} icon={icon} title={title} hint={hint}>
      {children}
    </Panel>
  );
}

// 设置分区。原来五块挤在一个长页面里（2500px+），改由左侧导航各占一项，
// 这里按传入的 section 只渲染对应那块。
export type SettingsSection = "credentials" | "cards" | "catalog" | "outbounds" | "proxy";

// 固定列数的栅格；窄屏自动退成一列。
function Grid({ cols, children }: { cols: 2 | 3; children: ReactNode }) {
  return (
    <Box
      sx={{
        display: "grid",
        gridTemplateColumns: { xs: "1fr", sm: `repeat(${cols}, minmax(0, 1fr))` },
        gap: 2,
      }}
    >
      {children}
    </Box>
  );
}

// 与后端 settingsView 一一对应；这里只保存脱敏摘要，不保存任何完整凭据。
type CardStatus = {
  last4: string;
  usable: boolean;
  problem?: string;
  blocked?: string;
  cooling_seconds?: number;
};
type Plan = { months: number; amount: number; product: string };
type SettingsView = {
  payments: boolean;
  credentials: {
    auth_token_tail: string;
    has_ct0: boolean;
    authorization_set: boolean;
    user_agent: string;
  };
  cards: CardStatus[];
  stripe: { present: boolean; tail: string };
  catalog: { merchant: string; currency: string; plans: Plan[] };
  proxy: { direct: boolean; tags: string[] };
};

type CardDraft = {
  number: string;
  exp_month: string;
  exp_year: string;
  cvc: string;
  billing_name: string;
  email: string;
  billing_country: string;
  billing_postal_code: string;
};

// 付款出站：X 按出口所在国报价，所以这里决定拿不拿得到低价区。
type OutboundNode = {
  type: string;
  tag: string;
  server: string;
  server_port: string;
  username?: string;
  password?: string;
  method?: string;
  uuid?: string;
  security?: string;
  sni?: string;
  flow?: string;
  network?: string;
  ws_path?: string;
  tls?: boolean;
};

// 已保存的出站节点。id 是内容指纹，不是 tag —— tag 可以重复，
// 用 tag 匹配就可能测错/删错节点。
type SavedNode = {
  id: string;
  tag: string;
  type: string;
  server?: string;
  server_port?: number;
  country?: string;
  pinned: boolean;
};

// 出站测试结果。
type ProbeResult = {
  ok: boolean;
  node: string;
  type: string;
  server?: string;
  ip?: string;
  reach_x: boolean;
  x_status?: number;
  elapsed_ms: number;
  stage?: string;
  message: string;
};

type OutboundStatus = {
  configured: boolean;
  nodes: number;
  // 后端在无节点时可能给出 null（Go 的 nil slice），所以按可选处理。
  tags?: string[] | null;
  mode: string;
  available: number;
  cooling: number;
  proxy_mode: string;
};

// 支持的出站类型。表单字段随类型变化，避免让操作者面对一堆无关输入框。
const OUTBOUND_TYPES: { value: string; label: string; hint: string }[] = [
  { value: "http", label: "HTTP / HTTPS 代理", hint: "服务端、端口，可选账号密码" },
  { value: "socks", label: "SOCKS5 代理", hint: "服务端、端口，可选账号密码" },
  { value: "shadowsocks", label: "Shadowsocks", hint: "加密方式与密码" },
  { value: "vmess", label: "VMess", hint: "UUID" },
  { value: "vless", label: "VLESS", hint: "UUID、TLS/Reality、可选 WS" },
  { value: "trojan", label: "Trojan", hint: "密码与 SNI" },
  { value: "anytls", label: "AnyTLS", hint: "密码与 SNI" },
  { value: "hysteria2", label: "Hysteria2", hint: "密码与 SNI" },
  { value: "direct", label: "直连", hint: "不使用代理（按服务器所在国报价）" },
];

const emptyNode = (): OutboundNode => ({
  type: "socks",
  tag: "",
  server: "",
  server_port: "",
});

const emptyCard: CardDraft = {
  number: "",
  exp_month: "",
  exp_year: "",
  cvc: "",
  billing_name: "",
  email: "",
  billing_country: "",
  billing_postal_code: "",
};

export function SettingsPanel({ section }: { section: SettingsSection }) {
  const [view, setView] = useState<SettingsView | null>(null);
  const [loadError, setLoadError] = useState("");
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  // 提交守卫：必须同步生效。setBusy(true) 要等 React 重渲染才可见，
  // 而连续点击都在同一轮事件里派发，每次都读到旧的 busy=false。
  // 这里保存的是配置（改价格、换凭据、存出站），重复提交虽不直接扣款，
  // 但会写入 vault 并触发多余的服务重启。
  const submitting = useRef(false);

  const [authToken, setAuthToken] = useState("");
  const [ct0, setCT0] = useState("");
  const [authorization, setAuthorization] = useState("");
  const [userAgent, setUserAgent] = useState("");
  // 整段粘贴的原始文本与识别反馈
  const [cookiePaste, setCookiePaste] = useState("");
  const [pasteNote, setPasteNote] = useState("");
  const [pasteOk, setPasteOk] = useState(false);

  // 解析粘贴内容并回填。只覆盖识别到的字段，没识别到的保持用户已填的值不动。
  const applyCookiePaste = useCallback((text: string) => {
    if (!text.trim()) {
      setPasteNote("");
      setPasteOk(false);
      return;
    }
    const parsed = parseCookies(text);
    const filled: string[] = [];
    if (parsed.auth_token) {
      setAuthToken(parsed.auth_token);
      filled.push("auth_token");
    }
    if (parsed.ct0) {
      setCT0(parsed.ct0);
      filled.push("ct0");
    }
    if (parsed.authorization) {
      setAuthorization(parsed.authorization);
      filled.push("Authorization");
    }
    if (parsed.user_agent) {
      setUserAgent(parsed.user_agent);
      filled.push("User-Agent");
    }
    if (filled.length === 0) {
      setPasteNote("没能从这段内容里认出 auth_token / ct0，请检查粘贴的原文。");
      setPasteOk(false);
      return;
    }
    setPasteNote("已识别并填入：" + filled.join("、") + "。确认无误后点「保存凭据」。");
    setPasteOk(true);
  }, []);

  const [card, setCard] = useState<CardDraft>(emptyCard);

  const [stripeKey, setStripeKey] = useState("");
  const [merchant, setMerchant] = useState("");
  const [currency, setCurrency] = useState("");
  const [planMonths, setPlanMonths] = useState<string[]>(["", ""]);
  const [planAmount, setPlanAmount] = useState<string[]>(["", ""]);
  const [planProduct, setPlanProduct] = useState<string[]>(["", ""]);

  const [proxyMode, setProxyMode] = useState("direct");
  const [proxyJSON, setProxyJSON] = useState("");

  // 付款出站
  const [outbounds, setOutbounds] = useState<OutboundStatus | null>(null);
  const [nodes, setNodes] = useState<OutboundNode[]>([]);
  const [rawJSON, setRawJSON] = useState("");
  const [useRaw, setUseRaw] = useState(false);
  const [outboundError, setOutboundError] = useState("");
  // 已保存的节点清单与操作中状态
  const [savedNodes, setSavedNodes] = useState<SavedNode[]>([]);
  const [pinnedId, setPinnedId] = useState("");
  const [probing, setProbing] = useState("");
  const [probes, setProbes] = useState<Record<string, ProbeResult>>({});

  const loadOutbounds = useCallback(async () => {
    setOutboundError("");
    try {
      const data = await adminApi<OutboundStatus>("/api/admin/settings/outbounds");
      setOutbounds(data);
    } catch (e) {
      setOutboundError((e as Error).message);
    }
  }, []);

  // 节点清单与"保存出站池"是两条独立的路径：
  // 清单负责测试/指定/删除（都走各自的端点，立即生效），
  // 保存负责新增与改参数（整池提交）。
  // 混在一起会出现"我删了节点但刷新后还在"这种困惑。
  const loadNodeList = useCallback(async () => {
    setOutboundError("");
    try {
      const data = await adminApi<{ nodes: SavedNode[]; pinned: string }>(
        "/api/admin/settings/outbounds/nodes",
      );
      setSavedNodes(data.nodes ?? []);
      setPinnedId(data.pinned ?? "");
    } catch (e) {
      setOutboundError((e as Error).message);
    }
  }, []);

  const load = useCallback(async () => {
    setLoadError("");
    try {
      const data = await adminApi<SettingsView>("/api/admin/settings");
      setView(data);
      // 金额以最小单位存储，展示时换回主单位再交给用户编辑。
      setMerchant(data.catalog.merchant);
      setCurrency(data.catalog.currency);
      const plans = data.catalog.plans ?? [];
      setPlanMonths([plans[0]?.months ? String(plans[0].months) : "", plans[1]?.months ? String(plans[1].months) : ""]);
      setPlanAmount([plans[0] ? (plans[0].amount / 100).toFixed(2) : "", plans[1] ? (plans[1].amount / 100).toFixed(2) : ""]);
      setPlanProduct([plans[0]?.product ?? "", plans[1]?.product ?? ""]);
      setProxyMode(data.proxy.direct ? "direct" : "json");
    } catch (e) {
      setLoadError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    void load();
    void loadOutbounds();
    void loadNodeList();
  }, [load, loadOutbounds, loadNodeList]);

  async function saveOutbounds() {
    if (submitting.current) return;
    submitting.current = true;
    setBusy(true);
    setError("");
    setNotice("");
    setOutboundError("");
    try {
      const body = useRaw ? { nodes: [], raw_json: rawJSON } : { nodes, raw_json: "" };
      const result = await adminApi<{ restart_required?: boolean }>("/api/admin/settings/outbounds", body);
      setNotice("付款出站已保存。" + (result?.restart_required ? " 重启服务后生效。" : " 下一笔订单即按新出口执行。"));
      if (!useRaw) setNodes([]);
      setRawJSON("");
      await loadOutbounds();
      await loadNodeList();
    } catch (e) {
      setOutboundError((e as Error).message);
    } finally {
      setBusy(false);
      submitting.current = false;
    }
  }

  // 测试一个节点。不写 vault、不动配置，只报告能不能用。
  async function testNode(n: SavedNode) {
    if (probing) return;
    setProbing(n.id);
    setOutboundError("");
    setProbes((prev) => {
      const next = { ...prev };
      delete next[n.id];
      return next;
    });
    try {
      const r = await adminApi<ProbeResult>("/api/admin/settings/outbounds/probe", { id: n.id });
      setProbes((prev) => ({ ...prev, [n.id]: r }));
    } catch (e) {
      setOutboundError((e as Error).message);
    } finally {
      setProbing("");
    }
  }

  // 指定/取消指定。立即生效（下一笔付款就按这个节点）。
  async function pinNode(id: string) {
    if (submitting.current) return;
    submitting.current = true;
    setBusy(true);
    setOutboundError("");
    setNotice("");
    try {
      await adminApi("/api/admin/settings/outbounds/pin", { id });
      await loadNodeList();
      setNotice(id ? "已指定该节点，后续付款优先走它（若它进入冷却会自动改用其他节点）。" : "已恢复为轮转。");
    } catch (e) {
      setOutboundError((e as Error).message);
    } finally {
      setBusy(false);
      submitting.current = false;
    }
  }

  // 删除一个已保存的节点。危险操作，所以要确认。
  async function deleteNode(n: SavedNode) {
    if (submitting.current) return;
    if (!window.confirm(
      `删除节点「${n.tag || n.server || n.id}」？

` +
        `删除后下一笔付款不再使用它。若池子里已经没有其他可用节点，付款会直接失败。`,
    )) {
      return;
    }
    submitting.current = true;
    setBusy(true);
    setOutboundError("");
    setNotice("");
    try {
      const r = await adminApi<{ removed: number; remaining: number }>(
        "/api/admin/settings/outbounds/delete",
        { id: n.id },
      );
      setProbes((prev) => {
        const next = { ...prev };
        delete next[n.id];
        return next;
      });
      await Promise.all([loadNodeList(), loadOutbounds()]);
      setNotice(
        r.remaining === 0
          ? "已删除最后一个节点。付款将走直连——X 会按服务器所在地定价，通常更贵。"
          : `已删除该节点，池里还剩 ${r.remaining} 个。`,
      );
    } catch (e) {
      setOutboundError((e as Error).message);
    } finally {
      setBusy(false);
      submitting.current = false;
    }
  }

  async function save(label: string, path: string, body: unknown) {
    if (submitting.current) return;
    submitting.current = true;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      const result = await adminApi<{ restart_required?: boolean }>(path, body);
      setNotice(
        label + "已保存。" + (result?.restart_required ? " 代理在服务启动时加载，重启服务后生效。" : ""),
      );
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
      submitting.current = false;
    }
  }

  function planBodies() {
    const plans = [];
    for (let i = 0; i < 2; i += 1) {
      const months = planMonths[i]?.trim();
      if (!months) continue;
      plans.push({ months: Number(months), amount: planAmount[i]?.trim() ?? "", product: planProduct[i]?.trim() ?? "" });
    }
    return plans;
  }

  const cardReady =
    card.number.trim() && card.exp_month.trim() && card.exp_year.trim() &&
    card.cvc.trim() && card.billing_name.trim() && card.email.trim() && card.billing_country.trim();

  // 后端在无节点时可能返回 null，这里统一归一化，避免直接对 null 取属性。
  const tagsText = (outbounds?.tags ?? []).join(" · ");

  return (
    <Box sx={{ display: "grid", gap: 3 }}>
      {loadError && (
        <Alert severity="error" action={<Button color="inherit" onClick={() => void load()}>重新加载</Button>}>
          {loadError}
        </Alert>
      )}

      {/* 每个分区由左侧导航单独进入，这里只渲染当前那块 */}
      {section === "credentials" && (
      <Section
        id="settings-credentials"
        icon={<KeyOutlined fontSize="small" />}
        title="X 登录凭据"
        hint={`当前 auth_token：${view?.credentials.auth_token_tail || "未配置"}；ct0：${view?.credentials.has_ct0 ? "已配置" : "未配置"}。下面留空的字段保持原值不变。`}
      >
        <Stack spacing={2}>
          {/* 换凭据时逐个抄太费劲，跟引导页一样支持整段粘贴自动拆分。
              界面上必须说清两件事，否则运营会在这三个动作之间犹豫：
              ① 单字段是从上面粘贴自动带出来的，通常不用手填；
              ② 留空 = 保持原值，不会把已有配置清掉。 */}
          <TextField
            label="粘贴整串 Cookie / JSON（自动填充下面的字段）"
            value={cookiePaste}
            onChange={(e) => {
              setCookiePaste(e.target.value);
              applyCookiePaste(e.target.value);
            }}
            multiline
            minRows={3}
            disabled={busy}
            placeholder={
              'auth_token=abc123…; ct0=def456…\n或：[{"name":"auth_token","value":"abc…"},{"name":"ct0","value":"def…"}]'
            }
            helperText="从浏览器开发者工具的 Application → Cookies 里复制整条 auth_token 与 ct0，或直接粘贴它们的 JSON。"
            slotProps={{ htmlInput: { spellCheck: false } }}
          />
          {pasteNote && (
            <Alert severity={pasteOk ? "success" : "warning"} sx={{ py: 0.5 }}>
              {pasteNote}
            </Alert>
          )}
          <Typography variant="body2" color="text.secondary">
            下面是自动拆出来的字段，也可以在下面直接改。留空保持原值不变。
          </Typography>
          <TextField label="auth_token" value={authToken} onChange={(e) => setAuthToken(e.target.value)} autoComplete="off" disabled={busy} helperText="X 登录后的 Bearer 令牌，是识别当前操作账号的依据。" slotProps={{ htmlInput: { spellCheck: false } }} />
          <TextField label="ct0" value={ct0} onChange={(e) => setCT0(e.target.value)} autoComplete="off" disabled={busy} helperText="X 的 CSRF 令牌，与 auth_token 配套，通常一起变。" slotProps={{ htmlInput: { spellCheck: false } }} />
          <Grid cols={2}>
            <TextField label="Authorization（留空保持原值）" value={authorization} onChange={(e) => setAuthorization(e.target.value)} autoComplete="off" disabled={busy} helperText="完整请求头值，一般是 Bearer <token>。" slotProps={{ htmlInput: { spellCheck: false } }} />
            <TextField label="User-Agent（留空保持原值）" value={userAgent} onChange={(e) => setUserAgent(e.target.value)} autoComplete="off" disabled={busy} helperText="需与登录时的浏览器一致。" />
          </Grid>
          <Box>
            <Button
              variant="contained"
              disabled={busy || (!authToken.trim() && !ct0.trim() && !authorization.trim() && !userAgent.trim())}
              onClick={() =>
                void save("X 凭据", "/api/admin/settings/credentials", {
                  auth_token: authToken,
                  ct0: ct0,
                  authorization: authorization,
                  user_agent: userAgent,
                })
              }
            >
              保存凭据
            </Button>
          </Box>
        </Stack>
      </Section>
      )}

      {section === "cards" && (
      <Section
        id="settings-cards"
        icon={<CreditCardOutlined fontSize="small" />}
        title="支付卡"
        hint="下单时用这里的卡向 X 付款。卡片信息加密保存，页面只显示尾号。"
      >
        {view?.cards.length ? (
          <Stack spacing={1} sx={{ mb: 2.5 }}>
            {view.cards.map((c) => (
              <Box
                key={c.last4}
                sx={{
                  display: "flex", alignItems: "center", gap: 1.5, flexWrap: "wrap",
                  px: 1.5, py: 1, borderRadius: 1.5, bgcolor: "action.hover",
                }}
              >
                <CreditCardOutlined fontSize="small" sx={{ color: "text.secondary" }} />
                <Typography variant="body2" sx={{ fontFamily: "monospace", fontWeight: 600 }}>
                  •••• {c.last4}
                </Typography>
                <Chip
                  size="small"
                  variant={c.usable ? "filled" : "outlined"}
                  color={c.usable ? "success" : "warning"}
                  label={
                    c.usable
                      ? "可用"
                      : c.blocked
                        ? "已封禁"
                        : c.problem
                          ? c.problem
                          : c.cooling_seconds
                            ? `冷却 ${Math.ceil(c.cooling_seconds / 60)} 分钟`
                            : "不可用"
                  }
                />
                <Box sx={{ flex: 1 }} />
                <Button
                  size="small"
                  color="error"
                  disabled={busy}
                  onClick={() => void save("卡片移除", "/api/admin/settings/cards/remove", { last4: c.last4 })}
                >
                  移除
                </Button>
              </Box>
            ))}
            <Box>
              <Button size="small" variant="outlined" disabled={busy} onClick={() => void save("解封", "/api/admin/settings/cards/unblock", {})}>
                解封全部卡片
              </Button>
            </Box>
          </Stack>
        ) : (
          <Alert severity="info" sx={{ mb: 2.5 }}>尚未配置支付卡。没有可用卡片时无法向 X 下单。</Alert>
        )}

        <Typography variant="overline" color="text.secondary" sx={{ display: "block", mb: 1 }}>
          新增一张卡
        </Typography>
        <Stack spacing={2}>
          <TextField label="卡号" value={card.number} onChange={(e) => setCard({ ...card, number: e.target.value })} autoComplete="off" disabled={busy} slotProps={{ htmlInput: { spellCheck: false } }} />
          <Grid cols={3}>
            <TextField label="有效期月" placeholder="09" value={card.exp_month} onChange={(e) => setCard({ ...card, exp_month: e.target.value })} disabled={busy} />
            <TextField label="有效期年" placeholder="2029" value={card.exp_year} onChange={(e) => setCard({ ...card, exp_year: e.target.value })} disabled={busy} />
            <TextField label="CVC" value={card.cvc} onChange={(e) => setCard({ ...card, cvc: e.target.value })} disabled={busy} />
          </Grid>
          <Grid cols={2}>
            <TextField label="持卡人姓名" value={card.billing_name} onChange={(e) => setCard({ ...card, billing_name: e.target.value })} disabled={busy} />
            <TextField label="邮箱" type="email" value={card.email} onChange={(e) => setCard({ ...card, email: e.target.value })} disabled={busy} />
          </Grid>
          <Grid cols={2}>
            <TextField label="账单国家" placeholder="US" value={card.billing_country} onChange={(e) => setCard({ ...card, billing_country: e.target.value })} disabled={busy} />
            <TextField label="邮编（可选）" value={card.billing_postal_code ?? ""} onChange={(e) => setCard({ ...card, billing_postal_code: e.target.value })} disabled={busy} />
          </Grid>
          <Box>
            <Button
              variant="contained"
              disabled={busy || !cardReady}
              onClick={async () => {
                await save("支付卡", "/api/admin/settings/cards", { cards: [card] });
                setCard(emptyCard);
              }}
            >
              添加支付卡
            </Button>
          </Box>
        </Stack>
      </Section>
      )}

      {section === "catalog" && (
      <Section
        id="settings-stripe"
        icon={<PaymentsOutlined fontSize="small" />}
        title="商品与价格"
        hint="这些是 X 结账页使用的商品与商户标识，用于向 X 下单购买赠送 —— 不是你的收款渠道，买家如何付款由你另行决定。金额必须与 X 实际收取的一致，否则下单会被拒绝。"
      >
        {!view?.stripe.present && (
          <Alert severity="info" sx={{ mb: 2.5 }}>
            留空即表示不启用在线收款。填了任意一项就必须填齐公钥、商户账号和至少一个套餐。
          </Alert>
        )}
        <Stack spacing={2}>
          <TextField label="Stripe 公钥" placeholder="pk_test_… 或 pk_live_…" value={stripeKey} onChange={(e) => setStripeKey(e.target.value)} autoComplete="off" disabled={busy} slotProps={{ htmlInput: { spellCheck: false } }} />
          <Box>
            <Button variant="contained" disabled={busy || !stripeKey.trim()} onClick={() => void save("Stripe 公钥", "/api/admin/settings/stripe", { stripe_key: stripeKey })}>
              保存公钥
            </Button>
          </Box>
          <Grid cols={2}>
            <TextField label="商户账号" placeholder="acct_…" value={merchant} onChange={(e) => setMerchant(e.target.value)} disabled={busy} slotProps={{ htmlInput: { spellCheck: false } }} />
            <TextField label="币种" placeholder="usd" value={currency} onChange={(e) => setCurrency(e.target.value)} disabled={busy} />
          </Grid>
          {[0, 1].map((i) => (
            <Grid key={i} cols={3}>
              <TextField label={`套餐 ${i + 1} 月数`} type="number" value={planMonths[i]} onChange={(e) => setPlanMonths(planMonths.map((v, j) => (j === i ? e.target.value : v)))} disabled={busy} slotProps={{ htmlInput: { min: 1, max: 24 } }} />
              <TextField label={`套餐 ${i + 1} 金额`} value={planAmount[i]} onChange={(e) => setPlanAmount(planAmount.map((v, j) => (j === i ? e.target.value : v)))} disabled={busy} />
              <TextField label={`套餐 ${i + 1} 商品`} placeholder="prod_…" value={planProduct[i]} onChange={(e) => setPlanProduct(planProduct.map((v, j) => (j === i ? e.target.value : v)))} disabled={busy} slotProps={{ htmlInput: { spellCheck: false } }} />
            </Grid>
          ))}
          <Box>
            <Button
              variant="contained"
              disabled={busy || !planBodies().length}
              onClick={() => void save("套餐", "/api/admin/settings/catalog", { merchant, currency, plans: planBodies() })}
            >
              保存套餐
            </Button>
          </Box>
        </Stack>
      </Section>
      )}

      {section === "outbounds" && (
      <Section
        id="settings-outbounds"
        icon={<SettingsEthernetOutlined fontSize="small" />}
        title="付款出站"
        hint="下单与询价都走这里的出口。X 按出口所在国报价，所以要拿低价区就把出口放在那个国家。留空即直连（按服务器所在地报价）。"
      >
        <Stack spacing={2.5}>
          {outbounds && (
            <Box
              sx={{
                display: "flex", alignItems: "center", gap: 1.5, flexWrap: "wrap",
                px: 1.5, py: 1, borderRadius: 1.5, bgcolor: "action.hover",
              }}
            >
              <Chip
                size="small"
                color={outbounds.mode === "pool" ? "success" : "default"}
                label={outbounds.mode === "pool" ? "代理池" : "直连"}
              />
              <Typography variant="body2">
                {outbounds.mode === "pool"
                  ? `共 ${outbounds.nodes} 个节点，可用 ${outbounds.available}${outbounds.cooling ? `，冷却 ${outbounds.cooling}` : ""}`
                  : "未配置出站节点，使用服务器自身网络"}
              </Typography>
              {tagsText && (
                <Typography variant="body2" color="text.secondary" sx={{ fontFamily: "monospace" }}>
                  {tagsText}
                </Typography>
              )}
            </Box>
          )}

          {outboundError && <Alert severity="error">{outboundError}</Alert>}

          <FormControlLabel
            control={<Checkbox checked={useRaw} disabled={busy} onChange={(e) => setUseRaw(e.target.checked)} />}
            label="改用原始 JSON（anytls / hysteria2 / 带 transport 的复杂节点）"
          />

          {useRaw ? (
            <TextField
              label="出站节点数组 JSON"
              value={rawJSON}
              onChange={(e) => setRawJSON(e.target.value)}
              multiline
              minRows={5}
              disabled={busy}
              placeholder='[{"type":"socks","tag":"bd","server":"1.2.3.4","server_port":1080,"version":"5","username":"u","password":"p"}]'
              slotProps={{ htmlInput: { spellCheck: false } }}
            />
          ) : (
            <>
              {nodes.map((n, i) => (
                <Box key={i} sx={{ border: 1, borderColor: "divider", borderRadius: 2, p: 2 }}>
                  <Stack direction="row" alignItems="center" sx={{ mb: 1.5 }}>
                    <Typography variant="overline" color="text.secondary">节点 {i + 1}</Typography>
                    <Box sx={{ flex: 1 }} />
                    <Button size="small" color="error" disabled={busy} onClick={() => setNodes(nodes.filter((_, j) => j !== i))}>
                      删除
                    </Button>
                  </Stack>
                  <Stack spacing={2}>
                    <Grid cols={2}>
                      <TextField
                        select
                        label="类型"
                        value={n.type}
                        disabled={busy}
                        onChange={(e) => setNodes(nodes.map((v, j) => (j === i ? { ...v, type: e.target.value } : v)))}
                      >
                        {OUTBOUND_TYPES.map((t) => (
                          <MenuItem key={t.value} value={t.value}>{t.label}</MenuItem>
                        ))}
                      </TextField>
                      <TextField
                        label="标签"
                        placeholder="bd-1"
                        value={n.tag}
                        disabled={busy}
                        onChange={(e) => setNodes(nodes.map((v, j) => (j === i ? { ...v, tag: e.target.value } : v)))}
                      />
                    </Grid>
                    {n.type !== "direct" && (
                      <>
                        <Grid cols={2}>
                          <TextField
                            label="服务器"
                            placeholder="1.2.3.4 或 host.example.com"
                            value={n.server}
                            disabled={busy}
                            onChange={(e) => setNodes(nodes.map((v, j) => (j === i ? { ...v, server: e.target.value } : v)))}
                            slotProps={{ htmlInput: { spellCheck: false } }}
                          />
                          <TextField
                            label="端口"
                            placeholder="1080"
                            value={n.server_port}
                            disabled={busy}
                            onChange={(e) => setNodes(nodes.map((v, j) => (j === i ? { ...v, server_port: e.target.value } : v)))}
                          />
                        </Grid>
                        {(n.type === "http" || n.type === "socks") && (
                          <Grid cols={2}>
                            <TextField
                              label="用户名（可选）"
                              value={n.username ?? ""}
                              disabled={busy}
                              autoComplete="off"
                              onChange={(e) => setNodes(nodes.map((v, j) => (j === i ? { ...v, username: e.target.value } : v)))}
                            />
                            <TextField
                              label="密码（可选）"
                              type="password"
                              value={n.password ?? ""}
                              disabled={busy}
                              autoComplete="new-password"
                              onChange={(e) => setNodes(nodes.map((v, j) => (j === i ? { ...v, password: e.target.value } : v)))}
                            />
                          </Grid>
                        )}
                        {n.type === "shadowsocks" && (
                          <Grid cols={2}>
                            <TextField
                              select
                              label="加密方式"
                              value={n.method ?? "aes-256-gcm"}
                              disabled={busy}
                              onChange={(e) => setNodes(nodes.map((v, j) => (j === i ? { ...v, method: e.target.value } : v)))}
                            >
                              {["aes-256-gcm", "aes-128-gcm", "chacha20-ietf-poly1305", "xchacha20-ietf-poly1305", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305", "none"].map((m) => (
                                <MenuItem key={m} value={m}>{m}</MenuItem>
                              ))}
                            </TextField>
                            <TextField
                              label="密码"
                              type="password"
                              value={n.password ?? ""}
                              disabled={busy}
                              autoComplete="new-password"
                              onChange={(e) => setNodes(nodes.map((v, j) => (j === i ? { ...v, password: e.target.value } : v)))}
                            />
                          </Grid>
                        )}
                        {(n.type === "vmess" || n.type === "vless") && (
                          <Grid cols={2}>
                            <TextField
                              label="UUID"
                              value={n.uuid ?? ""}
                              disabled={busy}
                              onChange={(e) => setNodes(nodes.map((v, j) => (j === i ? { ...v, uuid: e.target.value } : v)))}
                              slotProps={{ htmlInput: { spellCheck: false } }}
                            />
                            <TextField
                              label="SNI"
                              placeholder="www.example.com"
                              value={n.sni ?? ""}
                              disabled={busy}
                              onChange={(e) => setNodes(nodes.map((v, j) => (j === i ? { ...v, sni: e.target.value } : v)))}
                            />
                          </Grid>
                        )}
                        {n.type === "vless" && (
                          <Grid cols={3}>
                            <TextField
                              select
                              label="Security"
                              value={n.security ?? "tls"}
                              disabled={busy}
                              onChange={(e) => setNodes(nodes.map((v, j) => (j === i ? { ...v, security: e.target.value } : v)))}
                            >
                              <MenuItem value="tls">tls</MenuItem>
                              <MenuItem value="reality">reality</MenuItem>
                              <MenuItem value="none">none</MenuItem>
                            </TextField>
                            <TextField
                              select
                              label="传输"
                              value={n.network ?? ""}
                              disabled={busy}
                              onChange={(e) => setNodes(nodes.map((v, j) => (j === i ? { ...v, network: e.target.value } : v)))}
                            >
                              <MenuItem value="">默认</MenuItem>
                              <MenuItem value="ws">WebSocket</MenuItem>
                              <MenuItem value="grpc">gRPC</MenuItem>
                            </TextField>
                            <TextField
                              label="路径 / service"
                              value={n.ws_path ?? ""}
                              disabled={busy}
                              onChange={(e) => setNodes(nodes.map((v, j) => (j === i ? { ...v, ws_path: e.target.value } : v)))}
                            />
                          </Grid>
                        )}
                        {(n.type === "trojan" || n.type === "anytls" || n.type === "hysteria2") && (
                          <Grid cols={2}>
                            <TextField
                              label="密码"
                              type="password"
                              value={n.password ?? ""}
                              disabled={busy}
                              autoComplete="new-password"
                              onChange={(e) => setNodes(nodes.map((v, j) => (j === i ? { ...v, password: e.target.value } : v)))}
                            />
                            <TextField
                              label="SNI"
                              placeholder="www.example.com"
                              value={n.sni ?? ""}
                              disabled={busy}
                              onChange={(e) => setNodes(nodes.map((v, j) => (j === i ? { ...v, sni: e.target.value } : v)))}
                            />
                          </Grid>
                        )}
                        {n.type === "http" && (
                          <FormControlLabel
                            control={
                              <Checkbox
                                checked={Boolean(n.tls)}
                                disabled={busy}
                                onChange={(e) => setNodes(nodes.map((v, j) => (j === i ? { ...v, tls: e.target.checked } : v)))}
                              />
                            }
                            label="使用 TLS（HTTPS 代理）"
                          />
                        )}
                      </>
                    )}
                  </Stack>
                </Box>
              ))}
              <Box>
                <Button variant="outlined" startIcon={<AddRounded />} disabled={busy || nodes.length >= 128} onClick={() => setNodes([...nodes, emptyNode()])}>
                  添加节点
                </Button>
              </Box>
            </>
          )}

          {/* 已保存的节点清单。
              放在"保存出站节点"按钮上方，因为这两件事要分清：
              上方是**已经生效**的池子（测试/指定/删除都立刻起作用），
              下方表单是**待保存**的编辑稿。顺序反过来会让人以为
              "填完要保存"才是生效路径。 */}
          <Box sx={{ border: 1, borderColor: "divider", borderRadius: 2, overflow: "hidden" }}>
            <Stack
              direction="row"
              alignItems="center"
              sx={{ px: 2, py: 1.25, bgcolor: "surfaceContainerLow" }}
            >
              <Typography variant="subtitle2">
                已生效的节点（{savedNodes.length}）
              </Typography>
              <Box sx={{ flex: 1 }} />
              <Typography variant="body2" color="text.secondary">
                {pinnedId ? "已指定首选节点" : "按轮转选择"}
              </Typography>
            </Stack>

            {savedNodes.length === 0 ? (
              <Typography variant="body2" color="text.secondary" sx={{ px: 2, py: 2 }}>
                当前没有节点，付款走直连 —— X 会按服务器所在地定价，通常比低价区贵。
                用下面的表单添加，保存后立刻生效。
              </Typography>
            ) : (
              <Stack divider={<Divider flexItem />}>
                {savedNodes.map((n) => {
                  const pr = probes[n.id];
                  return (
                    <Box key={n.id} sx={{ px: 2, py: 1.5 }}>
                      <Stack
                        direction={{ xs: "column", sm: "row" }}
                        alignItems={{ sm: "center" }}
                        spacing={1}
                      >
                        <Box sx={{ minWidth: 0, flex: 1 }}>
                          <Stack direction="row" alignItems="center" spacing={1}>
                            <Typography variant="body2" sx={{ fontWeight: 600 }} noWrap>
                              {n.tag || "(未命名)"}
                            </Typography>
                            <Chip size="small" label={n.type} />
                            {n.pinned && (
                              <Chip size="small" color="primary" label="首选" />
                            )}
                          </Stack>
                          <Typography
                            variant="body2"
                            color="text.secondary"
                            noWrap
                            sx={{ mt: 0.25 }}
                          >
                            {n.server ? `${n.server}:${n.server_port ?? ""}` : "直连"}
                            {n.country ? ` · 标注 ${n.country}` : ""}
                          </Typography>
                        </Box>

                        <Stack direction="row" spacing={1} sx={{ flexShrink: 0 }}>
                          <Button
                            size="small"
                            variant="outlined"
                            disabled={busy || Boolean(probing)}
                            startIcon={
                              probing === n.id ? (
                                <CircularProgress size={14} aria-hidden="true" />
                              ) : (
                                <NetworkCheckRounded />
                              )
                            }
                            onClick={() => void testNode(n)}
                          >
                            {probing === n.id ? "测试中" : "测试"}
                          </Button>
                          <Button
                            size="small"
                            variant={n.pinned ? "contained" : "outlined"}
                            disabled={busy}
                            onClick={() => void pinNode(n.pinned ? "" : n.id)}
                          >
                            {n.pinned ? "取消首选" : "设为首选"}
                          </Button>
                          <Button
                            size="small"
                            color="error"
                            disabled={busy || Boolean(probing)}
                            onClick={() => void deleteNode(n)}
                          >
                            删除
                          </Button>
                        </Stack>
                      </Stack>

                      {pr && (
                        <Alert
                          severity={pr.ok ? "success" : "error"}
                          sx={{ mt: 1.5, py: 0.5 }}
                          onClose={() =>
                            setProbes((prev) => {
                              const next = { ...prev };
                              delete next[n.id];
                              return next;
                            })
                          }
                        >
                          {pr.ok
                            ? `出口可用，IP ${pr.ip}，${pr.reach_x ? `可达 x.com（HTTP ${pr.x_status}）` : "但无法确认可达 x.com"}，耗时 ${pr.elapsed_ms}ms`
                            : `${pr.message}（${pr.elapsed_ms}ms）`}
                        </Alert>
                      )}
                    </Box>
                  );
                })}
              </Stack>
            )}
          </Box>

          <Box sx={{ display: "flex", gap: 1.5, flexWrap: "wrap" }}>
            <Button
              variant="contained"
              disabled={busy || (useRaw ? !rawJSON.trim() : nodes.length === 0)}
              onClick={() => void saveOutbounds()}
            >
              {nodes.length > 0 || rawJSON.trim() ? "保存出站节点" : "保存出站节点"}
            </Button>
            {(nodes.length > 0 || rawJSON.trim()) && (
              <Button disabled={busy} onClick={() => { setNodes([]); setRawJSON(""); }}>
                清空表单
              </Button>
            )}
          </Box>
          <Typography variant="body2" color="text.secondary">
            保存空列表即恢复直连。已在途的订单会继续走原先绑定的出口，新订单按新配置选择。
          </Typography>
        </Stack>
      </Section>
      )}

      {section === "proxy" && (
      <Section
        id="settings-proxy"
        icon={<CloudOutlined fontSize="small" />}
        title="账号查询出口"
        hint="仅用于向 X 查询账号资格，不影响下单与定价。账号查询直连即可，通常不必修改。"
      >
        <Stack spacing={2}>
          <Typography variant="body2" color="text.secondary">
            当前：{outbounds?.proxy_mode ? outbounds.proxy_mode : (view?.proxy.direct ? "直连" : (view?.proxy.tags ?? []).join("、") || "未知")}
          </Typography>
          <TextField select label="方式" value={proxyMode} onChange={(e) => setProxyMode(e.target.value)} disabled={busy}>
            <MenuItem value="direct">直连</MenuItem>
            <MenuItem value="json">粘贴 sing-box 配置</MenuItem>
          </TextField>
          {proxyMode === "json" && (
            <TextField label="sing-box 配置 JSON" value={proxyJSON} onChange={(e) => setProxyJSON(e.target.value)} multiline minRows={4} disabled={busy} slotProps={{ htmlInput: { spellCheck: false } }} />
          )}
          <Box>
            <Button
              variant="contained"
              disabled={busy || (proxyMode === "json" && !proxyJSON.trim())}
              onClick={() => void save("账号查询出口", "/api/admin/settings/proxy", { proxy_mode: proxyMode, proxy_json: proxyJSON })}
            >
              保存账号查询出口
            </Button>
          </Box>
          <Typography variant="body2" color="text.secondary">
            这一项在服务启动时加载，改动后需要重启服务才生效。
          </Typography>
        </Stack>
      </Section>
      )}

      {error && <Alert severity="error">{error}</Alert>}
      {notice && <Alert severity="success" onClose={() => setNotice("")}>{notice}</Alert>}
    </Box>
  );
}
