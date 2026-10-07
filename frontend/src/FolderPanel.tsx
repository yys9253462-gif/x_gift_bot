import { useState } from "react";
import {
  Alert,
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  IconButton,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import CreateNewFolderOutlined from "@mui/icons-material/CreateNewFolderOutlined";
import DeleteOutlineOutlined from "@mui/icons-material/DeleteOutlineOutlined";
import EditOutlined from "@mui/icons-material/EditOutlined";
import FolderOutlined from "@mui/icons-material/FolderOutlined";
import { adminApi, type AdminStats, type Folder } from "./adminApi";

type Props = {
  folders: Folder[];
  stats?: AdminStats;
  filter: string;
  disabled: boolean;
  onSelect: (id: string) => void;
  onBusyChange: (value: boolean) => void;
  onChanged: () => Promise<void>;
};

// 名称上限与后端一致：len(name) > 120 字节即拒绝，故按 UTF-8 字节数校验。
const NAME_LIMIT = 120;
function nameError(raw: string) {
  const trimmed = raw.trim();
  if (!trimmed) return "批次名称不能为空。";
  if (new TextEncoder().encode(trimmed).length > NAME_LIMIT)
    return "批次名称最多 120 字节。";
  return "";
}

export function FolderPanel({
  folders,
  stats,
  filter,
  disabled,
  onSelect,
  onBusyChange,
  onChanged,
}: Props) {
  // null=关闭；"new"=新建；Folder=重命名。删除走独立确认框，避免误删。
  const [target, setTarget] = useState<Folder | "new" | null>(null);
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const [removing, setRemoving] = useState<Folder | null>(null);
  const [removeError, setRemoveError] = useState("");
  const [removePending, setRemovePending] = useState(false);

  function openRename(folder: Folder) {
    setTarget(folder);
    setName(folder.name);
    setError("");
  }
  function openNew() {
    setTarget("new");
    setName("");
    setError("");
  }

  async function save() {
    if (!target || pending) return;
    const invalid = nameError(name);
    if (invalid) {
      setError(invalid);
      return;
    }
    setPending(true);
    onBusyChange(true);
    try {
      // adminApi 的第二个参数就是请求体本身，传 undefined 即为 GET。
      // 不要包成 { method, body }——后端收到的字段会全是 undefined。
      if (target === "new") {
        await adminApi("/api/admin/folders", { name: name.trim() });
      } else {
        await adminApi("/api/admin/folders/rename", {
          id: target.id,
          name: name.trim(),
        });
      }
      await onChanged();
      setTarget(null);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setPending(false);
      onBusyChange(false);
    }
  }

  // 后端对文件夹用 ON DELETE SET NULL：删除只解除归类，兑换码本身不会丢。
  async function remove() {
    if (!removing || removePending) return;
    setRemovePending(true);
    onBusyChange(true);
    try {
      await adminApi("/api/admin/folders/delete", { id: removing.id });
      if (filter === removing.id) onSelect("");
      setRemoving(null);
      await onChanged();
    } catch (e) {
      setRemoveError((e as Error).message);
    } finally {
      setRemovePending(false);
      onBusyChange(false);
    }
  }

  return (
    <Box sx={{ mb: 3 }}>
      <Stack
        direction="row"
        justifyContent="space-between"
        alignItems="center"
        sx={{ mb: 1.5, gap: 1, flexWrap: "wrap" }}
      >
        <Typography variant="h2">批次</Typography>
        <Stack direction="row" spacing={1.25} alignItems="center">
          <Typography variant="body2" color="text.secondary">
            {stats?.total ?? "—"} 枚兑换码
          </Typography>
          <Button
            startIcon={<CreateNewFolderOutlined />}
            disabled={disabled}
            onClick={openNew}
            size="small"
            variant="outlined"
            sx={{ whiteSpace: "nowrap" }}
          >
            新建批次
          </Button>
        </Stack>
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        点击批次文件夹查看兑换码，左右滑动可查看全部批次。
      </Typography>
      <Stack
        direction="row"
        useFlexGap
        gap={1}
        sx={{
          minHeight: 80,
          overflowX: "auto",
          alignItems: "center",
          pb: 1,
        }}
        role="region"
        aria-label="批次文件夹，可横向滚动"
        tabIndex={0}
      >
        {[
          { id: "", name: "全部兑换码", count: stats?.total ?? 0 },
          ...(stats?.unfiled
            ? [{ id: "unfiled", name: "未分类", count: stats.unfiled }]
            : []),
          ...folders,
        ].map((folder) => (
          <Box
            key={folder.id}
            sx={{
              display: "flex",
              border: 1,
              borderColor: filter === folder.id ? "primary.main" : "divider",
              borderRadius: 2,
              bgcolor:
                filter === folder.id ? "action.selected" : "background.paper",
              maxWidth: "100%",
              flexShrink: 0,
            }}
          >
            <Button
              disabled={disabled}
              aria-pressed={filter === folder.id}
              startIcon={<FolderOutlined />}
              onClick={() => onSelect(folder.id)}
              sx={{
                px: 2,
                py: 1.5,
                justifyContent: "flex-start",
                textAlign: "left",
                overflowWrap: "anywhere",
                minWidth: 0,
              }}
            >
              {folder.name} · {folder.count}
            </Button>
            {folder.id && folder.id !== "unfiled" && (
              <>
                <Tooltip describeChild title={`重命名批次：${folder.name}`}>
                  <span>
                    <IconButton
                      aria-label={`重命名批次：${folder.name}`}
                      disabled={disabled}
                      onClick={() => openRename(folder)}
                      size="small"
                      sx={{
                        ml: "4px",
                        mr: "2px",
                        my: "10px",
                        width: 40,
                        height: 40,
                        color: "text.secondary",
                      }}
                    >
                      <EditOutlined fontSize="small" />
                    </IconButton>
                  </span>
                </Tooltip>
                <Tooltip describeChild title={`删除批次：${folder.name}`}>
                  <span>
                    <IconButton
                      aria-label={`删除批次：${folder.name}`}
                      disabled={disabled}
                      onClick={() => {
                        setRemoving(folder);
                        setRemoveError("");
                      }}
                      size="small"
                      sx={{
                        ml: "2px",
                        mr: "10px",
                        my: "10px",
                        width: 40,
                        height: 40,
                        // 删除是破坏性操作：常态低调，悬停/聚焦才用红色提示，
                        // 避免整条文件夹带被红色图标淹没，也避免误触时的视觉误导。
                        color: "text.disabled",
                        "&:hover, &:focus-visible": {
                          color: "error.main",
                          bgcolor: "error.light",
                        },
                      }}
                    >
                      <DeleteOutlineOutlined fontSize="small" />
                    </IconButton>
                  </span>
                </Tooltip>
              </>
            )}
          </Box>
        ))}
      </Stack>

      {/* 新建 / 重命名共用一个对话框，标题随模式切换。 */}
      <Dialog
        open={!!target}
        onClose={() => !pending && setTarget(null)}
        fullWidth
        maxWidth="xs"
        aria-labelledby="batch-name-title"
        aria-describedby="batch-name-content"
      >
        <Box
          component="form"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
          <DialogTitle id="batch-name-title">
            {target === "new" ? "新建批次" : "重命名批次"}
          </DialogTitle>
          <DialogContent id="batch-name-content">
            <TextField
              autoFocus
              label="批次名称"
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                if (error) setError("");
              }}
              disabled={pending}
              required
              fullWidth
              error={!!error}
              sx={{ mt: 1 }}
              helperText={
                error ||
                (target === "new"
                  ? "创建后可在「高级筛选」里把兑换码移入该批次"
                  : "该批次内的兑换码会同步更新名称")
              }
            />
          </DialogContent>
          <DialogActions sx={{ p: 2 }}>
            <Button
              variant="outlined"
              disabled={pending}
              onClick={() => setTarget(null)}
            >
              取消
            </Button>
            <Button variant="contained" disabled={pending} type="submit">
              {target === "new" ? "创建" : "保存"}
            </Button>
          </DialogActions>
        </Box>
      </Dialog>

      <Dialog
        open={!!removing}
        onClose={() => !removePending && setRemoving(null)}
        fullWidth
        maxWidth="xs"
        aria-labelledby="batch-delete-title"
        aria-describedby="batch-delete-content"
      >
        <DialogTitle id="batch-delete-title">删除批次</DialogTitle>
        <DialogContent id="batch-delete-content">
          <Alert severity="warning" sx={{ mb: 2 }}>
            删除「{removing?.name}」后，批次内的兑换码会移回「未分类」，
            兑换码本身不会被删除。
          </Alert>
          {removeError && (
            <Alert severity="error" role="alert">
              {removeError}
            </Alert>
          )}
        </DialogContent>
        <DialogActions sx={{ p: 2 }}>
          <Button
            variant="outlined"
            disabled={removePending}
            onClick={() => setRemoving(null)}
          >
            取消
          </Button>
          <Button
            variant="contained"
            color="error"
            disabled={removePending}
            onClick={() => void remove()}
          >
            删除批次
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}