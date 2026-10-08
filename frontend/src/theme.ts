import colors from "./colors.json";
import { createTheme } from "@mui/material/styles";
import { control, radius, rhythm, shadow, type } from "./theme/tokens";

// M3 tonal roles，配色见 colors.json。
// 对比度由 frontend/scripts/contrast.mjs 实测守护，不靠目测：
// 正文/次要文字 ≥4.5:1，输入框与控件边框 ≥3:1，两种模式都过。
//
// 视觉系统只保留三层，全站不引入第四层：
//   页面层  background.default    比卡片更沉，让区块"浮"起来
//   区块层  background.paper      面板本体，承载主要内容
//   内容层  surfaceContainer*     面板内部的表头、内嵌卡片、表单分组
// 深色模式不是把浅色反过来：页面底压到接近黑（#0D1117），
// 区块层只亮一档（#161B23），内容层再亮一档（#1B212B）——
// 单向递增的明度阶梯，是深色下不靠边框也能分层的关键。
//
// 阴影颜色放在 colors.json 的 shadowColor 里，这样它是随方案切换的 CSS 变量，
// 而不是在这里写死 rgba 后再按 palette.mode 分支（MUI 的 vars 里没有 palette.mode）。

/**
 * 面板阴影：用 CSS 变量拼装，浅深两套自动切换。
 *
 * 入参是 shadowColor 字符串而不是整个 theme：theme.ts 的 styleOverrides 回调
 * 拿到的 theme 没有 .vars（只有 vars 类型才有），而组件里通过 sx 回调取到的又是
 * 普通 Theme。让调用方自己把 `theme.vars.palette.shadowColor` 传进来，
 * 既不用到处 `as` 强转，产出的 CSS 也仍然是 var()，切换主题即时生效。
 */
const panelShadow = (shadowColor: string) => `0 1px 2px ${shadowColor}`;
const popoverShadow = (shadowColor: string) =>
  `0 4px 12px ${shadowColor}, 0 16px 40px ${shadowColor}`;

