# SQLite 备份与恢复

数据库启用 WAL。运行时只复制主数据库文件可能漏掉已提交事务。使用内置在线快照命令，或停机复制整个数据卷。密钥与数据库分开保护；备份邮件正文同样属于私有数据。

## 在线快照

在项目根目录执行，命令使用容器已有配置，不打印凭据：

```bash
umask 077
mkdir -p backup
eig_backup_name="guard-$(date -u +%Y%m%dT%H%M%SZ).db"
docker compose -f deployments/docker-compose.yml exec -T app \
  /app/server --backup "/data/$eig_backup_name"
eig_app_container="$(docker compose -f deployments/docker-compose.yml ps -q app)"
docker cp "$eig_app_container:/data/$eig_backup_name" "backup/$eig_backup_name"
chmod 0600 "backup/$eig_backup_name"
```

`--backup` 使用 SQLite `VACUUM INTO`，包含快照时已提交的 WAL 数据，目标权限为 `0600`。已有目标一律拒绝覆盖；失败会清理本次创建的未完成文件。命令默认两分钟超时，成功输出 `backup completed`。容器中的快照可在确认外部副本安全后另行清理。

裸机使用同样的程序与服务配置：

```bash
# 在已加载服务环境、且有数据库读写权限的终端中：
invoice-guard --backup /安全的备份目录/guard-新时间戳.db
invoice-guard --check-db
```

`--check-db` 检查 SQLite 完整性、外键与启用账号的凭据解密，不输出凭据。服务配置仍须有效；缺失授权或错误密钥会返回非零退出码。

## 停机归档

需要保存整个数据目录时：

```bash
umask 077
mkdir -p backup
docker compose -f deployments/docker-compose.yml stop app
docker run --rm \
  -v email-invoice-guard_sqlite-data:/data:ro \
  -v "$PWD/backup:/backup" alpine:3.24.4 \
  sh -ec 'umask 077; tar czf /backup/guard-$(date -u +%Y%m%dT%H%M%SZ).tar.gz -C /data .'
docker compose -f deployments/docker-compose.yml start app
```

使用自定义 `SQLITE_VOLUME_NAME` 时，将命令中的默认卷名替换为实际卷名。应用容器采用非 root 和只读根文件系统，无需在其中安装 SQLite 工具。

## 分开保护密钥

保存数据库快照、原加密密钥及部署配置。`.env` 含 Bot Token 等信息，应加密备份；密钥存于不同位置，保留多代快照。密钥丢失后无法从数据库恢复邮箱凭据。修改密钥不会自动重新加密旧凭据。

## 恢复到新卷

保留原卷，恢复到一个新命名卷，避免覆盖当前数据。以下示例使用在线 `.db` 快照；先将文件名替换为实际备份名。

```bash
docker compose -f deployments/docker-compose.yml stop app
docker volume create email-invoice-guard-restored-20261115
docker run --rm \
  -v email-invoice-guard-restored-20261115:/data \
  -v "$PWD/backup:/backup:ro" alpine:3.24.4 \
  sh -ec 'test ! -e /data/invoice-guard.db; cp /backup/guard-实际时间戳.db /data/invoice-guard.db; chown -R 10001:10001 /data; chmod 0600 /data/invoice-guard.db'
```

若恢复完整 `.tar.gz` 归档，在新卷中解压归档并设置属主，保留归档中的主文件、WAL 与 SHM 为同一组；不要混用不同时间点的文件。

在 `.env` 中将 `SQLITE_VOLUME_NAME` 改为新卷名，并恢复与快照匹配的原加密密钥。然后先检查，再启动：

```bash
docker compose -f deployments/docker-compose.yml run --rm --no-deps app --check-db
docker compose -f deployments/docker-compose.yml up -d --force-recreate app
docker compose -f deployments/docker-compose.yml exec -T app /app/server --healthcheck
```

检查应输出 `database integrity, foreign keys and credential decryption: ok`。检查失败时保留新卷以便排查，并将 `.env` 的卷名切回原卷后重新创建 app。原卷始终保留，不执行清空命令。

恢复后在 Bot 执行 `/status`、`/accounts` 与 `/sync`。同一天的提醒唯一约束、已确认状态和同步游标随快照恢复。过期投递租约按 `UNKNOWN` 重试，属于至少一次语义；恢复较旧快照时，快照之后已在外部发送的消息可能再次发送。

真实部署应演练一次快照、恢复、健康检查、凭据读取、增量同步及提醒恢复。自动化存储测试与端到端测试已覆盖不依赖真实凭证的对应路径。
