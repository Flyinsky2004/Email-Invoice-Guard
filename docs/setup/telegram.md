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

`/start` 或 `/menu` 打开交互式菜单：所有列表与操作都在同一条消息内通过按钮
完成，使用「返回 / 主页」在同一气泡中导航。斜杠命令仍可作为快捷方式。

| 命令 | 说明 |
|---|---|
| `/start`、`/menu` | 打开交互式菜单 |
| `/help` | 菜单式帮助（「命令列表」按钮查看完整命令） |
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
| `/reminder_add` | 向导式创建手动周期提醒 |
| `/reminders` | 手动计划列表 |
| `/reminder <id>` | 查看单个计划及其待确认周期 |
| `/reminder_edit <id>` | 向导式修改计划 |
| `/reminder_pause <id>` | 暂停计划（不产生新周期与汇总） |
| `/reminder_resume <id>` | 恢复计划（从下一个账单日开始） |
| `/reminder_delete <id>` | 取消计划并跳过排队的汇总 |
| `/reminder_paid <occurrenceID>` | 确认某个周期已支付 |
| `/reminder_pending` | 列出全部待确认周期 |
| `/reminder_history <id>` | 计划的审计事件历史 |

### 动态设置

- `/settings timezone <IANA 时区>`，例如 `Asia/Shanghai`
- `/settings reminder <HH:MM>`，例如 `09:30`
- `/settings sync <秒>`，例如 `120`
- `/settings folder <accountID>`：列出该账号的文件夹名称、文件夹 ID 与启用状态
- `/settings folder <accountID> <folderID> on|off`：开启或关闭指定文件夹的同步
- `/settings language`：查看当前界面语言
- `/settings language en|zh-CN`：切换机器人界面语言，立即生效

### 界面语言

- 支持 English（`en`）与简体中文（`zh-CN`）两种界面语言；未设置时默认简体中文。
- 设置值保存在数据库 `settings` 表的 `language` 键中，每条消息、每次按钮回调和每次投递都会重新读取，修改后无需重启。
- 也可使用别名 `zh`、`zh-cn`、`cn` 表示简体中文；其他值会被拒绝且不修改已保存的设置。
- 命令名、按钮回调数据、日志与已存储的业务数据不随语言变化。

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

## 5. 手动周期提醒

手动周期提醒不依赖邮件，适合 VPS 续费、域名、订阅等固定支出。

### 创建与编辑

- `/reminder_add` 启动向导：标题 → 周期（v1 仅月度）→ 商家 → 金额/币种 → 账期日（1–31）→ 资产（可选，可跳过）→ 提醒时间（`HH:MM`）→ 时区 → 支付链接（可选）→ 备注（可选）→ 确认。
- 向导状态保存在内存中，超过 `MANUAL_WIZARD_TTL` 自动失效；发送 `/cancel` 可随时取消。
- `/reminder_edit <id>` 用同一向导修改现有计划；修改账期日、提醒时间或时区会重算下一个账单日，并跳过已有周期的日期。

### 每日汇总与确认

- 每个计划在每个**计划本地日历日**最多发送一条汇总，列出当天所有待确认周期；同一计划同一天重复调度或重启不会重复发送。
- 账期日超过当月天数时按 `last_day` 取当月最后一天。
- 汇总消息与 `/reminder <id>` 中的「Mark paid」按钮只显示二次确认；点「Confirm paid」后才写入 `PAID_CONFIRMED`。已确认周期可「Undo」回到待确认。
- 只有授权用户（`TELEGRAM_ALLOWED_USER_IDS`）且会话等于 `TELEGRAM_CHAT_ID` 时确认才生效；其他用户点击会被拒绝。
- **邮件永远不会把手动计划或周期标记为已支付**，支付状态只能由用户确认改变。

### 回调行为

| 回调 | 行为 |
|---|---|
| `Mark paid` | 只展示确认提示（Confirm / Cancel），不改状态 |
| `Confirm paid` | 调用确认，写入 `PAID_CONFIRMED` 与 `PAYMENT_CONFIRMED` 事件，并刷新消息 |
| `Undo` | 撤销确认，回到 `PENDING_CONFIRMATION` |
| `View plan` / `History` | 打开计划详情或事件历史 |

### 配置变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `MANUAL_DEFAULT_TIME` | `09:00` | 计划未填写时的默认提醒时间 |
| `MANUAL_DEFAULT_TIMEZONE` | `Asia/Taipei` | 计划未填写时的默认时区 |
| `MANUAL_DAILY_RETRY` | `true` | 汇总投递失败后是否重试 |
| `MANUAL_DAILY_INTERVAL` | `24h` | 汇总重试间隔 |
| `MANUAL_MONTH_END_POLICY` | `last_day` | v1 仅支持 `last_day` |
| `MANUAL_SCHEDULER_INTERVAL` | `30s` | 调度轮询间隔 |
| `MANUAL_WIZARD_TTL` | `10m` | 向导有效期 |
| `MANUAL_MAX_CATCHUP_MONTHS` | `24` | 追赶窗口；超出后跳到下一个账单日 |

手动提醒默认启用；关闭需要以不含手动服务的方式组合机器人（冻结契约中的显式禁用方式），没有运行时开关。

## 6. 账单交互

- 账单以**一条增强消息**投递，包含明细、金额、到期日、主机与可信的商家链接。
- 点击「已支付」只是进入**二次确认**；再次确认后才写入 `PAID_CONFIRMED`。
- 已确认账单可「撤销」，回到 `PENDING_CONFIRMATION`。
- 提醒：到期前按本地日历日每天提醒一次；账单创建当天不提醒；`REVIEW_REQUIRED` 不提醒。

## 7. 投递语义与限制

- 出站消息持久化在 outbox，失败会按退避重试。
- 网络超时导致结果不明时标记为 `UNKNOWN` 并重试，属于**至少一次**投递；极端情况下可能重复一条消息。
- 每个分片单独记录 Telegram 消息 ID，重试时跳过已成功的分片。
- 已发送的外部消息无法与本地支付状态原子回滚，这是实际存在的限制。

## 8. 常见问题

| 现象 | 原因 | 处理 |
|---|---|---|
| 启动报 `TELEGRAM_BOT_TOKEN` 格式错误 | 令牌不完整 | 从 BotFather 重新复制 |
| 命令无响应 | Chat ID 或用户 ID 不匹配 | 核对 `getUpdates` 中的值 |
| 429 Too Many Requests | 触发限流 | 调大 `TELEGRAM_RATE_INTERVAL`，程序会遵守 `retry_after` |
| 收不到消息 | 机器人被移出群组或被禁言 | 检查群成员与权限 |
