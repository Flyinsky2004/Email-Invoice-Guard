# Telegram 机器人配置

机器人使用**长轮询**（getUpdates）接收消息与按钮回调，不使用 webhook，因此无需公网回调地址；但 QQ 接入页仍需要 `PUBLIC_BASE_URL` 指向可访问的 HTTPS 站点。

## 1. 创建机器人

1. 在 Telegram 中搜索 **@BotFather**，发送 `/newbot`。
2. 按提示设置名称与用户名，完成后获得令牌，形如 `123456789:AA...`。
3. 写入 `.env`：

   ```dotenv
   TELEGRAM_BOT_TOKEN=123456789:AA...
   ```

## 2. 获取 Chat ID 与用户 ID

1. 将机器人拉入目标群组（或直接私聊）。
2. 在群内发送任意消息，然后访问：

   ```
   https://api.telegram.org/bot<TOKEN>/getUpdates
   ```

   在返回 JSON 中找到 `chat.id`（群组通常为负数）和发送者 `from.id`。
3. 写入配置：

   ```dotenv
   TELEGRAM_CHAT_ID=-1001234567890
   TELEGRAM_ALLOWED_USER_IDS=111111111,222222222
   ```

## 3. 权限模型

- 每条命令与按钮回调都必须**同时**满足：
  - 发送者用户 ID 在 `TELEGRAM_ALLOWED_USER_IDS` 中；
  - 会话 `chat.id` 等于 `TELEGRAM_CHAT_ID`。
- 任一不满足都会被拒绝；被拒绝的回调也会应答，避免客户端一直转圈。
- 机器人不会在聊天中索取或回显任何密码、授权码、令牌。

## 4. 机器人命令

| 命令 | 说明 |
|---|---|
| `/start`、`/help` | 帮助与命令列表 |
| `/add microsoft` | 生成 Microsoft 授权链接 |
| `/add qq` | 生成 QQ 一次性接入链接 |
| `/accounts` | 列出邮箱账号及状态 |
| `/remove <accountID>` | 停用账号（保留历史账单） |
| `/sync` | 立即同步全部账号 |
| `/latest [n]` | 最近邮件 |
| `/search <关键词>` | 搜索邮件 |
| `/invoices` | 账单列表 |
| `/pending` | 待确认账单 |
| `/paid` | 已确认账单 |
| `/overdue` | 已逾期账单 |
| `/assets` | 资产（VPS）列表，支持按 provider 过滤 |
| `/providers` | 已启用的解析器/服务商 |
| `/status` | 系统状态（账号、邮件、待办数量） |
| `/settings` | 查看/修改时区、提醒时间、同步间隔、文件夹开关 |

### 动态设置

- `/settings timezone <IANA 时区>`，例如 `Asia/Shanghai`
- `/settings reminder <HH:MM>`，例如 `09:30`
- `/settings sync <秒>`，例如 `120`
- `/settings folder <accountID>`：列出该账号的文件夹名称、文件夹 ID 与启用状态
- `/settings folder <accountID> <folderID> on|off`：开启或关闭指定文件夹的同步

文件夹管理完整流程：

1. 先用 `/accounts` 找到账号 ID。
2. 发送 `/settings folder <accountID>`，机器人返回文件夹列表，例如：

   ```text
   INBOX            inbox-folder-id      on
   收件箱副本        custom-folder-id     off
   ```

3. 复制目标文件夹 ID，发送 `/settings folder <accountID> <folderID> on` 开启，
   或 `... off` 关闭。
4. 默认只启用 Inbox；其他文件夹需要显式开启后才会同步。

账号接入后新建的文件夹会在下一次目录刷新时被发现，机器人会保留你已有的
开启/关闭选择；已启用的文件夹继续同步，未启用的保持关闭。

设置写入数据库后立即生效：提醒调度在每次 Tick 读取时区与提醒时间，同步循环在
每轮之间读取同步间隔，文件夹开关在下一轮目录刷新后生效。

## 5. 账单交互

- 账单以**一条增强消息**投递，包含明细、金额、到期日、主机与可信的商家链接。
- 点击「已支付」只是进入**二次确认**；再次确认后才写入 `PAID_CONFIRMED`。
- 已确认账单可「撤销」，回到 `PENDING_CONFIRMATION`。
- 提醒：到期前按本地日历日每天提醒一次；账单创建当天不提醒；`REVIEW_REQUIRED` 不提醒。

## 6. 投递语义与限制

- 出站消息持久化在 outbox，失败会按退避重试。
- 网络超时导致结果不明时标记为 `UNKNOWN` 并重试，属于**至少一次**投递；极端情况下可能重复一条消息。
- 每个分片单独记录 Telegram 消息 ID，重试时跳过已成功的分片。
- 已发送的外部消息无法与本地支付状态原子回滚，这是实际存在的限制。

## 7. 常见问题

| 现象 | 原因 | 处理 |
|---|---|---|
| 启动报 `TELEGRAM_BOT_TOKEN` 格式错误 | 令牌不完整 | 从 BotFather 重新复制 |
| 命令无响应 | Chat ID 或用户 ID 不匹配 | 核对 `getUpdates` 中的值 |
| 429 Too Many Requests | 触发限流 | 调大 `TELEGRAM_RATE_INTERVAL`，程序会遵守 `retry_after` |
| 收不到消息 | 机器人被移出群组或被禁言 | 检查群成员与权限 |
