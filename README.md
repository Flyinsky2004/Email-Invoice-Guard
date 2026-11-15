# Email Invoice Guard

一个自托管的账单邮件守卫：聚合 Outlook / Hotmail / Live / QQ 邮箱所有启用接收文件夹的新邮件，内置 RFCHOST 账单解析器，通过 Telegram 推送增强通知，并支持人工确认支付状态与到期提醒。程序**从不自动付款**，「已支付」只是用户的手动声明。

- 单进程、模块化单体，数据保存在本地 SQLite。
- 支持 1–10 个独立授权的邮箱账号。
- 邮件、账单、提醒、投递全部持久化，重启可恢复。
- 凭据与会话使用 AES-256-GCM 加密，密钥独立于数据库。

## 功能概览

| 能力 | 说明 |
|---|---|
| 多账号同步 | Microsoft Graph（Outlook/Hotmail/Live）与 QQ IMAP，按账号串行、账号间并行 |
| 首次基线 | 建立历史基线，通知基线前最近 24 小时及初始化期间新增邮件；更早邮件静默入库 |
| 账单解析 | RFCHOST 账单解析，严格整数金额与日历日期校验 |
| 增强通知 | 一条逻辑通知包含明细、金额、到期日、主机与可信链接，超长时安全分片 |
| 人工确认 | 两段式确认「已支付」，支持撤销与取消 |
| 到期提醒 | 按本地日历日每天提醒一次，创建当天与待复核账单不提醒 |
| 资产关联 | 按明确的商家/账号/主机或产品标识关联 VPS 资产，不猜测 |
| 安全接入 | 一次性 HTTPS 链接提交 QQ 授权码，Microsoft OAuth + PKCE |
| 可靠投递 | 持久化 outbox、租约、失败重试、分片消息 ID 记录 |

## 架构

```text
Microsoft Graph / QQ IMAP
        │
        ▼
   Sync（归一化 + 分类 + 解析 + 资产关联）
        │  单个事务
        ▼
  SQLite（邮件 / 账单 / 游标 / outbox / 会话）
        │
        ├── 提醒调度 ──► outbox ──► Telegram 长轮询投递
        └── 支付状态机 ◄── Telegram 按钮回调
```

详细契约见 `docs/architecture.md`、`docs/contracts.md`、`docs/database-schema.md`。

## 一键安装（Linux / systemd）