export const theme = createTheme({
  cssVariables: { colorSchemeSelector: "data" },
  colorSchemes: {
    light: { palette: colors.light },
    dark: { palette: colors.dark },
  },
  shape: { borderRadius: radius.control },
  spacing: 8,
  typography: {
    // 只用系统字体栈：后台 CSP 是 style-src 'self' 'nonce'，任何外链字体都会被拦。
    fontFamily:
      '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif',
    // 字阶收紧：h1 只给页面标题，h2 给面板，h3 给面板内分节。
    h1: {
      fontSize: type.pageTitle,
      fontWeight: 680,
      lineHeight: 1.22,
      letterSpacing: "-0.022em",
    },
    h2: {
      fontSize: type.panelTitle,
      fontWeight: 650,
      lineHeight: 1.4,
      letterSpacing: "-0.012em",
    },
    h3: {
      fontSize: type.sectionTitle,
      fontWeight: 650,
      lineHeight: 1.45,
      letterSpacing: "0.008em",
    },
    button: { fontWeight: 600, textTransform: "none", letterSpacing: 0 },
    body1: { fontSize: "0.9375rem", lineHeight: 1.7 },
    body2: { fontSize: "0.875rem", lineHeight: 1.62 },
    caption: { fontSize: type.meta, lineHeight: 1.5 },
    overline: {
      fontSize: "0.6875rem",
      fontWeight: 700,
      lineHeight: 1.6,
      letterSpacing: "0.08em",
    },
  },
  components: {
    MuiCssBaseline: {
      styleOverrides: (theme) => ({
        body: {
          minWidth: 320,
          overflowWrap: "anywhere",
          bgcolor: "background.default",
          WebkitFontSmoothing: "antialiased",
        },
        // 焦点可见性兜底。
        //
        // 2026-10-09 的键盘遍历发现 3 个元素聚焦后没有任何视觉变化
        // （批次筛选里的套餐项，以及两个输入框）。逐个补 :focus-visible 规则
        // 治标 —— 以后新增组件还会再漏。所以在这里给一个全局兜底：
        // 任何可聚焦元素只要是"键盘焦点"就必然有轮廓。
        //
        // 用 :focus-visible 而不是 :focus —— 鼠标点击时不该冒出轮廓，
        // 但键盘 Tab 时必须出现，这是 WCAG 2.4.7 的要求。
        //
        // 排除了 disabled：禁用元素不该显示焦点轮廓。
        //
        // 注意这条规则挂在 :root 上，所以子元素要靠 *:focus-visible 显式匹配
        // —— 写 & :focus-visible（后代组合子）只对直接后代生效，
        // 而真正需要的是任意深度（面板里的按钮、表格里的 Chip）。
        // !important 是必要的：MUI 各组件自带 :focus-visible 规则且
        // 注入在组件层，emotion 的注入顺序会让它们赢过这里的基线规则。
        // 兜底规则必须能压过组件，否则等于没加。
        "& *:focus-visible": {
          outline: `2px solid ${theme.vars.palette.primary.main} !important`,
          outlineOffset: "2px !important",
          // MUI 的 ButtonBase 用 box-shadow 做 hover/focus 效果，
          // 不清掉会与轮廓叠在一起，反而看不清焦点在哪。
          "&:not([disabled])": { boxShadow: "none" },
        },
        "*::-webkit-scrollbar": { width: 10, height: 10 },
        "*::-webkit-scrollbar-track": { background: "transparent" },
        "*::-webkit-scrollbar-thumb": {
          backgroundColor: theme.vars.palette.divider,
          borderRadius: 8,
          border: "2px solid transparent",
          backgroundClip: "content-box",
        },
        "*::-webkit-scrollbar-thumb:hover": {
          backgroundColor: theme.vars.palette.outline,
        },
        // 焦点轮廓放在组件样式之后声明，任何 border:none 都盖不掉它。
        ":focus-visible": {
          outline: `3px solid ${theme.vars.palette.primary.main}`,
          outlineOffset: 2,
        },
        "@media (prefers-reduced-motion: reduce)": {
          html: { scrollBehavior: "auto" },
          ".MuiLinearProgress-bar, .MuiTouchRipple-child, .MuiCircularProgress-root":
            { animation: "none", transition: "none" },
        },
      }),
    },

    MuiButton: {
      defaultProps: { disableElevation: true },
      styleOverrides: {
        // 24px 全圆角在表单里显得笨重，降到控件档；高度守住 40 的可点面积。
        root: {
          borderRadius: radius.control,
          minHeight: control.h,
          paddingInline: 18,
          fontWeight: 600,
          transition:
            "background-color .14s ease, border-color .14s ease, color .14s ease",
        },
        sizeSmall: {
          minHeight: control.hSm,
          paddingInline: 13,
          borderRadius: radius.chip + 1,
          fontSize: "0.8125rem",
        },
        // 主按钮常态纯色，悬停才给一层浅投影提示"可点"。
        // 投影色走 shadowColor 变量，不写死 —— 深色下自动换成更实的黑。
        containedPrimary: ({ theme }) => ({
          boxShadow: "none",
          "&:hover": { boxShadow: panelShadow(theme.vars.palette.shadowColor) },
          "&:active": { boxShadow: "none" },
          "&.Mui-disabled": { boxShadow: "none" },
        }),
        contained: {
          "&.Mui-disabled": { boxShadow: "none" },
        },
        // 次要按钮边框用 outline（实测 ≥3:1）而不是 divider：
        // divider 只作装饰分隔，拿来当边框在深色下会让输入框"消失"。
        outlined: ({ theme }) => ({
          borderColor: theme.vars.palette.outline,
          "&:hover": {
            borderColor: theme.vars.palette.primary.main,
            bgcolor: theme.vars.palette.action.hover,
          },
          "&.Mui-disabled": {
            borderColor: theme.vars.palette.divider,
            color: theme.vars.palette.text.disabled,
          },
        }),
        // 描边按钮一律保持中性。
        // MUI 的 Button 默认 color 就是 primary，若给 outlinedPrimary 上主色，
        // 表格里每一行的「补单」都会变成蓝框 —— 100 行时整屏都是蓝点，
        // 真正的主操作（实心按钮）反而被淹掉。克制在这里等于可扫读。
        outlinedPrimary: ({ theme }) => ({
          borderColor: theme.vars.palette.outline,
          color: theme.vars.palette.text.primary,
          "&:hover": {
            borderColor: theme.vars.palette.primary.main,
            bgcolor: theme.vars.palette.action.hover,
            color: theme.vars.palette.primary.main,
          },
        }),
        // 危险按钮：常态只是红字+浅边框，悬停才铺红底。
        // 满屏实心红按钮会让"停用/删除"看起来像主操作。
        outlinedError: ({ theme }) => ({
          borderColor: theme.vars.palette.outline,
          color: theme.vars.palette.error.main,
          "&:hover": {
            borderColor: theme.vars.palette.error.main,
            bgcolor: theme.vars.palette.error.light,
          },
        }),
        text: ({ theme }) => ({
          paddingInline: 12,
          "&:hover": { bgcolor: theme.vars.palette.action.hover },
        }),
      },
    },

    MuiIconButton: {
      styleOverrides: {
        root: ({ theme }) => ({
          // 40×40 是本项目的可点面积下限。
          minWidth: control.h,
          minHeight: control.h,
          borderRadius: radius.control,
          color: theme.vars.palette.text.secondary,
          "&:hover": {
            bgcolor: theme.vars.palette.action.hover,
            color: theme.vars.palette.text.primary,
          },
        }),
        sizeSmall: {
          minWidth: control.hSm,
          minHeight: control.hSm,
          borderRadius: radius.chip + 1,
        },
      },
    },

    MuiPaper: {
      defaultProps: { elevation: 0 },
      styleOverrides: {
        root: { backgroundImage: "none" },
      },
    },

    MuiCard: {
      styleOverrides: {
        root: ({ theme }) => ({
          border: `1px solid ${theme.vars.palette.divider}`,
          borderRadius: radius.panel,
          boxShadow: panelShadow(theme.vars.palette.shadowColor),
        }),
      },
    },

    MuiTextField: { defaultProps: { fullWidth: true, variant: "outlined" } },

    MuiOutlinedInput: {
      styleOverrides: {
        root: ({ theme }) => ({
          borderRadius: radius.control,
          bgcolor: theme.vars.palette.background.paper,
          "& .MuiOutlinedInput-notchedOutline": {
            borderColor: theme.vars.palette.outline,
            transition: "border-color .15s ease",
          },
          "&:hover .MuiOutlinedInput-notchedOutline": {
            borderColor: theme.vars.palette.text.secondary,
          },
          "&.Mui-focused .MuiOutlinedInput-notchedOutline": {
            borderWidth: 2,
            borderColor: theme.vars.palette.primary.main,
          },
          "&.Mui-disabled": {
            bgcolor: theme.vars.palette.surfaceContainerLow,
            color: theme.vars.palette.text.disabled,
          },
        }),
        input: { paddingBlock: 11 },
        inputSizeSmall: { paddingBlock: 8 },
        // 多行输入（Cookie / JSON 粘贴区）行距放开一点更好读。
        inputMultiline: { paddingBlock: 10, lineHeight: 1.6 },
      },
    },

    MuiInputBase: {
      styleOverrides: {
        input: ({ theme }) => ({
          // 占位符是弱信息，但低于 4.5:1 就等于"看不清要填什么"。
          // 不用 opacity 压暗，直接取 secondary（实测两种模式都 ≥6.5:1）。
          "&::placeholder": {
            color: theme.vars.palette.text.secondary,
            opacity: 1,
          },
        }),
      },
    },

    MuiInputLabel: {
      styleOverrides: { root: { fontWeight: 500 } },
    },

    MuiFormHelperText: {
      styleOverrides: {
        root: ({ theme }) => ({
          fontSize: type.meta,
          lineHeight: 1.45,
          marginTop: 5,
          marginInlineStart: 2,
          "&.Mui-disabled": { color: theme.vars.palette.text.disabled },
        }),
      },
    },

    MuiFormControlLabel: {
      styleOverrides: { label: { fontSize: "0.875rem" } },
    },

    MuiCheckbox: {
      styleOverrides: {
        root: ({ theme }) => ({
          padding: 8,
          color: theme.vars.palette.outline,
          "&.Mui-checked": { color: theme.vars.palette.primary.main },
        }),
      },
    },

    MuiStepIcon: {
      styleOverrides: {
        root: ({ theme }) => ({
          color: theme.vars.palette.outline,
          "&.Mui-active": { color: theme.vars.palette.primary.main },
          "&.Mui-completed": { color: theme.vars.palette.success.main },
        }),
        text: ({ theme }) => ({ fill: theme.vars.palette.background.paper }),
      },
    },

    MuiChip: {
      styleOverrides: {
        root: {
          fontWeight: 600,
          maxWidth: "100%",
          height: "auto",
          minHeight: 28,
          borderRadius: radius.chip,
          fontSize: "0.8125rem",
        },
        label: { whiteSpace: "normal", paddingBlock: 3, paddingInline: 10 },
        sizeSmall: { minHeight: 24, fontSize: type.meta },
        // 描边 chip 是后台用得最多的形态（状态标签）。
        // 补一层极淡的同色底，让它在长列表里能被扫到，而不是一条纯线框。
        outlined: ({ theme }) => ({
          borderColor: theme.vars.palette.outlineVariant,
          "&.MuiChip-colorSuccess": {
            borderColor: theme.vars.palette.success.main,
            bgcolor: theme.vars.palette.success.light,
          },
          "&.MuiChip-colorWarning": {
            borderColor: theme.vars.palette.warning.main,
            bgcolor: theme.vars.palette.warning.light,
          },
          "&.MuiChip-colorError": {
            borderColor: theme.vars.palette.error.main,
            bgcolor: theme.vars.palette.error.light,
          },
          "&.MuiChip-colorPrimary": {
            borderColor: theme.vars.palette.primary.main,
            bgcolor: theme.vars.palette.primary.light,
          },
        }),
      },
    },

    MuiAlert: {
      styleOverrides: {
        root: ({ theme }) => ({
          borderRadius: radius.inset,
          alignItems: "flex-start",
          padding: "11px 14px",
          // 语义浅底由 colors.json 的 *.light 提供；再加一条同色左边框，
          // 让错误/警告在长页面里一眼定位。
          borderLeft: `3px solid ${theme.vars.palette.error.main}`,
          "&.MuiAlert-colorSuccess": {
            borderLeftColor: theme.vars.palette.success.main,
          },
          "&.MuiAlert-colorWarning": {
            borderLeftColor: theme.vars.palette.warning.main,
          },
          "&.MuiAlert-colorInfo": {
            borderLeftColor: theme.vars.palette.info.main,
          },
        }),
        message: {
          minWidth: 0,
          overflowWrap: "anywhere",
          padding: 0,
          fontSize: "0.875rem",
          lineHeight: 1.62,
        },
        icon: { paddingTop: 1, marginRight: 10, fontSize: 20 },
      },
    },

    MuiTooltip: {
      styleOverrides: {
        tooltip: ({ theme }) => ({
          fontSize: type.meta,
          borderRadius: radius.chip,
          padding: "6px 10px",
          bgcolor: theme.vars.palette.text.primary,
          color: theme.vars.palette.background.paper,
        }),
        arrow: ({ theme }) => ({ color: theme.vars.palette.text.primary }),
      },
    },

    MuiTable: {
      styleOverrides: { root: { borderCollapse: "separate", borderSpacing: 0 } },
    },
    MuiTableHead: {
      styleOverrides: {
        root: ({ theme }) => ({
          // 表头走"内容层"底色，靠明度差与正文分开，不再叠一条灰线 ——
          // 长表格里少一条线就少一处视觉噪声。
          "& .MuiTableCell-head": {
            fontWeight: 650,
            fontSize: type.meta,
            letterSpacing: "0.045em",
            textTransform: "uppercase",
            color: theme.vars.palette.text.secondary,
            bgcolor: theme.vars.palette.surfaceContainerLow,
            borderBottom: `1px solid ${theme.vars.palette.outlineVariant}`,
            whiteSpace: "nowrap",
          },
        }),
      },
    },
    MuiTableCell: {
      styleOverrides: {
        root: ({ theme }) => ({
          borderBottom: `1px solid ${theme.vars.palette.outlineVariant}`,
          paddingBlock: 11,
          paddingInline: 16,
        }),
        body: { fontSize: "0.875rem" },
        paddingCheckbox: { paddingInline: 8 },
      },
    },
    MuiTableRow: {
      styleOverrides: {
        root: ({ theme }) => ({
          transition: "background-color .12s ease",
          "&:hover": { bgcolor: theme.vars.palette.action.hover },
          "&.Mui-selected": { bgcolor: theme.vars.palette.action.selected },
          "&.Mui-selected:hover": { bgcolor: theme.vars.palette.action.selected },
          "&:last-child .MuiTableCell-root": { borderBottom: 0 },
        }),
      },
    },

    MuiDivider: {
      styleOverrides: {
        root: ({ theme }) => ({ borderColor: theme.vars.palette.divider }),
      },
    },

    MuiLinearProgress: {
      styleOverrides: {
        root: ({ theme }) => ({
          borderRadius: radius.chip,
          height: 6,
          bgcolor: theme.vars.palette.surfaceContainerHigh,
        }),
        bar: { borderRadius: radius.chip },
      },
    },

    MuiSkeleton: {
      styleOverrides: { root: { borderRadius: radius.inset } },
    },

    MuiDialog: {
      styleOverrides: {
        // 弹窗是唯一真正浮在最上层的东西，用最高一档阴影。
        paper: ({ theme }) => ({
          borderRadius: radius.panel,
          boxShadow: popoverShadow(theme.vars.palette.shadowColor),
          border: `1px solid ${theme.vars.palette.outlineVariant}`,
        }),
      },
    },
    MuiDialogTitle: {
      styleOverrides: {
        root: { fontSize: "1.0625rem", fontWeight: 650, paddingBottom: 8 },
      },
    },
    MuiDialogActions: {
      styleOverrides: { root: { padding: 16, gap: 8 } },
    },

    MuiMenu: {
      styleOverrides: {
        paper: ({ theme }) => ({
          borderRadius: radius.inset,
          boxShadow: popoverShadow(theme.vars.palette.shadowColor),
          border: `1px solid ${theme.vars.palette.outlineVariant}`,
          margin: 4,
        }),
      },
    },
    MuiMenuItem: {
      styleOverrides: {
        root: {
          borderRadius: radius.chip,
          marginInline: 6,
          minHeight: 38,
          fontSize: "0.875rem",
        },
      },
    },

    MuiSnackbarContent: {
      styleOverrides: {
        root: ({ theme }) => ({
          borderRadius: radius.inset,
          boxShadow: popoverShadow(theme.vars.palette.shadowColor),
          fontSize: "0.875rem",
        }),
      },
    },

    MuiAccordion: {
      styleOverrides: {
        root: ({ theme }) => ({
          borderRadius: radius.inset,
          border: `1px solid ${theme.vars.palette.divider}`,
          "&::before": { display: "none" },
        }),
      },
    },
  },
});

// 面板纵向节奏收口在这里，9 个页面共用同一份间距约定（见 tokens.ts）。
export const panelRhythm = rhythm;

// 阴影档位对外暴露，供 Panel / Sidebar 复用同一套值。
export const elevation = { panel: panelShadow, popover: popoverShadow, tokens: shadow };