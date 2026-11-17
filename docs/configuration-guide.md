# TGCloud 配置参考

## 配置入口

优先通过安装向导或管理菜单修改配置。`.env` 由工具按字段读取，不作为 Shell 脚本执行。Telegram 与 WebDAV 的秘密字段使用 Base64 保存；Base64 不提供保密性，文件权限仍应为 `0600`。

| 字段 | 含义 |
| --- | --- |
| `TELEGRAM_API_ID` | Telegram API ID，数字 |
| `TELEGRAM_API_HASH_B64` | API Hash 的 Base64 |
| `BOT_TOKEN_B64` | Bot Token 的 Base64 |
| `ALLOWED_USER_ID` | 管理员 Telegram 数字用户 ID |
| `WEBDAV_URL_B64` | WebDAV 地址的 Base64 |
| `WEBDAV_USERNAME_B64` | 专用用户名的 Base64 |
| `WEBDAV_PASSWORD_B64` | 专用密码的 Base64 |
| `WEBDAV_TARGET_PATH_B64` | 目标相对目录的 Base64；可留空 |
| `LOCAL_TEMP_BUDGET_GB` | 本地任务暂存预算 |
| `MIN_FREE_DISK_GB` | 必须保留的真实可用磁盘空间 |
| `TZ` | 容器时区，默认 `Asia/Shanghai` |
| `TGCLOUD_STORAGE_BACKEND` | 固定为 `clouddrive2` |
| `TGCLOUD_RCLONE_REMOTE_NAME` | 固定为 `cd2` |

以向导生成的字段为准，不把示例秘密放进命令行或公开文件。

## CloudDrive2 与 WebDAV

先在网关后台登录并挂载云存储，再创建专用 WebDAV 用户。Bot 写入账号必须有目标目录的写入权限；外部只读访问应使用另一名只读用户。

容器间默认地址为 `http://tgcloud-clouddrive2:19798/dav`。目标目录相对于该 WebDAV 用户的可见根目录解析，例如 `Telegram/Downloads`；不使用 `/opt/...` 作为云端目录。

## 持久化身份

| 资源 | 默认名称或路径 |
| --- | --- |
| 命令／Python 包 | `tgcloud` |
| 实例目录 | `/opt/tgcloud` |
| Bot 容器 | `tgcloud-clouddrive2-bot` |
| 网关容器 | `tgcloud-clouddrive2` |
| Docker 网络 | `tgcloud-clouddrive2-net` |
| 业务数据库 | `data/tgcloud.db` |
| Bot Session | `data/bot.session` |
| 个人监听 Session | `data/listener/personal.session` |
| rclone 配置 | `config/rclone/rclone.conf` |

不要在运行时移动数据库、替换 Session 或复用其他实例的来源记录。发现其他数据库或孤立旁文件时，工具会停止，避免创建替代空库。

## 资源控制

预算与保留空间必须为大于零的有限数字。Bot 根据 CPU、内存、磁盘和任务反馈调整并发；配置预算不能替代真实空闲空间。`CONTROL_INTERVAL_SECONDS`、CPU 阈值、内存阈值、`MAX_RETRIES` 和 `REMOTE_HEALTH_INTERVAL_SECONDS` 的具体读取见 `src/tgcloud/config.py`，通常保留默认值。

通过[就绪检查和诊断](operations-guide.md)定位配置问题。WebDAV 验收和实际小文件转存都通过后，再提高任务量。
