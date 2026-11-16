# CloudDrive2 配置

通过 `tgcloud` 菜单和逐项输入生成配置，不要求普通用户编写 JSON。

## Telegram 与 CloudDrive2

准备自己的 API ID / API Hash、Bot Token 和允许使用的管理员 ID。Bot 私聊鉴权、用户隔离及来源访问检查继续生效。云存储账号、OAuth、Cookie 等仅在自己的 CloudDrive2 中配置。

为 Bot 创建专用 WebDAV 用户，核对账号根目录、目标子目录及写入权限。不要把网关管理账号或 Bot 凭据用于公网只读访问。

## 新项目身份

| 项目 | 默认值 |
| --- | --- |
| Python 包 / 命令 | `tgcloud` |
| 源码版本 | `src/tgcloud/version.py`，0.0.1 |
| 安装目录 | `/opt/tgcloud-clouddrive2` |
| Bot 容器 | `tgcloud-clouddrive2-bot` |
| 网关容器 | `tgcloud-clouddrive2` |
| 网络 | `tgcloud-clouddrive2-net` |
| 数据库（新装） | `data/tgcloud.db` |
| Bot Session | `data/bot.session` |
| 个人监听 Session | `data/listener/personal.session` |
| 后端参数 | `TGCLOUD_STORAGE_BACKEND=clouddrive2` |
| rclone 名称 | `TGCLOUD_RCLONE_REMOTE_NAME=cd2` |

`.env`、数据库、Session、rclone 配置和备份不得提交公共仓库。Base64 是字段编码，不是加密。现有用户数据不因源码改名自动迁移或重命名。

## 磁盘与验证

暂存预算和真实可用空间由预检与运行时共同保护；不要把磁盘保留空间设为零。写入成功后还需大小复验，重要文件应在网盘官方客户端独立确认。