先将代码推送到 [GitHub 仓库](https://github.com/Flyinsky2004/Email-Invoice-Guard)，发布一个 Release，并等待 Actions 构建完成。然后在 VPS 执行：

```bash
curl -fsSL https://github.com/Flyinsky2004/Email-Invoice-Guard/releases/latest/download/install.sh | sudo bash
```

安装时隐藏询问 **Bot Token**，询问 **信任用户 ID**（逗号分隔），并可选填写公网 HTTPS 地址。默认 Chat ID 是第一个信任用户；先与机器人私聊发送 `/start`。

支持参数：

```bash
curl -fsSL https://github.com/Flyinsky2004/Email-Invoice-Guard/releases/latest/download/install.sh -o install.sh
sudo bash install.sh --public-url https://invoice.example.com --chat-id -1001234567890
sudo bash install.sh --version v1.0.0
bash install.sh --help
```

默认下载最新稳定 Release，自动识别 amd64 / arm64、校验 SHA-256、安装 systemd 服务并设置开机启动。再次执行即升级，保留配置、SQLite 数据和原加密密钥。VPS 不需要 Go 或容器运行环境。

完整安装与 HTTPS 配置见 [部署指南](docs/operations/deployment.md)，发布流程与全部参数见 [Release 指南](docs/operations/releases.md)。

## 本地开发

前置：Go 1.27.4+、`git`。

```bash
umask 077
cp .env.example .env
# 本地可用回环地址；必须显式允许 http：
#   PUBLIC_BASE_URL=http://localhost:8080
#   ALLOW_INSECURE_HTTP=true
#   DATABASE_PATH=./data/invoice-guard.db
#   TRUSTED_PROXY_CIDRS=（留空）

set -a
. ./.env
set +a
# 本地开发使用可写数据目录：
export DATABASE_PATH=./data/invoice-guard.db
go run ./cmd/server
```

常用命令：

```bash
./scripts/verify.sh             # 全量格式检查、vet、test、race、Linux 构建
CGO_ENABLED=0 go build -trimpath -o ./bin/server ./cmd/server
./bin/server --healthcheck
./bin/server --check-db         # 完整性、外键及凭据解密校验
./bin/server --backup ./backup-$(date +%Y%m%d-%H%M%S).db
```

本地 HTTP 可用于开发与健康检查。QQ 授权码页面始终要求 HTTPS；真实接入请配置 TLS 反向代理。自动化测试使用 Mock Graph、TLS IMAP 和 Telegram，不需要真实凭证。

## 配置

所有配置来自环境变量，启动时严格校验，错误信息只包含变量名、不包含变量值。完整示例见 [.env.example](.env.example)。

### 必填

| 变量 | 说明 |
|---|---|
| `PUBLIC_BASE_URL` | 公网 HTTPS 源站，用于生成 QQ 一次性链接；不带路径 |
| `TELEGRAM_BOT_TOKEN` | BotFather 令牌，格式 `<数字>:<密钥>` |
| `TELEGRAM_CHAT_ID` | 投递目标会话（群组为负数） |
| `TELEGRAM_ALLOWED_USER_IDS` | 允许操作的用户 ID，逗号分隔，至少一个 |
| `ENCRYPTION_KEY` 或 `ENCRYPTION_KEY_FILE` | 32 字节随机密钥的 base64；文件方式要求权限 `0600` |

### 常用可选项

| 变量 | 默认值 | 说明 |
|---|---|---|
| `DATABASE_PATH` | `./data/invoice-guard.db` | SQLite 路径（安装器设为 `/var/lib/email-invoice-guard/...`） |
| `LISTEN_ADDRESS` | `:8080` | 监听地址 |
| `HEALTH_PATH` | `/healthz` | 健康检查路径 |
| `TIMEZONE` | `Asia/Taipei` | 提醒使用的 IANA 时区 |
| `REMINDER_TIME` | `09:00` | 每日提醒时间（24 小时制） |
| `SYNC_INTERVAL` | `60s` | 同步间隔 |
| `SHUTDOWN_TIMEOUT` | `15s` | 优雅退出超时 |
| `TELEGRAM_POLL_TIMEOUT` | `25s` | 长轮询超时（须小于 1 分钟） |
| `TELEGRAM_RATE_INTERVAL` | `1s` | 发送节流间隔 |
| `TRUSTED_PROXY_CIDRS` | 空 | 允许声明 `X-Forwarded-Proto: https` 的反代网段 |
| `QQ_ALLOWED_DOMAINS` | `qq.com,foxmail.com` | 允许接入的 QQ 别名域名（裸域名，逗号分隔） |
| `ALLOW_INSECURE_HTTP` | `false` | 仅本地开发：允许回环地址使用 http |

### Microsoft（可选）

`MICROSOFT_CLIENT_ID` 为空即禁用 Microsoft 接入，QQ 不受影响。启用时还需 `MICROSOFT_CLIENT_SECRET` 与 `MICROSOFT_REDIRECT_URL`（与 `PUBLIC_BASE_URL` 同源，路径固定 `/oauth/microsoft/callback`）。

### QQ IMAP

只支持固定主机 `imap.qq.com:993`，使用第三方授权码而非 QQ 密码。默认接受
`@qq.com` 与 `@foxmail.com`；已确认可走 QQ IMAP 的别名域名可通过
`QQ_ALLOWED_DOMAINS` 显式加入白名单（裸域名，禁止 scheme/路径/端口/通配符），
每个别名仍必须通过真实 QQ TLS 登录验证才会保存。详见
[docs/setup/qq-imap.md](docs/setup/qq-imap.md)。

## 账号接入

1. **Telegram**：创建机器人、获取 Chat ID 与用户 ID → [docs/setup/telegram.md](docs/setup/telegram.md)
2. **Microsoft**：Azure 应用注册、回调地址、委托权限 → [docs/setup/microsoft-oauth.md](docs/setup/microsoft-oauth.md)
3. **QQ 邮箱**：开启 IMAP、生成授权码、一次性 HTTPS 链接 → [docs/setup/qq-imap.md](docs/setup/qq-imap.md)

接入入口都在 Telegram 中：`/add microsoft`、`/add qq`。

## Bot 命令

| 命令 | 说明 |
|---|---|
| `/start`、`/help` | 帮助 |
| `/add microsoft` / `/add qq` | 生成一次性接入链接 |
| `/accounts` | 账号列表 |
| `/remove <accountID>` | 停用账号（保留历史账单） |
| `/sync` | 立即同步 |
| `/latest` | 最近邮件 |
| `/search <关键词>` | 搜索邮件 |
| `/invoices`、`/pending`、`/paid`、`/overdue` | 账单视图 |
| `/assets` | 资产列表（可按 provider 过滤） |
| `/providers` | 已启用服务商/解析器 |
| `/status` | 系统状态 |
| `/settings` | 时区、提醒时间、同步间隔、文件夹开关 |

文件夹管理：`/settings folder <accountID>` 列出文件夹名称/ID/启用状态，
再用 `/settings folder <accountID> <folderID> on|off` 切换。接入后新建的文件夹
会在目录刷新时被发现，已有的开关选择会保留。详见
[docs/setup/telegram.md](docs/setup/telegram.md)。

权限：每条命令与按钮回调都同时校验**用户 ID 白名单**与**配置的 Chat ID**，其他会话一律拒绝。

## 状态与人工操作

| 状态 | 含义 |
|---|---|
| `PENDING_CONFIRMATION` | 已解析、等待用户确认 |
| `PAID_CONFIRMED` | 用户手动声明已支付 |
| `REVIEW_REQUIRED` | 信息不足或校验未通过，需人工复核，不发送每日提醒 |
| `CANCELLED` | 已取消 |

- 账单消息中的「已支付」按钮进入二次确认，确认后才写入 `PAID_CONFIRMED`。
- 已确认账单可撤销回 `PENDING_CONFIRMATION`。
- 商家识别只是描述性信息，不代表账单真实性；程序不会自动付款。

## 备份与恢复

- 不要在运行时直接复制 SQLite 主文件（WAL 可能未合并）。
- 使用 `server --backup <新文件路径>` 创建包含已提交 WAL 数据的在线快照，或停机复制整个数据卷。
- **密钥必须与数据库分开备份**；丢失密钥将无法解密邮箱凭据。
- 恢复后执行 `PRAGMA integrity_check`，并确认健康检查与 `/status` 正常。

完整步骤见 [docs/operations/backup-restore.md](docs/operations/backup-restore.md)。

## 故障排查

配置、数据库、Telegram、QQ/IMAP、Microsoft 与时间问题见 [docs/operations/troubleshooting.md](docs/operations/troubleshooting.md)。

## 安全边界与已知限制

- 所有凭据与会话载荷使用 AES-256-GCM 加密，密钥来自环境或 `0600` 文件，绝不入库。
- 日志与错误信息不输出令牌、授权码、密钥或邮件正文。
- 只信任 `TRUSTED_PROXY_CIDRS` 内代理的 `X-Forwarded-Proto`；默认不信任任何转发头，直连必须自带 TLS。
- Telegram 投递为**至少一次**语义：超时导致结果不明时会重试，极端情况下可能重复一条消息。
- 已发送的外部消息无法与本地支付状态原子回滚。
- 不自动下载邮件附件；不执行邮件内容中的任何指令。
- 商家识别不等于账单真实性，用户需自行核对。

## Release 自动构建

发布 Release（`release.published`）后，GitHub Actions 先执行测试，再构建 Linux amd64/arm64 二进制，上传压缩包、`SHA256SUMS` 与 `install.sh`。不使用 Docker。

## 项目结构

```text
cmd/server/                入口：信号处理、slog、--healthcheck
internal/app/              配置加载与运行时组合
internal/httpserver/       HTTP 服务与 OAuth 回调
internal/core/             冻结的领域契约
internal/providers/        Microsoft / IMAP / QQ 接入
internal/sync/             同步流水线
internal/invoice/          解析与支付状态机
internal/reminder/         每日本地日历提醒
internal/assets/           资产关联
internal/storage/          SQLite 仓储与迁移
internal/telegram/         长轮询与消息投递
deployments/               systemd 单元与原生 Caddy 参考配置
docs/setup/                账号接入文档
docs/operations/           部署、备份、排障
tests/e2e/                 端到端测试
```

最终验收结果见 [docs/reports/final-report.md](docs/reports/final-report.md)，架构与各 Worker 报告见 [docs/reports/integration-review.md](docs/reports/integration-review.md)。
