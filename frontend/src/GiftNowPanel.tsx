import { useCallback, useEffect, useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  LinearProgress,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import SendOutlined from "@mui/icons-material/SendOutlined";
import { Panel } from "./Panel";
import { adminApi, formatTime } from "./adminApi";

// 后台直接赠送：填接收账号和套餐时长，跑完整个链路直到付款。
//
// 设计要点：
//   - 任务化。一次完整下单实测 15 秒以上（含询价、建单、tokenize、
//     提交、核实），同步请求会超时，所以提交后立刻返回 task_id，
//     页面按秒轮询。
//   - 失败必须说清"哪里错"。这一页存在的理由就是 2026-10-08 那次：
//     手动链接页把所有失败压成"该账号目前无法接收 Premium 赠送"，
//     实际可能是发送账号被 X 限流（code 37）、X 接口改了、
//     付款出口没配、套餐金额不匹配——完全不同的处置方式。
//     所以失败时直接展示分类、X 原始 code 与 message、以及该改什么。
//   - 付款证据警告。如果订单已产生 session 或已提交，失败提示会额外
//     提醒先核实 Stripe 是否已扣款，避免"看到失败就重试"造成重复扣款。

type Stage = {
  percent: number;
  message: string;
  at: number;
  done?: boolean;
  failed?: boolean;
  error?: string;
  category?: string;
  hint?: string;
};

type Diagnosis = {
  category: string;
  summary: string;
  detail: string;
  hint: string;
  x_code?: number;
  x_message?: string;
  stage?: string;
};

type Task = {
  username: string;
  recipient?: string;
  months: number;
  amount?: number;
  currency?: string;
  state: string; // queued | running | succeeded | failed
  stage: Stage;
  stages: Stage[];
  created: number;
  updated: number;
  session_id?: string;
  card_last4?: string;
  message?: string;
  diagnosis?: Diagnosis;
};

type Plan = { months: number; amount: number; currency: string };

// 后端给出的分类 → 中文标题。前端不重新判断，只做展示映射。
const CATEGORY_LABELS: Record<string, string> = {
  sender_not_authorised: "发送账号被 X 限制",
  operation_stale: "X 接口标识可能已变更",
  recipient_ineligible: "接收账号不可接收",
  recipient_not_found: "接收账号不存在",
  x_read_failure: "X 查询失败",
  egress_unavailable: "付款出口不可用",
  price_mismatch: "价格不匹配",
  payment_paused: "付款已暂停",
  order_busy: "订单占用中",
  x_error: "X 返回错误",
  unknown: "未归类",
};

const STATE_LABELS: Record<string, { label: string; color: "default" | "primary" | "success" | "warning" | "error" }> = {
  queued: { label: "排队中", color: "default" },
  running: { label: "进行中", color: "primary" },
  succeeded: { label: "已付款", color: "success" },
  failed: { label: "失败", color: "error" },
};

export function GiftNowPanel({ disabled = false }: { disabled?: boolean }) {
  const [username, setUsername] = useState("");
  const [months, setMonths] = useState(3);
  const [plans, setPlans] = useState<Plan[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [tasks, setTasks] = useState<Task[]>([]);
  const [current, setCurrent] = useState<string>("");
  const pollRef = useRef<number | null>(null);

  const loadTasks = useCallback(async () => {
    try {
      const d = await adminApi<{ tasks: Task[] }>("/api/admin/gift/tasks");
      setTasks(d.tasks ?? []);
    } catch {
      // 列表刷新失败不打断当前任务，进度轮询自己会带出错误。
    }
  }, []);

  useEffect(() => {
    void loadTasks();
    adminApi<{ plans: Plan[] }>("/api/admin/gift/plans")
      .then((d) => {
        setPlans(d.plans ?? []);
        // 套餐只有一个时长时直接选中，省掉一次点击。
        if (d.plans?.length === 1) setMonths(d.plans[0].months);
      })
      .catch(() => setError("无法读取套餐配置。"));
  }, [loadTasks]);

  // 轮询只在有任务未结束时运行，避免空转。
  useEffect(() => {
    if (!current) return;
    const tick = async () => {
      try {
        const t = await adminApi<Task>(`/api/admin/gift/${current}`);
        setTasks((prev) => [t, ...prev.filter((x) => x.username + x.created !== t.username + t.created)]);
        if (t.state === "succeeded" || t.state === "failed") {
          setCurrent("");
          setBusy(false);
        }
      } catch (e) {
        setError(e instanceof Error ? e.message : "读取任务状态失败。");
        setCurrent("");
        setBusy(false);
      }
    };
    void tick();
    pollRef.current = window.setInterval(() => void tick(), 2000);
    return () => {
      if (pollRef.current) window.clearInterval(pollRef.current);
      pollRef.current = null;
    };
  }, [current]);

  const start = async () => {
    const name = username.trim().replace(/^@/, "");
    if (!name) {
      setError("请填写接收账号。");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const r = await adminApi<{ task_id: string }>("/api/admin/gift", { username: name, months });
      setCurrent(r.task_id);
      setUsername("");
      await loadTasks();
    } catch (e) {
      setError(e instanceof Error ? e.message : "发起赠送失败。");
      setBusy(false);
    }
  };

  return (
    <Panel
      id="gift-now"
      title="立即赠送"
      icon={<SendOutlined />}
      hint="填接收账号和套餐时长，后台跑完整流程直到付款。这会真的向 X 付款，请确认账号与时长无误。"
      actions={
        <Chip
          size="small"
          variant="outlined"
          color="error"
          label="会真实扣款"
          sx={{ fontSize: 11.5 }}
        />
      }
    >
      <Stack
        component="form"
        spacing={2}
        onSubmit={(e) => {
          e.preventDefault();
          if (!busy) void start();
        }}
        sx={{ display: "grid", gap: 2, gridTemplateColumns: { xs: "1fr", sm: "2fr 1fr auto" }, alignItems: "start" }}
      >
        <TextField
          label="接收账号"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          placeholder="例如 ABG19940928（不需要 @）"
          disabled={busy || disabled}
          size="small"
          slotProps={{ htmlInput: { maxLength: 15, autoComplete: "off" } }}
        />
        <TextField
          select
          label="套餐时长"
          value={months}
          onChange={(e) => setMonths(Number(e.target.value))}
          disabled={busy || disabled}
          size="small"
        >
          {plans.map((p) => (
            <MenuItem key={p.months} value={p.months}>
              {p.months} 个月 · {p.currency} {p.amount}
            </MenuItem>
          ))}
        </TextField>
        <Button
          type="submit"
          variant="contained"
          disabled={busy || disabled || plans.length === 0}
          startIcon={<SendOutlined />}
          sx={{ minWidth: 132 }}
        >
          {busy ? "正在赠送…" : "开始赠送"}
        </Button>
      </Stack>

      {error && (
        <Alert severity="error" sx={{ mt: 2 }} onClose={() => setError("")}>
          {error}
        </Alert>
      )}

      {tasks.length > 0 && (
        <Box sx={{ mt: 3 }}>
          <Typography variant="h3" component="h3" sx={{ fontSize: 12, fontWeight: 650, color: "text.secondary", letterSpacing: ".06em", textTransform: "uppercase", mb: 1.25 }}>
            最近任务
          </Typography>
          <Stack spacing={1.5}>
            {tasks.slice(0, 8).map((t) => (
              <TaskCard key={t.username + t.created + t.months} task={t} />
            ))}
          </Stack>
        </Box>
      )}
    </Panel>
  );
}

function TaskCard({ task }: { task: Task }) {
  const state = STATE_LABELS[task.state] ?? { label: task.state, color: "default" as const };
  const d = task.diagnosis;
  return (
    <Box
      sx={{
        border: 1,
        borderColor: task.state === "failed" ? "error.main" : "divider",
        borderRadius: 2,
        p: 2,
        bgcolor: task.state === "failed" ? "action.hover" : "background.paper",
      }}
    >
      <Stack direction="row" alignItems="center" spacing={1.25} sx={{ mb: 0.75, flexWrap: "wrap", rowGap: 0.75 }}>
        <Typography sx={{ fontWeight: 650, fontSize: 14 }}>@{task.username}</Typography>
        <Chip size="small" variant="outlined" label={`${task.months} 个月`} />
        <Chip size="small" color={state.color} label={state.label} />
        {task.card_last4 && <Chip size="small" variant="outlined" label={`尾号 ${task.card_last4}`} />}
        <Typography variant="caption" color="text.secondary" sx={{ ml: "auto" }}>
          {formatTime(task.created)}
        </Typography>
      </Stack>

      {task.state === "running" && (
        <>
          <Typography variant="body2" sx={{ mb: 1 }}>
            {task.stage?.message ?? "处理中…"}
          </Typography>
          <LinearProgress
            variant={task.stage?.percent > 0 ? "determinate" : "indeterminate"}
            value={Math.min(100, task.stage?.percent ?? 0)}
            sx={{ height: 6, borderRadius: 3 }}
          />
        </>
      )}

      {task.state === "queued" && (
        <Typography variant="body2" color="text.secondary">
          {task.stage?.message ?? "排队中…"}
        </Typography>
      )}

      {task.state === "succeeded" && (
        <Alert severity="success" sx={{ mt: 0.5 }}>
          {task.message}
        </Alert>
      )}

      {task.state === "failed" && d && (
        <Alert severity="error" sx={{ mt: 0.5 }}>
          <Typography sx={{ fontWeight: 650, mb: 0.5 }}>
            {CATEGORY_LABELS[d.category] ?? d.category}
            {d.x_code ? `（X code ${d.x_code}）` : ""}
          </Typography>

          <Typography variant="body2" sx={{ mb: 0.75 }}>
            {d.summary}
          </Typography>

          {d.stage && (
            <Typography variant="caption" color="text.secondary" display="block" sx={{ mb: 0.75 }}>
              出错阶段：{d.stage}
            </Typography>
          )}

          {d.x_message && (
            <Typography
              variant="caption"
              component="pre"
              sx={{
                display: "block",
                m: 0,
                mb: 0.75,
                p: 1,
                borderRadius: 1,
                bgcolor: "action.hover",
                fontFamily: "monospace",
                fontSize: 11.5,
                whiteSpace: "pre-wrap",
                overflowWrap: "anywhere",
              }}
            >
              X: {d.x_message}
            </Typography>
          )}

          <Typography variant="caption" display="block" sx={{ mb: 0.75, overflowWrap: "anywhere" }}>
            原始错误：{d.detail}
          </Typography>

          <Typography variant="body2" sx={{ fontWeight: 600 }}>
            该怎么改：{d.hint}
          </Typography>
        </Alert>
      )}

      {task.stages?.length > 1 && (
        <Box sx={{ mt: 1.5 }}>
          <Typography variant="caption" color="text.secondary" display="block" sx={{ mb: 0.5 }}>
            流程轨迹
          </Typography>
          <Stack spacing={0.25}>
            {task.stages.map((s, i) => (
              <Typography
                key={`${s.at}-${i}`}
                variant="caption"
                sx={{
                  color: s.failed ? "error.main" : "text.secondary",
                  fontFamily: "monospace",
                  fontSize: 11.5,
                }}
              >
                {formatTime(s.at)} · {s.percent}% · {s.message}
              </Typography>
            ))}
          </Stack>
        </Box>
      )}
    </Box>
  );
}