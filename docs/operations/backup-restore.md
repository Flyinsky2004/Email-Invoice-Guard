# 备份与恢复

数据库是 SQLite（WAL 模式）。**不要**在服务运行时直接 `cp` 主数据库文件：WAL 中可能还有未合并的事务，复制结果可能不一致或损坏。

## 1. 需要备份什么

| 内容 | 位置 | 说明 |
|---|---|---|
| SQLite 数据库 | `/data/invoice-guard.db`（含 `-wal`、`-shm`） | 邮件、账单、游标、outbox、加密后的凭据 |
| 加密密钥 | `ENCRYPTION_KEY` 或 `ENCRYPTION_KEY_FILE` | 丢失后无法解密已保存的邮箱凭据 |
| `.env` | 部署目录 | 含令牌等敏感配置，需加密保存 |
| Caddy 证书 | 命名卷 `caddy-data` | 可选；丢失后会自动重新签发 |

**密钥必须与数据库分开存放**：只拿到数据库无法恢复账号；只拿到密钥也无法恢复数据。

## 2. 一致性备份

### 方式 A：停机复制（最稳妥）

```bash
docker compose -f deployments/docker-compose.yml stop app
docker run --rm -v email-invoice-guard_sqlite-data:/data:ro -v "$PWD/backup:/backup" alpine \
  sh -c 'tar czf /backup/guard-$(date +%Y%m%d-%H%M%S).tar.gz -C /data .'
docker compose -f deployments/docker-compose.yml start app
```

### 方式 B：在线备份（WAL 安全）

使用 SQLite 的在线备份 API（`.backup`）可在运行中取得一致快照：

```bash
docker compose -f deployments/docker-compose.yml exec app \
  sh -c 'apk add --no-cache sqlite >/dev/null 2>&1 || true'  # 仅当镜像内无 sqlite3 时
# 若镜像未包含 sqlite3，请在宿主机对只读副本执行，或优先使用方式 A
```

> 应用内部的 `storage.Backup(ctx, destination)` 也提供一致性快照能力；当前版本未暴露为独立 CLI 子命令，因此运维侧推荐方式 A。

### 方式 C：裸机部署

```bash
sudo systemctl stop invoice-guard
sudo tar czf /backup/guard-$(date +%Y%m%d-%H%M%S).tar.gz -C /var/lib/invoice-guard .
sudo systemctl start invoice-guard
```

## 3. 备份密钥

```bash
# 密钥文件方式
sudo install -m 0600 /etc/invoice-guard/encryption.key /backup/encryption.key

# 环境变量方式：从 .env 中提取后单独加密保存
grep '^ENCRYPTION_KEY=' .env > /backup/encryption-key.env
chmod 0600 /backup/encryption-key.env
```

建议对备份整体再做一次加密（如 `age`、`gpg`），并异地保存多代副本。

## 4. 恢复

1. 停止服务：

   ```bash
   docker compose -f deployments/docker-compose.yml stop app
   ```

2. 恢复数据卷内容（先清空目标目录，避免残留 `-wal`/`-shm`）：

   ```bash
   docker run --rm -v email-invoice-guard_sqlite-data:/data -v "$PWD/backup:/backup" alpine \
     sh -c 'rm -rf /data/* && tar xzf /backup/guard-YYYYmmdd-HHMMSS.tar.gz -C /data && chown -R 10001:10001 /data'
   ```

3. 恢复密钥到 `.env` 或密钥文件（权限 `0600`）。

4. 校验完整性：

   ```bash
   docker run --rm -v email-invoice-guard_sqlite-data:/data:ro alpine \
     sh -c 'apk add --no-cache sqlite >/dev/null && sqlite3 /data/invoice-guard.db "PRAGMA integrity_check; PRAGMA foreign_key_check;"'
   ```

   期望输出 `ok` 且无外键错误。若有问题，立即改用上一代备份。

5. 启动并验证：

   ```bash
   docker compose -f deployments/docker-compose.yml start app
   docker compose -f deployments/docker-compose.yml exec app /app/server --healthcheck
   curl -fsS https://<域名>/healthz
   ```

6. 在 Telegram 中执行 `/status` 与 `/sync`，确认账号、游标与投递恢复正常。

## 5. 恢复后的语义

- 同步游标随数据库一起恢复，不会重放历史邮件。
- outbox 中租约已过期的 `SENDING` 任务会被判定为 `UNKNOWN` 并重试，属于**至少一次**语义，极端情况下可能重复投递一条消息。
- 已确认的账单状态保留；提醒的唯一约束 `(invoice_id, reminder_date)` 防止同一天重复提醒。
- 若恢复的是较旧快照，恢复后到快照时间点之间已发送的消息可能被再次投递。

## 6. 恢复演练

定期在非生产环境演练恢复，至少验证：

- `PRAGMA integrity_check` 为 `ok`；
- 服务能通过健康检查；
- `/status` 数量与备份时接近；
- `/sync` 不报 `ErrReauthorization`（若报，需重新授权，说明密钥或凭据不匹配）。
