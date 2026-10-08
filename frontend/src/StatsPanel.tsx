import { useCallback, useEffect, useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Collapse,
  Paper,
  Skeleton,
  Stack,
  Typography,
  useMediaQuery,
  useTheme,
  type CssVarsTheme,
} from "@mui/material";
import ExpandMoreRounded from "@mui/icons-material/ExpandMoreRounded";
import QueryStatsRounded from "@mui/icons-material/QueryStatsRounded";
import { stats as fetchStats, type AdminStatsDetail } from "./adminApi";

// 本地这份 visuallyHidden 原来只有 clip: rect(0 0 0 0)。
// 旧式 clip 在现代浏览器里裁剪不可靠：当内部的「最近 30 天」表格
// 比这个 1px 容器高时会把父级撑开。
// 改用 MUI 官方写法：clipPath: inset(50%) 能可靠裁剪。
//
// 但光有 clipPath 还不够。2026-10-08 实测（tools/measure.mjs）：
//
//	table  pos=absolute  top=1493  h=942  bottom=2435
//	body  h=2014     ← 父容器只到这里
//
// 这张无障碍表格高 942px，absolute 定位让它从文档流里被提出来，
// 可它仍以静态位置参与滚动区域计算，于是运维页底部多出 545px 空白。
// contain: "size layout" 能阻止它影响父容器高度，但**不会改变它自身
// 742px 的尺寸**，也不阻止它扩展 scrollHeight —— 实测 contain 确实
// 生效（计算样式里能看到），页面高度却一像素没变。
//
// 折叠按钮上那个 visuallyHidden 的标题不能用 fixed —— 它通过 aria-controls
// 指向折叠内容，需要保持在文档流里正确的位置（虽然视觉上隐藏）。
const visuallyHidden = {
  position: "absolute",
  width: "1px",
  height: "1px",
  maxHeight: "1px",
  p: 0,
  m: "-1px",
  overflow: "hidden",
  clipPath: "inset(50%)",
  whiteSpace: "nowrap",
  border: 0,
} as const;

// 给「最近 30 天」那张无障碍表格专用的隐藏样式。
//
// 为什么不能用上面那份：display:table 的 height/min-height 在 CSS 里是
// "最小高度"，内部 30 行照样能把它顶高 —— 实测 minHeight:0 打进产物也无效。
// display:block 能裁剪，但会让 caption/thead/tr 失去表格语义，而这张表
// 存在的唯一理由就是给屏幕阅读器读。
//
// 所以改用 position:fixed：它完全脱离文档流，不再参与 scrollHeight 计算，
// 也就不会把运维页撑出空白。定位不写 inset，静态位置保持不变，
// 同时保留表格语义给读屏。
//
// 校验：node tools/measure.mjs --page=运维
// 修好后不应再有 h=942 的绝对定位 table，scrollHeight 应收敛到 body 高度。
const hiddenDataTable = {
  position: "fixed",
  top: 0,
  left: 0,
  width: "1px",
  height: "1px",
  p: 0,
  m: "-1px",
  overflow: "hidden",
  clipPath: "inset(50%)",
  whiteSpace: "nowrap",
  border: 0,
} as const;

function StatCard({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}) {
  return (
    <Box
      sx={{ border: 1, borderColor: "divider", borderRadius: 2, p: 2, minWidth: 0 }}
    >
      <Typography variant="caption" color="text.secondary" component="p">
        {label}
      </Typography>
      <Typography variant="h3" component="p" sx={{ mt: 0.25 }}>
        {value}
      </Typography>
      {hint && (
        <Typography variant="caption" color="text.secondary" component="p">
          {hint}
        </Typography>
      )}
    </Box>
  );
}

const series = [
  { key: "created", label: "生成" },
  { key: "redeemed", label: "兑换" },
  { key: "succeeded", label: "成功" },
] as const;

