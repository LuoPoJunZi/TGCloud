# Telegram 配置

在安装向导中逐项填写自己的 API ID / API Hash、Bot Token 和管理员 ID，不把真实值写入文档。

## 用户和权限

使用 `tgcloud` → 5. Bot 用户管理。Bot 私聊鉴权与管理员监听授权继续有效，不绕过来源访问限制。

## 授权文件

Bot 与个人监听 Session 分开保存，不能公开或在多个进程同时使用同一 Session。不要在两个实例同时使用同一个 Token。
