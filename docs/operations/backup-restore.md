# systemd 部署的备份与恢复

SQLite 使用 WAL，运行时不要只复制主文件。密钥与数据库应分开保管；不要在升级或恢复时生成替代密钥。

## 在线快照

通过 systemd 读取服务的环境文件，避免在终端输出或 source 含秘密的配置：

```bash
sudo systemd-run --wait --pipe --collect \
  -p User=email-invoice-guard -p Group=email-invoice-guard \
  -p EnvironmentFile=/etc/email-invoice-guard/env \
  /usr/local/lib/email-invoice-guard/email-invoice-guard \
  --backup /var/lib/email-invoice-guard/backup-新时间戳.db
```

`--backup` 创建包含已提交 WAL 数据的一致快照，权限 `0600`。已有目标会拒绝覆盖。将该快照复制到安全位置，另行保存原密钥与环境配置。自定义安装路径/用户时替换命令参数。

## 停机备份

```bash
sudo systemctl stop email-invoice-guard
sudo tar czf /安全目录/guard-新时间戳.tar.gz -C /var/lib/email-invoice-guard .
sudo chmod 0600 /安全目录/guard-新时间戳.tar.gz
sudo systemctl start email-invoice-guard
```

整个数据目录应作为同一个时间点归档，不能混用不同时间的主文件、WAL 和 SHM。

## 恢复

1. 停止服务，保留原数据目录和原配置的备份。
2. 将一致快照恢复到一个新目录，如 `/var/lib/email-invoice-guard-restored/invoice-guard.db`，设置服务用户属主与私有权限。不要覆盖尚未备份的旧目录。
3. 将配置的 `DATABASE_PATH` 改为新文件，并更新单元的 `WorkingDirectory` / `ReadWritePaths` 指向新目录。恢复匹配的原加密密钥。
4. 使用同样的 `systemd-run` 命令将 `--backup ...` 换成 `--check-db`，检查 SQLite 完整性、外键和凭据解密。
5. `systemctl daemon-reload` 后启动服务，检查 `/status`、`/accounts` 与手动同步。

检查失败时保留新目录，切回原配置与原数据目录。升级回退只恢复程序文件，不自动回滚数据库。

恢复较旧快照后，快照之后已发送的 Telegram 消息可能再次投递。确认状态、同步游标与每日提醒唯一记录随快照恢复。超时状态未知的任务按至少一次语义重试。
