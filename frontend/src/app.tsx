import { Accordion, AccordionDetails, AccordionSummary, Box, Typography } from "@mui/material";
import ExpandMoreRounded from "@mui/icons-material/ExpandMoreRounded";
import { mount, Shell } from "./shared";
import { EligibilityCard } from "./EligibilityCard";

// 前台只提供「检测赠送资格」这一件事：访客填入 X 用户名，
// 由只读接口向 X 核实该账号当前能否接收 Premium 赠送，不消耗任何兑换码。
// 兑换、进度查询、付款链接生成等能力不属于本页，也不再从这里露出入口。
const faq = [
  [
    "应该填写哪个用户名？",
    "打开 X 个人主页，找到 @ 后面的用户名。请勿填写昵称或显示名称。",
  ],
  [
    "检测结果代表什么？",
    "结果表示该账号此刻能否接收 Premium 赠送。X 会随账号状态变化调整判断，因此提交兑换时系统会再核实一次。",
  ],
  [
    "账号暂时无法接收赠送怎么办？",
    "X 会根据账号情况决定是否允许接收 Premium 赠送。可以稍后重新检测；若持续无法接收，请先在该账号上完成 X 要求的前置条件。",
  ],
];

function App() {
  return (
    <Shell maxWidth={520}>
      <EligibilityCard />
      <Box component="section" aria-labelledby="faq-title" sx={{ mt: 4 }}>
        <Typography id="faq-title" variant="h3" component="h2" sx={{ mb: 1 }}>
          常见问题
        </Typography>
        {faq.map(([title, text], index) => (
          <Accordion
            key={title}
            disableGutters
            sx={{
              bgcolor: "transparent",
              borderBottom: 1,
              borderColor: "divider",
              "&:before": { display: "none" },
            }}
          >
            <AccordionSummary
              expandIcon={<ExpandMoreRounded />}
              id={`faq-${index}`}
              aria-controls={`faq-content-${index}`}
              sx={{ px: 0, minHeight: 64 }}
            >
              <Typography fontWeight={500}>{title}</Typography>
            </AccordionSummary>
            <AccordionDetails id={`faq-content-${index}`} sx={{ px: 0, pb: 3 }}>
              <Typography variant="body2" color="text.secondary">
                {text}
              </Typography>
            </AccordionDetails>
          </Accordion>
        ))}
      </Box>
    </Shell>
  );
}

mount(<App />);
