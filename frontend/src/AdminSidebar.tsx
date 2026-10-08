import { useState, type ReactElement } from "react";
import { Box, Divider, List, ListItemButton, ListItemIcon, ListItemText, Tooltip, Typography } from "@mui/material";
import ConfirmationNumberOutlined from "@mui/icons-material/ConfirmationNumberOutlined";
import SearchOutlined from "@mui/icons-material/SearchOutlined";
import BuildOutlined from "@mui/icons-material/BuildOutlined";
import SendOutlined from "@mui/icons-material/SendOutlined";
import MenuOpenOutlined from "@mui/icons-material/MenuOpenOutlined";
import MenuOutlined from "@mui/icons-material/MenuOutlined";
import KeyOutlined from "@mui/icons-material/KeyOutlined";
import CreditCardOutlined from "@mui/icons-material/CreditCardOutlined";
import PaymentsOutlined from "@mui/icons-material/PaymentsOutlined";
import SettingsEthernetOutlined from "@mui/icons-material/SettingsEthernetOutlined";
import CloudOutlined from "@mui/icons-material/CloudOutlined";

// 导航按用途分三组：
//   日常  兑换码 / 查询
//   配置  所有需要填写的地方各占一项（原来是挤在一个「站点设置」里）
//   运维  补单、统计、手动付款链接
// 拆开的理由：配置项散在一个长页面里，改一项要滚很久，
// 而且看不出"到底还有哪些没填"。
export type AdminPage =
  | "codes"
  | "lookup"
  | "credentials"
  | "cards"
  | "catalog"
  | "outbounds"
  | "proxy"
  | "gift"
  | "ops";

type NavItem = { key: AdminPage; label: string; hint: string; icon: ReactElement };
type NavGroup = { title?: string; items: NavItem[] };

const GROUPS: NavGroup[] = [
  {
    items: [
      { key: "codes", label: "兑换码", hint: "生成与查看兑换码、管理批次", icon: <ConfirmationNumberOutlined /> },
      { key: "lookup", label: "查询", hint: "按客户或按兑换码查订单", icon: <SearchOutlined /> },
    ],
  },
  {
    title: "配置",
    items: [
      { key: "credentials", label: "X 登录凭据", hint: "auth_token 与 ct0", icon: <KeyOutlined /> },
      { key: "cards", label: "支付卡", hint: "下单用的付款卡池", icon: <CreditCardOutlined /> },
      { key: "catalog", label: "商品与价格", hint: "套餐时长、金额与商品", icon: <PaymentsOutlined /> },
      { key: "outbounds", label: "付款出站", hint: "决定 X 报价的区域", icon: <SettingsEthernetOutlined /> },
      { key: "proxy", label: "查询出口", hint: "账号资格查询的出口", icon: <CloudOutlined /> },
    ],
  },
  {
    items: [
      { key: "gift", label: "立即赠送", hint: "填账号直接赠送并付款", icon: <SendOutlined /> },
    ],
  },
  {
    title: "运维",
    items: [
      { key: "ops", label: "运维", hint: "补单、统计与手动付款链接", icon: <BuildOutlined /> },
    ],
  },
];

const WIDTH = 224;

