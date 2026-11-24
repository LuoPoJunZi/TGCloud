package entry

import "strings"

// Match the managed Python renderer so an existing healthy proxy retains the
// same Compose identity and the Python recovery path can read the same files.
func (entry httpsEntry) render(state httpsState, candidate string) map[string]string {
	domain := state.domain()
	files := map[string]string{
		"nginx/nginx.conf":             httpsNginxTemplate,
		"nginx/conf.d/00-default.conf": httpsDefaultTemplate,
		"docker-compose.yml": strings.NewReplacer(
			"@NGINX@", entry.nginxImage, "@CERTBOT@", entry.certbotImage, "@CERT_NAMES@", domain,
		).Replace(httpsComposeTemplate),
	}
	if domain != "" {
		files["nginx/conf.d/00-default.conf"] += httpsDefaultTLSTemplate
		files["nginx/conf.d/20-clouddrive2.conf"] = strings.ReplaceAll(httpsRouteTemplate, "@DOMAIN@", domain)
	}
	if candidate != "" && candidate != domain {
		files["nginx/conf.d/10-acme-bootstrap.conf"] = strings.ReplaceAll(httpsBootstrapTemplate, "@DOMAIN@", candidate)
	}
	return files
}

const httpsNginxTemplate = `user nginx;
worker_processes auto;
pid /var/run/nginx.pid;
error_log /dev/stderr warn;

events { worker_connections 1024; }

http {
    include /etc/nginx/mime.types;
    default_type application/octet-stream;
    access_log off;
    sendfile on;
    server_tokens off;
    client_max_body_size 16m;
    map $http_upgrade $connection_upgrade {
        default upgrade;
        '' close;
    }
    include /etc/nginx/conf.d/*.conf;
}
`

const httpsDefaultTemplate = `server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name _;
    return 444;
}
`

const httpsDefaultTLSTemplate = `
server {
    listen 443 ssl default_server;
    listen [::]:443 ssl default_server;
    ssl_reject_handshake on;
}
`

const httpsBootstrapTemplate = `server {
    listen 80;
    listen [::]:80;
    server_name @DOMAIN@;
    if ($host != @DOMAIN@) { return 444; }

    location ^~ /.well-known/acme-challenge/ {
        root /var/www/certbot;
        default_type text/plain;
        try_files $uri =404;
    }
    location / { return 404; }
}
`

const httpsRouteTemplate = `server {
    listen 80;
    listen [::]:80;
    server_name @DOMAIN@;
    if ($host != @DOMAIN@) { return 444; }

    location ^~ /.well-known/acme-challenge/ {
        root /var/www/certbot;
        default_type text/plain;
        try_files $uri =404;
    }
    location / { return 308 https://$host$request_uri; }
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name @DOMAIN@;
    if ($host != @DOMAIN@) { return 444; }

    ssl_certificate /etc/letsencrypt/live/@DOMAIN@/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/@DOMAIN@/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_session_cache shared:TGCloudTLS:10m;
    ssl_session_timeout 1d;
    ssl_session_tickets off;

    location = /dav { return 403; }
    location ^~ /dav/ { return 403; }

    location / {
        proxy_pass http://127.0.0.1:19798;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_connect_timeout 10s;
        proxy_send_timeout 60s;
        proxy_read_timeout 120s;
    }
}
`

const httpsComposeTemplate = `services:
  nginx:
    image: @NGINX@
    container_name: tgcloud-proxy-nginx
    network_mode: host
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
    restart: unless-stopped
    read_only: true
    tmpfs:
      - /var/cache/nginx
      - /var/run
      - /tmp
    cap_drop:
      - ALL
    cap_add:
      - CHOWN
      - DAC_OVERRIDE
      - NET_BIND_SERVICE
      - SETGID
      - SETUID
    security_opt:
      - no-new-privileges:true
    labels:
      com.tgcloud.managed: "true"
      com.tgcloud.role: reverse-proxy
    volumes:
      - ./nginx/nginx.conf:/etc/nginx/nginx.conf:ro
      - ./nginx/conf.d:/etc/nginx/conf.d:ro
      - ./certbot/www:/var/www/certbot:ro
      - ./certbot/letsencrypt:/etc/letsencrypt:ro
    command:
      - /bin/sh
      - -c
      - while sleep 21600; do nginx -s reload; done & exec nginx -g 'daemon off;'
  certbot:
    image: @CERTBOT@
    container_name: tgcloud-proxy-certbot
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
    restart: unless-stopped
    read_only: true
    tmpfs:
      - /tmp
    cap_drop:
      - ALL
    security_opt:
      - no-new-privileges:true
    labels:
      com.tgcloud.managed: "true"
      com.tgcloud.role: certificate-manager
    environment:
      TGCLOUD_CERT_NAMES: "@CERT_NAMES@"
    volumes:
      - ./certbot/www:/var/www/certbot
      - ./certbot/letsencrypt:/etc/letsencrypt
      - ./certbot/lib:/var/lib/letsencrypt
      - ./certbot/log:/var/log/letsencrypt
      - ./certbot/status:/status
    entrypoint: /bin/sh
    command:
      - -c
      - umask 077; trap 'exit 0' TERM INT; while :; do for cert in $$TGCLOUD_CERT_NAMES; do attempt=$$(date -u +%s); before=$$(sha256sum "/etc/letsencrypt/live/$$cert/fullchain.pem" 2>/dev/null | cut -d" " -f1); if certbot renew --cert-name "$$cert" --webroot -w /var/www/certbot --quiet >/var/log/letsencrypt/renew-console.log 2>&1; then result=OK; printf "%s\n" "$$attempt" >"/status/$$cert.success"; after=$$(sha256sum "/etc/letsencrypt/live/$$cert/fullchain.pem" 2>/dev/null | cut -d" " -f1); if [ -n "$$before" ] && [ -n "$$after" ] && [ "$$before" != "$$after" ]; then printf "%s\n" "$$attempt" >"/status/$$cert.renewed"; fi; else result=FAILED; fi; printf "ATTEMPT=%s\nRESULT=%s\nNEXT=%s\n" "$$attempt" "$$result" "$$((attempt + 43200))" >"/status/$$cert.check.next"; mv "/status/$$cert.check.next" "/status/$$cert.check"; done; sleep 43200 & wait $$!; done
`
