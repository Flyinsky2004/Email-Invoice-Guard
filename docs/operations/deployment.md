# 部署指南（Debian / Docker）

本文覆盖 Docker Compose 部署与裸机 Debian 部署两种方式。生产环境必须使用 HTTPS。

## 1. 前置条件

- 一台有公网 IP 的 Linux 主机（Debian 12+ 或任意支持 Docker 的发行版）。
- 一个解析到该主机的域名，例如 `invoice.example.com`。
- 放行 80/443（ACME 与 HTTPS）。应用端口 8080 **不要**对外暴露。
- Telegram Bot Token、目标 Chat ID、管理员 User ID；Microsoft 应用及 QQ 授权码按需要配置（见 `docs/setup/`）。

## 2. 生成加密密钥

```bash
openssl rand -base64 32
```

把输出写入 `.env` 的 `ENCRYPTION_KEY`。该密钥用于 AES-256-GCM 加密凭据与会话：

- **必须与数据库分开备份**；丢失密钥将无法解密已保存的凭据。
- 不要提交到 Git，不要写进镜像。

也可以改用密钥文件（权限必须为 `0600`）：

```bash
sudo install -d -m 0750 -o 10001 -g 10001 /etc/invoice-guard
openssl rand -base64 32 | sudo tee /etc/invoice-guard/encryption.key >/dev/null
sudo chown 10001:10001 /etc/invoice-guard/encryption.key
sudo chmod 0600 /etc/invoice-guard/encryption.key
```

然后在 `.env` 中设置 `ENCRYPTION_KEY_FILE=/etc/invoice-guard/encryption.key`，并确保容器可读（只读挂载）。`ENCRYPTION_KEY` 与 `ENCRYPTION_KEY_FILE` 互斥。

## 3. Docker Compose 部署

```bash
umask 077
cp .env.example .env
# 编辑 .env：PUBLIC_BASE_URL、DOMAIN、TELEGRAM_*、ENCRYPTION_KEY 等
docker compose -f deployments/docker-compose.yml up -d --build
docker compose -f deployments/docker-compose.yml ps
```

组成：

| 服务 | 作用 | 网络 |
|---|---|---|
| `app` | 应用本体，非 root（uid 10001），只读根文件系统 | 仅 `backend` |
| `caddy` | TLS 终止、自动证书、反向代理 | `edge` + `backend` |

要点：

- `app` **不发布端口**，只能经 Caddy 访问。
- `backend` 使用固定网段 `10.89.0.0/24`；`TRUSTED_PROXY_CIDRS` 必须与之匹配。
- SQLite 数据位于 `SQLITE_VOLUME_NAME` 指定的卷（默认 `email-invoice-guard_sqlite-data`），挂载到 `/data`，属主为 10001。恢复时可切换到新卷并保留旧卷。
- 健康检查使用镜像内的 `--healthcheck`，无需安装 curl。
- `stop_grace_period: 30s` 与应用内 `SHUTDOWN_TIMEOUT=15s` 配合，保证优雅退出。

### 使用绑定挂载时

若把 `/data` 改为宿主机目录，必须修正属主：

```bash
sudo mkdir -p /srv/invoice-guard/data
sudo chown 10001:10001 /srv/invoice-guard/data
```

## 4. 裸机 Debian 部署

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates tzdata
sudo useradd --system --uid 10001 --home /var/lib/invoice-guard --shell /usr/sbin/nologin invoice-guard

CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ./bin/invoice-guard ./cmd/server
sudo install -m 0755 ./bin/invoice-guard /usr/local/bin/invoice-guard
sudo install -d -o invoice-guard -g invoice-guard -m 0750 /var/lib/invoice-guard
```

`/etc/invoice-guard/env`（权限 `0640`，属主 root:invoice-guard）：

```dotenv
PUBLIC_BASE_URL=https://invoice.example.com
DATABASE_PATH=/var/lib/invoice-guard/invoice-guard.db
LISTEN_ADDRESS=127.0.0.1:8080
TELEGRAM_BOT_TOKEN=...
TELEGRAM_CHAT_ID=...
TELEGRAM_ALLOWED_USER_IDS=...
ENCRYPTION_KEY_FILE=/etc/invoice-guard/encryption.key
TRUSTED_PROXY_CIDRS=127.0.0.1/32
```

systemd 单元 `/etc/systemd/system/invoice-guard.service`：

```ini
[Unit]
Description=Email Invoice Guard
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=invoice-guard
Group=invoice-guard
EnvironmentFile=/etc/invoice-guard/env
ExecStart=/usr/local/bin/invoice-guard
Restart=on-failure
RestartSec=5s
TimeoutStopSec=30s
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
UMask=0077
ReadWritePaths=/var/lib/invoice-guard

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now invoice-guard
sudo systemctl status invoice-guard
```

裸机场景下由本机 Caddy/Nginx 终止 TLS 并转发到 `127.0.0.1:8080`，因此 `TRUSTED_PROXY_CIDRS=127.0.0.1/32`。

## 5. 配置校验与启动行为

- 启动时严格校验配置；任何缺失或非法项都会打印**变量名**（不含值）并以非零码退出。
- `PUBLIC_BASE_URL` 必须是 HTTPS 绝对地址，且不带路径/查询/片段。
- `MICROSOFT_REDIRECT_URL` 必须与 `PUBLIC_BASE_URL` 同源，路径固定为 `/oauth/microsoft/callback`。
- `MICROSOFT_CLIENT_ID` 为空时 Microsoft 接入被禁用，QQ 仍可用。
- 时间解析使用 `TIMEZONE`（默认 `Asia/Taipei`）与 `REMINDER_TIME`（默认 `09:00`）。

## 6. 升级与回滚

```bash
git pull
docker compose -f deployments/docker-compose.yml build app
docker compose -f deployments/docker-compose.yml up -d app
```

升级前先备份（见 `docs/operations/backup-restore.md`）。回滚使用上一镜像标签并恢复对应数据库快照。

## 7. 健康检查

```bash
docker compose -f deployments/docker-compose.yml exec app /app/server --healthcheck
curl -fsS https://invoice.example.com/healthz
```

健康响应只包含 `{"status":"ok"}` 或 `{"status":"unavailable"}`，不泄露任何内部信息。

生产镜像固定为 Go 1.27.4 / Alpine 3.24.4 / Caddy 2.11.9。Docker 构建阶段会在目标 Linux 平台运行不依赖真实凭证的完整测试。密钥文件模式需要将对应文件只读挂载到容器内，并由 uid 10001 读取；默认 Compose 使用环境变量密钥。

Caddy 2.11.9 从固定版本源码使用 Go 1.27.4 重新编译，避免官方同版本镜像内旧 Go 工具链的标准库漏洞。Compose 会同时构建 app 与 caddy；不把邮箱、Bot 或加密密钥环境变量传给反向代理。

安全补丁依据：[GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859)、[Go 1.27.4 发布记录](https://go.dev/doc/devel/release#go1.27.4)。宿主机 Go 较旧且启用自动工具链时，会按 go.mod 下载项目所需 SDK；不修改系统安装。