function ActivityChart({ daily }: { daily: AdminStatsDetail["daily"] }) {
  const theme = useTheme<CssVarsTheme>();
  // sx does not map SVG fill/stroke to the palette; use theme vars directly.
  // 三条序列用主色 / 次色 / 成功色区分。这里刻意用 main 而不是 dark：
  // 实测 light.primary.dark 对纸白是 10.77:1（main 只有 6.92），在白底上
  // dark 更重而不是更淡，会让"生成"这一行显得比"兑换"更抢眼。
  // 降低抢眼程度靠几何（柱宽、高度）而不是换更深的色。
  const fills = {
    created: theme.vars.palette.primary.main,
    redeemed: theme.vars.palette.secondary.main,
    succeeded: theme.vars.palette.success.main,
  } as const;
  const rule = theme.vars.palette.divider;
  const totals = daily.reduce(
    (sum, day) => ({
      created: sum.created + day.created,
      redeemed: sum.redeemed + day.redeemed,
      succeeded: sum.succeeded + day.succeeded,
    }),
    { created: 0, redeemed: 0, succeeded: 0 },
  );
  const width = 720;
  const height = 40;
  const slot = width / daily.length;
  const first = daily[0];
  const middle = daily[Math.floor((daily.length - 1) / 2)];
  const last = daily[daily.length - 1];
  const format = (date: string) => date.slice(5).replace("-", "/");
  return (
    <Box>
      <Box
        role="img"
        aria-label={`最近 30 天共生成 ${totals.created} 枚，兑换 ${totals.redeemed} 枚，成功 ${totals.succeeded} 枚。`}
      >
        <Stack spacing={1.5}>
          {series.map((item) => {
            const values = daily.map((day) => day[item.key]);
            const peak = Math.max(1, ...values);
            return (
              <Box key={item.key}>
                <Stack
                  direction="row"
                  alignItems="baseline"
                  spacing={1}
                  sx={{ mb: 0.5 }}
                >
                  <Typography variant="body2" fontWeight={600}>
                    {item.label}
                  </Typography>
                  <Typography variant="caption" color="text.secondary">
                    共 {totals[item.key]}
                  </Typography>
                </Stack>
                <Box
                  component="svg"
                  aria-hidden
                  viewBox={`0 0 ${width} ${height}`}
                  preserveAspectRatio="none"
                  sx={{
                    display: "block",
                    width: "100%",
                    height: { xs: 36, sm: 44 },
                  }}
                >
                  <Box
                    component="line"
                    x1={0}
                    x2={width}
                    y1={height - 0.5}
                    y2={height - 0.5}
                    stroke={rule}
                    strokeWidth={1}
                  />
                  {values.map((value, index) => {
                    if (value === 0) return null;
                    const bar = Math.max(2, (value / peak) * (height - 4));
                    return (
                      <Box
                        component="rect"
                        key={daily[index].date}
                        x={index * slot + slot * 0.3}
                        y={height - bar}
                        // 30 根柱子一行时，柱宽从0.56 收到 0.4：
                        // 留出更多底色，密集时不会连成一片色块。
                        width={slot * 0.4}
                        height={bar}
                        rx={2}
                        fill={fills[item.key]}
                      />
                    );
                  })}
                </Box>
              </Box>
            );
          })}
        </Stack>
        <Box
          sx={{
            display: "grid",
            gridTemplateColumns: "1fr auto 1fr",
            mt: 0.75,
          }}
        >
          <Typography variant="caption" color="text.secondary">
            {format(first.date)}
          </Typography>
          <Typography
            variant="caption"
            color="text.secondary"
            textAlign="center"
          >
            {format(middle.date)}
          </Typography>
          <Typography
            variant="caption"
            color="text.secondary"
            textAlign="right"
          >
            {format(last.date)}
          </Typography>
        </Box>
      </Box>
      <Box component="table" sx={hiddenDataTable}>
        <caption>最近 30 天每日生成、兑换和成功的兑换码数量</caption>
        <thead>
          <tr>
            <th scope="col">日期</th>
            <th scope="col">生成</th>
            <th scope="col">兑换</th>
            <th scope="col">成功</th>
          </tr>
        </thead>
        <tbody>
          {daily.map((day) => (
            <tr key={day.date}>
              <th scope="row">{day.date}</th>
              <td>{day.created}</td>
              <td>{day.redeemed}</td>
              <td>{day.succeeded}</td>
            </tr>
          ))}
        </tbody>
      </Box>
    </Box>
  );
}

