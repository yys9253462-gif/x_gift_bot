import type { Palette, PaletteOptions } from "@mui/material/styles";

// colors.json 里除MUI 标准键之外的自定义键，需要在这里补类型，
// 否则 theme.vars.palette.outline / surfaceContainerLow 会被 tsc 判为不存在。
//
// 为什么用 "surfaceContainer*" 命名：这是 M3 的容器层级命名，
// 语义上正好对应本项目的三层结构（页面 / 区块 / 内容），不会与 MUI 内置色混淆。
declare module "@mui/material/styles" {
  interface Palette {
    /** 面板内部容器，比 paper 略沉，用于表头与内嵌分组 */
    surfaceContainerLow: string;
    /** 内容层容器 */
    surfaceContainer: string;
    /** 更强的内容层容器（进度条槽、禁用态底色） */
    surfaceContainerHigh: string;
    surfaceContainerHighest: string;
    /** 控件边框：实测 ≥3:1，深浅两套都达标 */
    outline: string;
    /** 更弱的分隔边框，仅装饰用，不承载可点区域边界 */
    outlineVariant: string;
    /**
     * 阴影色。放在 palette 里才能随方案切换成 CSS 变量，
     * 否则只能在 theme.ts 里按 palette.mode 分支（MUI 的 vars 上没有 mode）。
     */
    shadowColor: string;
  }
  interface PaletteOptions {
    surfaceContainerLow?: string;
    surfaceContainer?: string;
    surfaceContainerHigh?: string;
    surfaceContainerHighest?: string;
    outline?: string;
    outlineVariant?: string;
    shadowColor?: string;
  }
}

export {};