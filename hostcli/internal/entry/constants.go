package entry

import "errors"

const (
	HTTPSRoot           = "/opt/tgcloud-proxy"
	HTTPSProject        = "tgcloud-proxy"
	HTTPSNginx          = "tgcloud-proxy-nginx"
	HTTPSCertbot        = "tgcloud-proxy-certbot"
	DefaultNginxImage   = "nginx:1.30.5-alpine"
	DefaultCertbotImage = "certbot/certbot:v5.8.0"
)

var ErrExternalProxyMode = errors.New("当前为外部 Docker Nginx 模式，请使用外部代理维护入口执行续期演练")