export function StatsPanel({ refreshSignal }: { refreshSignal: number }) {
  const [expanded, setExpanded] = useState(true);
  const [data, setData] = useState<AdminStatsDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const sequence = useRef(0);
  const theme = useTheme<CssVarsTheme>();
  const reducedMotion = useMediaQuery("(prefers-reduced-motion: reduce)");
  const load = useCallback(async () => {
    const current = ++sequence.current;
    setLoading(true);
    setError("");
    try {
      const detail = await fetchStats();
      if (current === sequence.current) setData(detail);
    } catch (reason) {
      if (current === sequence.current) setError((reason as Error).message);
    } finally {
      if (current === sequence.current) setLoading(false);
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load, refreshSignal]);

  const redeemedRate =
    data && data.codes.redeemed > 0
      ? `${Math.round(data.rates.redeemed * 100)}%`
      : "—";
  const successRate =
    data && data.codes.redeemed > 0
      ? `${Math.round(data.rates.success * 100)}%`
      : "—";
  const codesTotal = Math.max(1, data?.codes.total ?? 1);
  const stagePeak = Math.max(
    1,
    ...(data?.review_stages ?? []).map((stage) => stage.count),
  );
  // Neutral track color that stays calm on dark surfaces (primary.light does not).
  const remainder = theme.vars.palette.divider;
  return (
    <Paper variant="outlined" sx={{ mb: 3, overflow: "hidden", borderRadius: 2.5 }}>
      {/* 标题栏与 Panel 保持一致的浅底 + 分隔线，避免同页两种风格 */}
      <Box sx={{ bgcolor: "action.hover", borderBottom: expanded ? 1 : 0, borderColor: "divider" }}>
      <Button
        fullWidth
        aria-expanded={expanded}
        aria-controls="stats-panel-content"
        onClick={() => setExpanded((value) => !value)}
        sx={{
          justifyContent: "flex-start",
          gap: 1.25,
          px: { xs: 2, sm: 2.5 },
          py: 1.5,
          borderRadius: 0,
          color: "text.primary",
          "&:hover": { bgcolor: "transparent" },
        }}
      >
        <Box
          aria-hidden="true"
          sx={{
            width: 26, height: 26, borderRadius: 1.25, display: "grid", placeItems: "center",
            bgcolor: "background.paper", color: "primary.main", border: 1, borderColor: "divider",
            "& svg": { fontSize: 16 },
          }}
        >
          <QueryStatsRounded fontSize="inherit" />
        </Box>
        <Typography variant="h2" component="span" sx={{ flexGrow: 1, textAlign: "left", fontSize: 15.5, fontWeight: 650 }}>
          统计概览
        </Typography>
        {data && (
          <Typography variant="body2" color="text.secondary">
            共 {data.codes.total} 枚
          </Typography>
        )}
        <ExpandMoreRounded
          sx={(theme) => ({
            transform: expanded ? "rotate(180deg)" : "none",
            transition: theme.transitions.create("transform"),
            "@media (prefers-reduced-motion: reduce)": { transition: "none" },
          })}
        />
      </Button>
      </Box>
      <Collapse in={expanded} timeout={reducedMotion ? 0 : undefined}>
        <Box
          id="stats-panel-content"
          role="region"
          aria-label="统计概览"
          sx={{ px: { xs: 2.5, sm: 3 }, pb: 3, pt: 1 }}
        >
          {/* 折叠按钮内的标题是 span，补一个隐藏的 h2 让小节 h3 有父级。 */}
          <Typography variant="h2" sx={visuallyHidden}>
            统计概览
          </Typography>
          {loading && (
            <Stack spacing={2} role="status" aria-label="正在加载统计数据">
              <Box
                sx={{
                  display: "grid",
                  gridTemplateColumns: {
                    xs: "repeat(2, 1fr)",
                    sm: "repeat(3, 1fr)",
                  },
                  gap: 1.5,
                }}
              >
                {Array.from({ length: 6 }, (_, index) => (
                  <Skeleton key={index} variant="rounded" height={92} />
                ))}
              </Box>
              <Skeleton variant="rounded" height={160} />
            </Stack>
          )}
          {!loading && error && (
            <Alert severity="error" role="alert">
              {error}
              {data && " 以下保留上次加载的数据。"}
              <Button onClick={() => void load()}>重新加载</Button>
            </Alert>
          )}
          {!loading && !error && data && data.codes.total === 0 && (
            <Typography variant="body2" color="text.secondary">
              还没有兑换码。生成第一批后，这里会显示兑换统计。
            </Typography>
          )}
          {data && data.codes.total > 0 && (
            <Stack spacing={3}>
              <Box
                sx={{
                  display: "grid",
                  gridTemplateColumns: {
                    xs: "repeat(2, 1fr)",
                    sm: "repeat(3, 1fr)",
                  },
                  gap: 1.5,
                }}
              >
                <StatCard label="兑换码总数" value={String(data.codes.total)} />
                <StatCard
                  label="已兑换"
                  value={String(data.codes.redeemed)}
                  hint={`兑换率 ${redeemedRate}`}
                />
                <StatCard
                  label="兑换成功"
                  value={String(data.codes.succeeded)}
                  hint={`成功率 ${successRate}`}
                />
                <StatCard label="处理中" value={String(data.codes.processing)} />
                <StatCard label="待核实" value={String(data.codes.review)} />
                <StatCard label="已停用" value={String(data.codes.revoked)} />
              </Box>
              {data.daily.length > 0 && (
                <Box component="section" aria-labelledby="stats-daily-title">
                  <Typography id="stats-daily-title" variant="h3" sx={{ mb: 1.5 }}>
                    最近 30 天活动
                  </Typography>
                  <ActivityChart daily={data.daily} />
                </Box>
              )}
              {data.months.length > 0 && (
                <Box component="section" aria-labelledby="stats-months-title">
                  <Typography id="stats-months-title" variant="h3" sx={{ mb: 1.5 }}>
                    套餐分布
                  </Typography>
                  <Stack
                    component="ul"
                    direction="row"
                    useFlexGap
                    flexWrap="wrap"
                    gap={2}
                    sx={{ listStyle: "none", m: 0, p: 0, mb: 1.5 }}
                  >
                    <Stack
                      component="li"
                      direction="row"
                      alignItems="center"
                      spacing={0.75}
                    >
                      <Box
                        aria-hidden
                        sx={{
                          width: 10,
                          height: 10,
                          borderRadius: "50%",
                          bgcolor: "success.main",
                        }}
                      />
                      <Typography variant="caption" color="text.secondary">
                        成功
                      </Typography>
                    </Stack>
                    <Stack
                      component="li"
                      direction="row"
                      alignItems="center"
                      spacing={0.75}
                    >
                      <Box
                        aria-hidden
                        sx={{
                          width: 10,
                          height: 10,
                          borderRadius: "50%",
                          bgcolor: remainder,
                        }}
                      />
                      <Typography variant="caption" color="text.secondary">
                        未完成
                      </Typography>
                    </Stack>
                  </Stack>
                  <Stack spacing={1.5}>
                    {data.months.map((entry) => (
                      <Box key={entry.months}>
                        <Stack
                          direction="row"
                          justifyContent="space-between"
                          alignItems="baseline"
                          spacing={1}
                          sx={{ mb: 0.75 }}
                        >
                          <Typography variant="body2" fontWeight={600}>
                            {entry.months} 个月
                          </Typography>
                          <Typography variant="body2" color="text.secondary">
                            共 {entry.total} · 成功 {entry.succeeded}（
                            {Math.round((entry.succeeded / entry.total) * 100)}
                            %）
                          </Typography>
                        </Stack>
                        <Box
                          aria-hidden
                          sx={{
                            display: "flex",
                            width: `${(entry.total / codesTotal) * 100}%`,
                            // 同上：降到 10px 的细条，套餐分布是辅助信息，
                            // 不该和上方的成功数表格抢注意力。
                            height: 10,
                            borderRadius: 999,
                            overflow: "hidden",
                          }}
                        >
                          <Box
                            sx={{
                              width: `${(entry.succeeded / entry.total) * 100}%`,
                              bgcolor: "success.main",
                            }}
                          />
                          <Box sx={{ flex: 1, bgcolor: remainder }} />
                        </Box>
                      </Box>
                    ))}
                  </Stack>
                </Box>
              )}
              {data.review_stages.length > 0 && (
                <Box component="section" aria-labelledby="stats-stages-title">
                  <Typography id="stats-stages-title" variant="h3" sx={{ mb: 1.5 }}>
                    待核实阶段分布
                  </Typography>
                  <Stack spacing={1.5}>
                    {[...data.review_stages]
                      .sort((a, b) => a.progress - b.progress)
                      .map((stage) => (
                        <Stack
                          key={stage.progress}
                          direction="row"
                          alignItems="center"
                          spacing={1.5}
                        >
                          <Typography variant="body2" sx={{ minWidth: 72 }}>
                            阶段 {stage.progress}%
                          </Typography>
                          <Box sx={{ flex: 1 }}>
                            <Box
                              aria-hidden
                              sx={{
                                width: `${(stage.count / stagePeak) * 100}%`,
                                // 条形压到 8px：这组数据的量级只有个位数，
                                // 用 22px 的粗条会让"1 枚"看起来像警报级别的量，
                                // 页面视觉重心被无关紧要的一行抢走。
                                height: 8,
                                borderRadius: 999,
                                bgcolor: "warning.main",
                              }}
                            />
                          </Box>
                          <Typography
                            variant="body2"
                            color="text.secondary"
                            sx={{ minWidth: 40, textAlign: "right" }}
                          >
                            {stage.count} 枚
                          </Typography>
                        </Stack>
                      ))}
                  </Stack>
                  <Typography
                    variant="caption"
                    color="text.secondary"
                    component="p"
                    sx={{ mt: 1 }}
                  >
                    阶段百分比对应兑换流程的进度位置。
                  </Typography>
                </Box>
              )}
            </Stack>
          )}
        </Box>
      </Collapse>
    </Paper>
  );
}
