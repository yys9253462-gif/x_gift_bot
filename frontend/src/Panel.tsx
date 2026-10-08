import type { ReactNode } from "react";
import { Box, Stack, Typography, useTheme } from "@mui/material";
import type { CssVarsTheme } from "@mui/material/styles";
import { elevation } from "./theme";
import { radius, rhythm } from "./theme/tokens";

// 后台面板的统一外壳。
//
// 统一这里的原因：改版前各面板自己写 Paper + Typography，
// 结果标题层级不一致（有的用 h2、有的只是加粗 div），
// 而且只有部分面板带<section>，屏幕阅读器无法按区域跳转。
//
// 现在所有面板都经过 Panel：语义固定为 <section aria-labelledby>，
// 标题固定为 h2，页内分节用 h3，层级不再漂移。
//
// 质感上的三层做法（对应 theme.ts 的三层结构）：
//   区块层  paper 底 + outlineVariant 边框 + 一层很浅的阴影 → 从页面底浮起来
//   面板头  surfaceContainerLow 底，与正文靠明度差分开，不靠灰线堆砌
//   图标位primary 淡底 + 主色字，比"白底+灰框"更轻，也不需要边框
export function Panel({
  id,
  title,
  hint,
  icon,
  actions,
  children,
  dense = false,
  sx,
}: {
  /** 面板锚点，同时用于 aria-labelledby */
  id: string;
  title: string;
  hint?: ReactNode;
  icon?: ReactNode;
  /** 标题右侧的操作区（按钮、状态标签等） */
  actions?: ReactNode;
  children: ReactNode;
  /** 紧凑模式：用于列表、表格这类自带留白的内容 */
  dense?: boolean;
  sx?: object;
}) {
  const titleID = `${id}-title`;
  //阴影色取自 palette 的 shadowColor（随深浅方案切换的 CSS 变量）。
  const theme = useTheme<CssVarsTheme>();
  return (
    <Box
      component="section"
      aria-labelledby={titleID}
      sx={{
        bgcolor: "background.paper",
        border: 1,
        borderColor: "outlineVariant",
        borderRadius: `${radius.panel}px`,
        boxShadow: elevation.panel(theme.vars.palette.shadowColor),
        overflow: "hidden",
        // 相邻面板同处一个 flex/grid 容器时，避免阴影被下一块的底色盖住。
        position: "relative",
        ...sx,
      }}
    >
      {/* 面板头：内容层底色把头与正文分开，长页面里更好定位。
          这里用 surfaceContainerLow 而不是 action.hover ——
          action.hover 是叠在 paper 上的半透明层，落到页面底上会偏色。 */}
      <Stack
        direction="row"
        alignItems="center"
        spacing={1.25}
        sx={{
          px: { xs: 2, sm: 2.5 },
          py: 1.4,
          bgcolor: "surfaceContainerLow",
          borderBottom: 1,
          borderColor: "outlineVariant",
        }}
      >
        {icon && (
          <Box
            aria-hidden="true"
            sx={{
              width: 28,
              height: 28,
              borderRadius: `${radius.chip + 1}px`,
              display: "grid",
              placeItems: "center",
              flexShrink: 0,
              // 主色淡底 + 主色字：比"白底+灰框"轻一档，也不需要边框去界定。
              bgcolor: "primary.light",
              color: "primary.main",
              "& svg": { fontSize: 17 },
            }}
          >
            {icon}
          </Box>
        )}
        <Typography
          id={titleID}
          variant="h2"
          component="h2"
          sx={{ flex: 1, minWidth: 0 }}
        >
          {title}
        </Typography>
        {actions}
      </Stack>
      <Box sx={{ p: dense ? 0 : { xs: 2, sm: rhythm.panel } }}>
        {hint && (
          <Typography
            variant="body2"
            color="text.secondary"
            sx={{ mb: rhythm.panel, mt: -0.25 }}
          >
            {hint}
          </Typography>
        )}
        {children}
      </Box>
    </Box>
  );
}

/** 面板内的次级分组标题 */
export function SubHeading({ children, id }: { children: ReactNode; id?: string }) {
  return (
    <Typography
      id={id}
      variant="h3"
      component="h3"
      sx={{
        color: "text.secondary",
        letterSpacing: "0.07em",
        textTransform: "uppercase",
        mb: 1.25,
        mt: 0.5,
      }}
    >
      {children}
    </Typography>
  );
}

/**
 * 空状态：面板还没有结果时占位，给出下一步该做什么。
 *
 * 之前短页面（如「查询」）下方是整片空白，看不出是"还没查"还是"加载中"；
 * 有了这个占位，未查询与无结果两种情形都能被明确表达。
 */
export function EmptyState({
  icon,
  title,
  hint,
  action,
}: {
  icon?: ReactNode;
  title: string;
  hint?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <Stack
      alignItems="center"
      spacing={1.25}
      // role=status 让筛选结果"从无到有"的切换能被读屏播报
      role="status"
      sx={{
        py: { xs: 3, sm: 4 },
        px: 2,
        textAlign: "center",
        // 虚线框表示"这里会放东西但现在没有"，与实线的区块层区分开。
        border: 1,
        borderStyle: "dashed",
        borderColor: "outlineVariant",
        borderRadius: `${radius.inset}px`,
        bgcolor: "surfaceContainerLow",
      }}
    >
      {icon && (
        <Box
          aria-hidden="true"
          sx={{
            width: 40,
            height: 40,
            borderRadius: "50%",
            display: "grid",
            placeItems: "center",
            color: "text.secondary",
            bgcolor: "surfaceContainerHigh",
            "& svg": { fontSize: 20 },
          }}
        >
          {icon}
        </Box>
      )}
      <Typography sx={{ fontWeight: 650, fontSize: 14 }}>{title}</Typography>
      {hint && (
        <Typography variant="body2" color="text.secondary" sx={{ maxWidth: 460 }}>
          {hint}
        </Typography>
      )}
      {action}
    </Stack>
  );
}