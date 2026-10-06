import type { ReactNode } from "react";
import { Box, Stack, Typography } from "@mui/material";

// 后台面板的统一外壳。
//
// 统一这里的原因：改版前各面板自己写 Paper + Typography，
// 结果标题层级不一致（有的用 h2、有的只是加粗 div），
// 而且只有部分面板带 <section>，屏幕阅读器无法按区域跳转。
//
// 现在所有面板都经过 Panel：语义固定为 <section aria-labelledby>，
// 标题固定为 h2，页内分节用 h3，层级不再漂移。
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
  return (
    <Box
      component="section"
      aria-labelledby={titleID}
      sx={{
        bgcolor: "background.paper",
        border: 1,
        borderColor: "divider",
        borderRadius: 2.5,
        overflow: "hidden",
        ...sx,
      }}
    >
      {/* 标题栏：浅色底把头部与正文分开，长页面里更好定位 */}
      <Stack
        direction="row"
        alignItems="center"
        spacing={1.25}
        sx={{
          px: { xs: 2, sm: 2.5 },
          py: 1.5,
          bgcolor: "action.hover",
          borderBottom: 1,
          borderColor: "divider",
        }}
      >
        {icon && (
          <Box
            aria-hidden="true"
            sx={{
              width: 26,
              height: 26,
              borderRadius: 1.25,
              display: "grid",
              placeItems: "center",
              flexShrink: 0,
              bgcolor: "background.paper",
              color: "primary.main",
              border: 1,
              borderColor: "divider",
              "& svg": { fontSize: 16 },
            }}
          >
            {icon}
          </Box>
        )}
        <Typography
          id={titleID}
          variant="h2"
          component="h2"
          sx={{ flex: 1, minWidth: 0, fontSize: 15.5, fontWeight: 650 }}
        >
          {title}
        </Typography>
        {actions}
      </Stack>
      <Box sx={{ p: dense ? 0 : { xs: 2, sm: 2.5 } }}>
        {hint && (
          <Typography variant="body2" color="text.secondary" sx={{ mb: 2.5, mt: -0.5 }}>
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
        fontSize: 12,
        fontWeight: 650,
        color: "text.secondary",
        letterSpacing: ".06em",
        textTransform: "uppercase",
        mb: 1.25,
        mt: 0.5,
      }}
    >
      {children}
    </Typography>
  );
}