export function AdminSidebar({
  page,
  onNavigate,
  footNote,
}: {
  page: AdminPage;
  onNavigate: (page: AdminPage) => void;
  footNote?: string;
}) {
  const [open, setOpen] = useState(true);

  return (
    <Box
      sx={{
        width: open ? WIDTH : 68,
        flexShrink: 0,
        position: "sticky",
        top: 16,
        // 用 stretch + minHeight 让侧栏与内容区等高：之前 alignSelf:flex-start
        // 让它按内容收缩，短页面（如查询页）下方会留出一大块空白。
        // maxHeight 保留，导航项变多时仍可内部滚动而不顶出视口。
        alignSelf: "stretch",
        minHeight: { xs: "auto", md: `calc(100vh - 32px)` },
        maxHeight: "calc(100vh - 32px)",
        overflowY: "auto",
        overflowX: "hidden",
        bgcolor: "background.paper",
        border: 1,
        borderColor: "divider",
        borderRadius: 2.5,
        transition: "width .2s cubic-bezier(.4,0,.2,1)",
        display: "flex",
        flexDirection: "column",
      }}
    >
      {/* 品牌区 */}
      <Box
        sx={{
          px: open ? 2 : 0,
          py: 1.75,
          display: "flex",
          alignItems: "center",
          justifyContent: open ? "space-between" : "center",
          gap: 1,
        }}
      >
        {open ? (
          <Box sx={{ display: "flex", alignItems: "center", gap: 1.25, minWidth: 0 }}>
            <Box
              aria-hidden="true"
              sx={{
                width: 32,
                height: 32,
                borderRadius: 1.5,
                display: "grid",
                placeItems: "center",
                flexShrink: 0,
                background: (t: { vars?: { palette?: { primary?: { main?: string }; secondary?: { main?: string } } } }) =>
                  `linear-gradient(135deg, ${t.vars?.palette?.primary?.main ?? "#415F91"}, ${t.vars?.palette?.secondary?.main ?? "#875078"})`,
                // 用 contrastText 而不是写死白色：深色方案下主色是浅蓝，
                // 白字压上去只有 1.7:1，换成深蓝得到 7.6:1。
                color: "primary.contrastText",
                fontWeight: 800,
                fontSize: 15,
                letterSpacing: "-.02em",
              }}
            >
              X
            </Box>
            <Box sx={{ minWidth: 0 }}>
              <Typography sx={{ fontWeight: 700, fontSize: 14.5, lineHeight: 1.2, whiteSpace: "nowrap" }}>
                XGift
              </Typography>
              <Typography sx={{ fontSize: 11.5, color: "text.secondary", lineHeight: 1.3, whiteSpace: "nowrap" }}>
                兑换码管理
              </Typography>
            </Box>
          </Box>
        ) : null}
        <Tooltip title={open ? "收起侧栏" : "展开侧栏"} placement="right">
          <ListItemButton
            onClick={() => setOpen((v) => !v)}
            aria-label={open ? "收起侧栏" : "展开侧栏"}
            sx={{
              minWidth: 32,
              minHeight: 32,
              p: 0,
              borderRadius: 1,
              justifyContent: "center",
              color: "text.secondary",
              "&:hover": { color: "text.primary", bgcolor: "action.hover" },
            }}
          >
            {open ? <MenuOpenOutlined sx={{ fontSize: 18 }} /> : <MenuOutlined sx={{ fontSize: 18 }} />}
          </ListItemButton>
        </Tooltip>
      </Box>
      <Divider />
      <Box sx={{ px: 1, py: 1, flex: 1 }}>
        {GROUPS.map((group, gi) => (
          <Box key={group.title ?? gi} sx={{ mb: gi === GROUPS.length - 1 ? 0 : 0.5 }}>
            {group.title && open && (
              <Typography
                sx={{
                  px: 1.25,
                  pt: gi === 0 ? 0.5 : 1.25,
                  pb: 0.5,
                  fontSize: 10.5,
                  fontWeight: 700,
                  letterSpacing: ".08em",
                  textTransform: "uppercase",
                  color: "text.secondary",
                }}
              >
                {group.title}
              </Typography>
            )}
            {group.title && !open && <Divider sx={{ my: 1 }} />}
            <List disablePadding>
              {group.items.map((it) => {
                const active = it.key === page;
                const button = (
                  <ListItemButton
                    key={it.key}
                    selected={active}
                    onClick={() => onNavigate(it.key)}
                    sx={{
                      position: "relative",
                      borderRadius: 1.5,
                      mb: 0.25,
                      minHeight: 40,
                      justifyContent: open ? "flex-start" : "center",
                      px: open ? 1.25 : 0,
                      color: active ? "primary.main" : "text.secondary",
                      bgcolor: active ? "primary.light" : "transparent",
                      transition: "background-color .12s ease, color .12s ease",
                      "&:hover": {
                        bgcolor: active ? "primary.light" : "action.hover",
                        color: active ? "primary.main" : "text.primary",
                      },
                      "&.Mui-selected": { bgcolor: "primary.light" },
                      "&.Mui-selected:hover": { bgcolor: "primary.light" },
                      "&::before": active
                        ? {
                            content: '""',
                            position: "absolute",
                            left: 0,
                            top: "50%",
                            transform: "translateY(-50%)",
                            width: 3,
                            height: 18,
                            borderRadius: "0 3px 3px 0",
                            bgcolor: "primary.main",
                          }
                        : undefined,
                    }}
                  >
                    <ListItemIcon
                      sx={{
                        minWidth: open ? 32 : 0,
                        justifyContent: "center",
                        color: "inherit",
                        "& svg": { fontSize: 19 },
                      }}
                    >
                      {it.icon}
                    </ListItemIcon>
                    {open && (
                      <ListItemText
                        primary={it.label}
                        slotProps={{
                          primary: { fontSize: 13.5, fontWeight: active ? 650 : 500, lineHeight: 1.3 },
                        }}
                      />
                    )}
                  </ListItemButton>
                );
                return open ? button : <Tooltip key={it.key} title={it.label} placement="right">{button}</Tooltip>;
              })}
            </List>
          </Box>
        ))}
      </Box>
      {open && footNote && (
        <>
          <Divider />
          <Box sx={{ px: 2, py: 1.5, display: "flex", alignItems: "center", gap: 0.75 }}>
            <Box
              aria-hidden="true"
              sx={{ width: 6, height: 6, borderRadius: "50%", bgcolor: "success.main", flexShrink: 0 }}
            />
            <Typography sx={{ fontSize: 11.5, color: "text.secondary", lineHeight: 1.4 }}>
              {footNote}
            </Typography>
          </Box>
        </>
      )}
    </Box>
  );
}
