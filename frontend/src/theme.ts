import colors from "./colors.json";
import { createTheme } from "@mui/material/styles";

// M3 tonal roles: blue primary, pink secondary, neutral gray surfaces.
// Status colors retain their semantic meaning in both schemes.
export const theme = createTheme({
  cssVariables: { colorSchemeSelector: "data" },
  colorSchemes: {
    light: { palette: colors.light },
    dark: { palette: colors.dark },
  },
  shape: { borderRadius: 12 },
  spacing: 8,
  typography: {
    fontFamily:
      '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif',
    // 字号刻度收紧：h1 只在页面标题用，h2 给面板，h3 给面板内分节。
    h1: {
      fontSize: "1.75rem",
      fontWeight: 700,
      lineHeight: 1.25,
      letterSpacing: "-0.02em",
    },
    h2: { fontSize: "1.0625rem", fontWeight: 650, lineHeight: 1.4, letterSpacing: "-0.01em" },
    h3: { fontSize: "0.8125rem", fontWeight: 650, letterSpacing: "0.01em" },
    button: { fontWeight: 600, textTransform: "none", letterSpacing: 0 },
    body1: { lineHeight: 1.7 },
    body2: { lineHeight: 1.6 },
    caption: { lineHeight: 1.5 },
  },
  components: {
    MuiButton: {
      defaultProps: { disableElevation: true },
      styleOverrides: {
        // 24px 全圆角在表单里显得笨重，改为 10px；尺寸保持 40/44 的可点面积。
        root: { borderRadius: 10, minHeight: 40, paddingInline: 18, fontWeight: 600 },
        sizeSmall: { minHeight: 34, paddingInline: 12, borderRadius: 8 },
        containedPrimary: {
          boxShadow: "none",
          "&:hover": { boxShadow: "0 2px 8px rgba(0,0,0,.16)" },
          "&:active": { boxShadow: "none" },
          "&.Mui-disabled": { boxShadow: "none" },
        },
        outlined: ({ theme }) => ({
          borderColor: theme.vars.palette.divider,
          "&:hover": { borderColor: theme.vars.palette.primary.main },
        }),
      },
    },
    MuiIconButton: {
      styleOverrides: {
        root: {
          minWidth: 38,
          minHeight: 38,
          borderRadius: 10,
          "&:hover": { bgcolor: "action.hover" },
        },
      },
    },
    MuiPaper: {
      defaultProps: { elevation: 0 },
      styleOverrides: { root: { backgroundImage: "none" } },
    },
    MuiCard: {
      styleOverrides: {
        root: ({ theme }) => ({
          border: `1px solid ${theme.vars.palette.divider}`,
          borderRadius: 14,
        }),
      },
    },
    MuiTextField: { defaultProps: { fullWidth: true, variant: "outlined" } },
    MuiOutlinedInput: {
      styleOverrides: {
        root: ({ theme }) => ({
          borderRadius: 10,
          // 默认边框比 divider 略深一点，聚焦时才跳到主色，落差更清晰
          "& .MuiOutlinedInput-notchedOutline": {
            borderColor: theme.vars.palette.divider,
            transition: "border-color .15s ease",
          },
          "&:hover .MuiOutlinedInput-notchedOutline": {
            borderColor: theme.vars.palette.text.secondary,
          },
          "&.Mui-focused .MuiOutlinedInput-notchedOutline": {
            borderWidth: 2,
            borderColor: theme.vars.palette.primary.main,
          },
        }),
        input: { paddingBlock: 11 },
        inputSizeSmall: { paddingBlock: 8 },
      },
    },
    MuiInputBase: {
      styleOverrides: {
        input: ({ theme }) => ({
          "&::placeholder": {
            color: theme.vars.palette.text.secondary,
            opacity: 0.7,
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
          fontSize: ".75rem",
          marginTop: 4,
          "&.Mui-disabled": { color: theme.vars.palette.text.secondary },
        }),
      },
    },
    MuiStepIcon: {
      styleOverrides: {
        root: ({ theme }) => ({ color: theme.vars.palette.text.secondary }),
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
          borderRadius: 8,
          fontSize: ".8125rem",
        },
        label: { whiteSpace: "normal", paddingBlock: 3, paddingInline: 10 },
        sizeSmall: { minHeight: 24, fontSize: ".75rem" },
      },
    },
    MuiAlert: {
      styleOverrides: {
        root: { borderRadius: 10, alignItems: "flex-start", padding: "10px 14px" },
        message: { minWidth: 0, overflowWrap: "anywhere", padding: 0, fontSize: ".875rem" },
        icon: { paddingTop: 1, marginRight: 10 },
      },
    },
    MuiTooltip: {
      styleOverrides: {
        tooltip: ({ theme }) => ({
          fontSize: ".75rem",
          borderRadius: 8,
          padding: "6px 10px",
          bgcolor: theme.vars.palette.text.primary,
          color: theme.vars.palette.background.paper,
        }),
      },
    },
    MuiTable: {
      styleOverrides: { root: { borderCollapse: "separate", borderSpacing: 0 } },
    },
    MuiTableHead: {
      styleOverrides: {
        root: ({ theme }) => ({
          "& .MuiTableCell-head": {
            fontWeight: 650,
            fontSize: ".75rem",
            letterSpacing: ".03em",
            textTransform: "uppercase",
            color: theme.vars.palette.text.secondary,
            bgcolor: "action.hover",
            borderBottom: `1px solid ${theme.vars.palette.divider}`,
            whiteSpace: "nowrap",
          },
        }),
      },
    },
    MuiTableCell: {
      styleOverrides: {
        root: ({ theme }) => ({
          borderBottom: `1px solid ${theme.vars.palette.divider}`,
          paddingBlock: 10,
          paddingInline: 16,
        }),
        body: { fontSize: ".875rem" },
      },
    },
    MuiTableRow: {
      styleOverrides: {
        root: {
          transition: "background-color .12s ease",
          "&:hover": { bgcolor: "action.hover" },
          "&:last-child .MuiTableCell-root": { borderBottom: 0 },
        },
      },
    },
    MuiDivider: {
      styleOverrides: { root: ({ theme }) => ({ borderColor: theme.vars.palette.divider }) },
    },
    MuiCssBaseline: {
      styleOverrides: (theme) => ({
        body: { minWidth: 320, overflowWrap: "anywhere" },
        // 细滚动条，避免默认样式打断视觉
        "*::-webkit-scrollbar": { width: 10, height: 10 },
        "*::-webkit-scrollbar-track": { background: "transparent" },
        "*::-webkit-scrollbar-thumb": {
          backgroundColor: theme.vars.palette.divider,
          borderRadius: 8,
          border: "2px solid transparent",
          backgroundClip: "content-box",
        },
        "*::-webkit-scrollbar-thumb:hover": {
          backgroundColor: theme.vars.palette.text.secondary,
        },
        ":focus-visible": {
          outline: `3px solid ${theme.vars.palette.primary.main}`,
          outlineOffset: 3,
        },
        "@media (prefers-reduced-motion: reduce)": {
          html: { scrollBehavior: "auto" },
          ".MuiLinearProgress-bar, .MuiTouchRipple-child, .MuiCircularProgress-root":
            { animation: "none", transition: "none" },
        },
      }),
    },
  },
});
