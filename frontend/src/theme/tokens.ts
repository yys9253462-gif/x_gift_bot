/**
 * 设计 token —— 视觉系统的单一事实来源。
 *
 * 分工：
 *   colors.json   只放颜色（浅/深两套 M3 tonal），由 appearance.ts 在首屏前
 *                 读取 background.default 同步 theme-color。
 *   tokens.ts     放"颜色之外"的东西：圆角、阴影、控件高度、层级节奏。
 *   theme.ts      把两者组装成 MUI theme，并给各组件写 styleOverrides。
 *
 * 为什么单独一层：Panel / Sidebar / theme 需要共享同一组圆角与阴影，
 * 早前它们各自写字面量（10、14、2.5），改一处就要在三处同步，
 * 久而久之同一种"卡片"出现了三种圆角。这里收敛成常量。
 */

/** 圆角阶梯。不用 MUI 的 shape.borderRadius 全局覆盖，按层级各取一档。 */
export const radius = {
  /** 输入框、按钮、标签等控件 */
  control: 10,
  /** 控件内的更小元素：chip、进度条、状态点容器 */
  chip: 7,
  /** 面板、卡片 */
  panel: 14,
  /** 面板内嵌的小容器 */
  inset: 10,
  /** 圆形（头像、状态点） */
  round: 999,
} as const;

/**
 * 控件高度。40 是可点面积下限（图标按钮必须 ≥40×40），
 * 32 只给表格里的次要行内操作，且这些都带 aria-label 兜底。
 */
export const control = {
  /** 主按钮 / 图标按钮 / 输入框 */
  h: 40,
  /** 紧凑场景（chip、行内小按钮） */
  hSm: 32,
} as const;

/**
 * 阴影。刻意保持中性灰、不带色相 —— 有色阴影在两种模式下都会显脏。
 * 只给"浮在页面底上"的容器用（面板、弹窗、侧栏），
 * 内嵌元素一律靠 surfaceContainer* 的明度差区分，不给阴影。
 */
export const shadow = {
  /**
   * 面板级：刚好把卡片从页面底上"揭"起来，再多就会显得廉价。
   * 深色模式下几乎不可见（深底上的黑阴影），所以深色主要靠边框和明度阶梯。
   */
  panel: "0 1px 2px rgba(16, 24, 38, 0.04), 0 1px 3px rgba(16, 24, 38, 0.06)",
  /** 弹窗 / 抽屉级，比面板略高 */
  popover: "0 4px 12px rgba(16, 24, 38, 0.08), 0 12px 32px rgba(16, 24, 38, 0.10)",
  /** 深色模式：黑阴影在深底上读不出来，换成更明显的黑 + 一点冷调 */
  panelDark: "0 1px 2px rgba(0, 0, 0, 0.40), 0 1px 3px rgba(0, 0, 0, 0.28)",
  popoverDark: "0 8px 24px rgba(0, 0, 0, 0.56), 0 2px 6px rgba(0, 0, 0, 0.40)",
} as const;

/**
 * 页面纵向节奏。9 个页面长度差别很大，靠统一的间距阶梯让长页面不散。
 */
export const rhythm = {
  /** 页面内主要区块之间 */
  section: 3,
  /** 面板头与正文、正文与正文 */
  panel: 2.5,
  /** 表单字段之间 */
  field: 2,
  /** 标签与其控件之间 */
  label: 0.75,
} as const;

/**
 * 字阶。收紧到比MUI 默认更小一档：后台信息密度高，
 * 1rem 的正文在 1440宽里显得松散，反而降低扫读效率。
 */
export const type = {
  /** 页面标题 h1 */
  pageTitle: "1.625rem",
  /** 面板标题 h2 */
  panelTitle: "1.0625rem",
  /** 面板内分节 h3 */
  sectionTitle: "0.8125rem",
  /** 表头 / 元信息 */
  meta: "0.75rem",
} as const;