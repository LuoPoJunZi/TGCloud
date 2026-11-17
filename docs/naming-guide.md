# TGCloud 分类与智能命名

## 规则

TGCloud 使用确定性的文字规则处理文件名和 Telegram 说明文字。可读原名优先保留；明确的推广前后缀会被清理；数字、默认名或缺乏标题信息的名称可由可靠的说明标题补全。候选不可靠时保留安全兜底名称，媒体扩展名不会直接由一段说明任意替换。

功能处理单条媒体的文件名和说明文字，不读取评论区、隐藏链接实体或依赖外部 AI 服务。规则评分用于解释选择，不能视作语义识别的置信度。

## 分类和目录

通过 `sudo tgcloud` → “8. 文件分类设置”调整可选分类。监听来源还可设置来源目录与命名规则。先预览路径与文件名，再扩大自动转存范围；文件名规范化不代表远端不存在同名对象。

## 本地预览

在完整源码目录执行：

```bash
python3 -B scripts/run_source.py naming-preview
```

按提示输入原名、媒体扩展名及说明，以单独一行 `.` 结束。也可准备只包含 `file_name`、`caption`、`fallback`、`media_extension` 的 JSON 文件：

```bash
python3 -B scripts/run_source.py naming-preview --input /absolute/sample.json --json
```

预览只处理本地文字，不创建任务、不下载上传、不改已登记记录或云端文件。实际转存仍会进行权限、路径和冲突检查。
