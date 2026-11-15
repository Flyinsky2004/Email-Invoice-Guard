# 故障排查

## 启动失败

应用启动时会严格校验配置，错误信息只包含**变量名**，不会输出变量值。

| 错误片段 | 原因 | 处理 |
|---|---|---|
| `PUBLIC_BASE_URL is required` | 未设置公网地址 | 填写 HTTPS 绝对地址 |
| `PUBLIC_BASE_URL: https is required` | 使用了 http 或非回环地址 | 改用 HTTPS；本地开发可显式设置 `ALLOW_INSECURE_HTTP=true` |
| `PUBLIC_BASE_URL: must not include a path` | 地址带了路径 | 只保留协议+主机 |
| `ENCRYPTION_KEY or ENCRYPTION_KEY_FILE is required` | 未提供密钥 | 用 `openssl rand -base64 32` 生成 |
| `ENCRYPTION_KEY: must decode to exactly 32 bytes` | 密钥长度不对 | 重新生成 32 字节 base64 |
| `ENCRYPTION_KEY_FILE: permissions must be owner-only (0600)` | 密钥文件权限过宽 | `chmod 600 <file>` |
| `ENCRYPTION_KEY and ENCRYPTION_KEY_FILE are mutually exclusive` | 同时设置了两者 | 只保留一个 |
| `TIMEZONE: unknown location` | 时区名错误 | 使用 IANA 名称，如 `Asia/Shanghai` |
| `REMINDER_TIME: must be HH:MM` | 时间格式错误 | 使用 24 小时制 `09:00` |
| `TRUSTED_PROXY_CIDRS: ... too broad` / `trusts every address` | 代理网段过宽 | 只写具体反代网段 |
| `TRUSTED_PROXY_CIDRS: ... must be a network address` | 写了主机位 | 写成网段地址，如 `10.89.0.0/24` |
| `QQ_ALLOWED_DOMAINS: invalid domain` | 写了 scheme/路径/端口/通配符或非法 DNS 名称 | 只写裸域名，如 `qq.com,my-alias.qq.com` |
| `IMAP_ADDRESS: only imap.qq.com is supported` | 指向了其他 IMAP 主机 | 固定为 `imap.qq.com:993` |
| `IMAP_SERVER_NAME: only imap.qq.com is supported` | 改了 TLS 主机名 | 固定为 `imap.qq.com` |
| `MICROSOFT_REDIRECT_URL: must share the PUBLIC_BASE_URL origin` | 回调与公网地址不同源 | 改为同域名同协议 |
| `LISTEN_ADDRESS: must be host:port` | 监听地址格式错误 | 如 `:8080` 或 `127.0.0.1:8080` |

## 健康检查失败

```bash
docker compose -f deployments/docker-compose.yml exec app /app/server --healthcheck
```

- 退出码非 0：容器内探测失败。检查 `LISTEN_ADDRESS`、`HEALTH_PATH`，或显式设置 `HEALTHCHECK_URL`。
- 返回 `{"status":"unavailable"}`：数据库不可用。查看应用日志中的 `error` 字段。
- 健康响应**不会**包含数据库路径、错误详情或任何密钥。

## 数据库问题

| 现象 | 原因 | 处理 |
|---|---|---|
| `database is locked` | 并发写或外部进程占用 | 确认只有一个实例；SQLite 已启用 `busy_timeout=5000` |
| `unable to open database file` | `/data` 权限或只读挂载 | 属主须为 `10001:10001`，卷可写 |
| 磁盘写满 | WAL 增长 | 清理空间；WAL 会在检查点后回收 |
| 恢复后完整性异常 | 备份不一致 | 用上一代备份重新恢复，见 `backup-restore.md` |

## Telegram 问题

| 现象 | 原因 | 处理 |
|---|---|---|
| `401 Unauthorized` | 令牌错误或已被撤销 | 从 BotFather 重新获取 |
| `409 Conflict` | 同一令牌存在多个轮询实例 | 只运行一个实例 |
| `429 Too Many Requests` | 触发限流 | 程序遵守 `retry_after`，可调大 `TELEGRAM_RATE_INTERVAL` |
| 命令无响应 | 用户 ID 或 Chat ID 不匹配 | 核对 `getUpdates` 中的 `from.id` 与 `chat.id` |
| 消息重复 | 超时导致结果不明后重试 | 至少一次语义的正常表现，无法做到严格恰好一次 |

## QQ / IMAP 问题

| 现象 | 原因 | 处理 |
|---|---|---|
| 接入页提示必须 HTTPS | 直连非 TLS 且来源不在可信代理网段 | 配置 Caddy，或让 `TRUSTED_PROXY_CIDRS` 覆盖反代地址 |
| 授权码登录失败 | 授权码错误或 IMAP 未开启 | 重新生成授权码 |
| 同步报 `ErrReauthorization` | 授权码失效 | 重新 `/add qq` |
| UIDVALIDITY 变化后重新同步 | 邮箱端重建了文件夹 | 属正常行为，会重建该文件夹基线 |

## Microsoft 问题

| 现象 | 原因 | 处理 |
|---|---|---|
| `invalid_grant` | 授权码过期/已用 | 重新 `/add microsoft` |
| 同步报 `ErrReauthorization` | 刷新令牌失效 | 重新授权 |
| 收不到个人邮箱邮件 | 应用账户类型不是 `consumers` | 修改应用注册或 `MICROSOFT_TENANT` |
| 回调 404 | 重定向路径不匹配 | 固定为 `/oauth/microsoft/callback` |

## 提醒与时间

- 提醒按 `TIMEZONE` 的**本地日历日**计算，默认 `Asia/Taipei`，默认时间 `09:00`。
- 容器缺少 `tzdata` 会导致时区加载失败；官方镜像已包含。
- 账单创建当天不提醒；`REVIEW_REQUIRED` 不提醒；同一账单同一天只提醒一次。

## 日志

- 使用 `log/slog` 输出结构化 JSON 到 stderr。
- 日志不包含令牌、授权码、密钥、邮件正文。
- 排查时优先查看 `error`、`account_id`、`outbox_id` 等字段，不要要求用户提供密钥。

```bash
docker compose -f deployments/docker-compose.yml logs -f --tail=200 app
```
