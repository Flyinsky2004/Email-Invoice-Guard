# Lessons

此文件记录已发生且可复用的工程教训。当前新仓库尚无历史条目。

新条目格式：`**一句话规则**`，依次四行：事件证据、修复 commit、Done right 示例、Seen N× (日期)。未知 commit 明确待定位，禁止写入凭据。

**Herdr 派工前验证完整 PATH，工具存在不代表 agent 环境可见。**
- 初始化校准（551cad8 后、冻结契约前）：直接启动与非交互登录 shell 中 Node 缺失，Claude hooks 报错；未派正式工作。
- 修复 commit：待冻结契约提交；启动包装改为交互登录 shell 并显式补齐已安装 Node bin，重新校准。
- Done right: 在 agent 中实际运行 command -v go git python3 node，再确认 Go 构建/测试可用。
- Seen 1× (2026-11-15).

**独立 Store 的内存 SQLite 必须使用随机具名 URI，禁止共用 file::memory:?cache=shared。**
- T06 初版（eee4718 前）用固定 file::memory:?cache=shared，两个同时 Open 的 Store 会互相看到对方数据；Codex 早期审查指出。
- 修复 commit: eee47188bfa046287c7ff73fb61b8f547d71e06a（每次 Open 生成 eig-<随机ID> 具名内存库，同池共享、跨 Store 隔离）。
- Done right: TestMemoryStoresAreIsolated 断言 A 库账号在 B 库返回 ErrNotFound。
- Seen 1× (2026-11-15).

**备份目标绝不能先删除或覆盖；必须绝对路径规范化比较、拒绝任何已存在目标并以 O_EXCL 0600 创建。**
- T06 初版（eee4718 前）先 os.Remove(destination) 再 VACUUM INTO，相对/符号/硬链接别名可能删掉 live DB，失败还会丢失旧备份；Codex 高优先级安全审查指出。
- 修复 commit: eee47188bfa046287c7ff73fb61b8f547d71e06a（filepath.Abs 比较 live/-wal/-shm，Lstat 拒绝已存在目标，O_EXCL 创建空文件，失败只删本次新建文件）。
- Done right: TestBackupRejectsRelativeAliasOfLiveDatabase / Symlink / Hardlink / ExistingBackupAndPreservesIt / FailureRemovesOnlyCreatedTarget。
- Seen 1× (2026-11-15).

**内部全量列表的 Limit<=0 不能被当作默认分页上限。**
- T06 初版（eee4718 前）ListInvoices/ListAssets 复用 clampLimit(0)=100，会永久漏掉第 101 张 pending 发票和资产关联；Codex 集成审查指出。
- 修复 commit: eee47188bfa046287c7ff73fb61b8f547d71e06a（Limit<=0 不加 LIMIT，正数上限 1000；EmailFilter 保持有限默认）。
- Done right: TestListInvoicesZeroLimitReturnsAll / TestListAssetsZeroLimitReturnsAll 各造 150 条断言全量。
- Seen 1× (2026-11-15).

**子表 ID 冲突要归一化，不能因调用方复用 ID 丢数据。**
- T06 测试暴露：多封邮件复用附件 ID 触发 email_attachments UNIQUE 失败，整封邮件保存失败。
- 修复 commit: eee47188bfa046287c7ff73fb61b8f547d71e06a（附件插入冲突时生成新 ID 并回写到返回结构）。
- Done right: 测试中同一附件 ID 的多封邮件均成功保存且 roundtrip 一致。
- Seen 1× (2026-11-15).

**追加式审计事件按插入顺序（rowid）排序，不按调用方传入时间。**
- T06 测试暴露：Transition 使用调用方传入的早于 CREATED 的时间，按 created_at 排序把 PAID 排到 CREATED 前。
- 修复 commit: eee47188bfa046287c7ff73fb61b8f547d71e06a（InvoiceEvents ORDER BY rowid）。
- Done right: TestTransitionCompareAndSwapIsIdempotent 断言事件顺序 CREATED → PAID_CONFIRMED。
- Seen 1× (2026-11-15).

**持有未关闭的 *sql.Rows 时不得再发子查询，尤其单连接内存库会死锁。**
- T06 初版 ListInvoices/AssetInvoices 在 rows 未关闭时调用 invoiceItems；内存库 MaxOpenConns(1) 会永久等待；Codex 审查提醒。
- 修复 commit: eee47188bfa046287c7ff73fb61b8f547d71e06a（先收集并 Close rows，再加载子项）。
- Done right: 所有列表方法先 Close rows 再 get 子项，-race 与并发测试通过。
- Seen 1× (2026-11-15).

**SQLite TEXT 时间必须固定宽度 UTC 布局，保证字典序等于时间序。**
- T06 审查提醒：RFC3339Nano 可变小数位会让整秒 "…00Z" 与同秒 "…00.123Z" 错序，影响租约/过期/available_at 比较。
- 修复 commit: eee47188bfa046287c7ff73fb61b8f547d71e06a（统一 2006-01-02T15:04:05.000000000Z）。
- Done right: TestFixedWidthTimeOrdering / TestClaimDeliverySameSecondBoundary / TestSessionExpirySameSecondBoundary。
- Seen 1× (2026-11-15).

**资产的 LastKnownServiceStart/End 是最近一个账期，不能取历史 MIN/MAX 拼接。**
- T06 初版（eee4718）mergeAsset 取 start MIN、end MAX，会把不同续费周期拼成历史跨度，旧账单还能覆盖 model/os/LastInvoice；集成审查指出。
- 修复 commit: b8c5980d5afea5d30451eb1a2bd7063e95893488（next 有效 end >= cur end 时成对更新 start/end/LastInvoiceID 与描述字段；旧账单只补空字段）。
- Done right: TestAssetRenewalKeepsLatestPeriodAndOlderReplayCannotOverwrite（2026-10/11 → 2026-11/12，再回放 2026-09）。
- Seen 1× (2026-11-15).

**OAuth 回调写入账号与凭据必须同事务，避免半状态账号。**
- T01 首版 CompleteAuthorization 顺序 SaveAccount+PutCredential；repo 为 Store 时凭据写失败会留下无凭据账号，后续同步持续失败。
- 修复 commit：dcf6c3c。
- Done right: 检测 core.Store 并在 Transaction 内同时写 account+credential，失败整体回滚；fakeStore 回归测试断言回滚后 0 账号。
- Seen 1× (2026-11-15).

**同 provider 同地址重新授权必须复用原 accountID，禁止 NewID 造成唯一键冲突。**
- T01 首版每次授权 NewID；移除后重加或同地址重授权会触发 UNIQUE(provider,address) 冲突并可能割裂历史账单。
- 修复 commit：dcf6c3c。
- Done right: ListAccounts 按 provider+address 大小写不敏感查找并复用 ID/CreatedAt，仅同 owner 可重授权，异 owner 返回 ErrUnauthorized。
- Seen 1× (2026-11-15).

**服务器 Retry-After 必须完整遵守，不得截断后提前重试。**
- T01 首版把 120s 截到 30s，违反服务端退避要求并可能加剧限流。
- 修复 commit：dcf6c3c。
- Done right: header 值原样等待，仅本地指数退避设上限；用 ctx 取消控制长等待，回归测试断言 120s 不被截断且取消前不重试。
- Seen 1× (2026-11-15).

**HTTP 重定向必须锁定产生该请求的 origin，不能使用多 origin 联合白名单。**
- T01 首版允许 Graph/Authority 两 origin 互相重定向，token 端点可能把表单请求导向 Graph origin。
- 修复 commit：dcf6c3c。
- Done right: CheckRedirect 比较 via[0] 与目标 origin，跨 origin 直接拒绝；回归测试覆盖 token→Graph 与 Graph→authority 均 0 命中。
- Seen 1× (2026-11-15).

**Graph v1.0 属性名必须以官方资源文档为准，禁止凭猜测 select 不存在的字段。**
- T01 首版 ListFolders 使用 $select=id,displayName,wellKnownName；Graph v1.0 mailFolder 无 wellKnownName 属性，真实请求会被拒绝且角色映射失效。
- 修复 commit：f962f0b。
- Done right: 通过 /me/mailFolders/{well-known}?$select=id 解析角色 ID，再列真实字段并递归 childFolders；测试断言请求不含 wellKnownName，覆盖中文收件箱与嵌套自定义文件夹。
- Seen 1× (2026-11-15).

**成功响应不得设置固定字节上限；只有错误小响应可以限流。**
- T01 首版对 Graph 成功响应使用 8MiB LimitReader，合法大正文会被截断并导致 JSON 解码失败、同步永久卡住。
- 修复 commit：f962f0b。
- Done right: 成功响应流式解码不设上限，token/错误响应保留 1MiB 限制；>8MiB 正文回归测试断言首尾完整。
- Seen 1× (2026-11-15).

**HTML 转文本应使用 HTML 解析器而非正则剥离标签。**
- T01 首版正则剥离标签会丢失未知或畸形标签内的有效文本。
- 修复 commit：f962f0b。
- Done right: 使用 x/net/html 遍历 DOM，跳过 script/style/head，块级元素换行并归一空白；测试覆盖未知标签、中文与实体。
- Seen 1× (2026-11-15).

**集合导航属性不得用 $expand 代替可分页端点，否则会静默丢项**
- T01 首版 fetchAttachments 使用 $expand=attachments，忽略 @odata.nextLink，附件超过一页时会丢失文件名与数量。
- 修复 commit：4fc35ed。
- Done right: GET /me/messages/{id}/attachments?$select=id,name,contentType,size 遍历 nextLink，不请求 contentBytes；两页回归测试；附件 ID 用 account+message+graphID 哈希生成 32-hex，避免跨账号碰撞。
- Seen 1× (2026-11-15).

**IMAP BODY[part] 不保证包含 MIME 头，文本部件必须同时取 BODY[part.MIME] 拼接解码，且禁止截断正文。**
- T02 初版对 multipart 文本部件只取 BODY[part] 后直接 message.Read，并用 LimitReader 8MiB；Codex 审查指出 base64/quoted-printable 中文正文会被当作 header 解析或静默截断，BODYSTRUCTURE 缺失时 BODY[] 兜底还会下载完整附件。
- 修复 commit：待定位（本次 T02 提交）。
- Done right: 每个文本部件同时 FETCH BODY.PEEK[n] 与 BODY.PEEK[n.MIME]，拼接后交给 go-message 解码；io.ReadAll 完整读取；BODYSTRUCTURE 缺失直接报错；真实 TLS IMAP 测试断言中文全文、附件元数据且不请求附件 section。
- Seen 1× (2026-11-15).

**go-imap imapserver 在客户端请求 Extended BODYSTRUCTURE 时必须写 Extended 非 nil 结构，text/* 还必须带 Text.NumLines。**
- T02 测试服务器初版未设置 Extended/Text，imapserver panic "client requested extended body structure but a non-extended one is written back"，客户端报 "expected number64, got N"。
- 修复 commit：待定位（本次 T02 提交）。
- Done right: 测试用 BodyStructureSinglePart 设置 Extended 与 Text{NumLines}，多部件设置 BodyStructureMultiPartExt。
- Seen 1× (2026-11-15).

**IMAP 客户端库会覆盖连接 deadline，必须用 net.Conn 包装器把每次 SetDeadline 钳制到 min(ctx deadline, now+cfg.Timeout)。**
- T02 初版只在 TCP Dial 使用 cfg.Timeout，go-imap 等待 greeting 时自行设置 30s 读超时，静默 TLS/IMAP 对端可挂起远超配置超时；Codex 审查指出。
- 修复 commit：待定位（本次 T02 提交）。
- Done right: deadlineConn 包装 SetDeadline/SetReadDeadline/SetWriteDeadline，测试静默 TLS 与静默 greeting 对端均在配置超时内失败，且更短的 ctx deadline 优先。
- Seen 1× (2026-11-15).

**QQ 重复提交同一地址或移除后重加必须复用 (provider,address) 已有账号 ID 并校验 owner，不能每次 NewID。**
- T02 初版每次 onboarding 都 core.NewID()，会触发 mail_accounts UNIQUE(provider,address)；Codex 审查指出。
- 修复 commit：待定位（本次 T02 提交）。
- Done right: 持久化事务内 ListAccounts 查找同 provider+address，owner 不符返回 ErrUnauthorized，复用 ID 更新凭证并保留历史；测试覆盖同 owner 复用、异 owner 拒绝与事务回滚。
- Seen 1× (2026-11-15).

**文本解码失败不得返回空字符串；必须保留原始/部分解码内容并标记，否则游标提交会永久丢正文。**
- T02 验收退修：decodeText 在 message.Read/io.ReadAll 错误时返回 ""，fetchEmail 继续并提交游标，永久丢失该文本部件；joinMIMEPart 以正文开头猜测自带头也会误判正文。
- 修复 commit：待定位（本次退修提交）。
- Done right: 解码失败返回原始编码文本（io.ReadAll 有 partial 时保留 partial+raw），写入 X-Invoice-Guard-Decode-Error 标记并 Warn 日志；MIME 头缺失时由 BODYSTRUCTURE 合成；joinMIMEPart 无条件拼接；损坏 base64/QP、无 MIME 头、正文以 Content-Type 开头均有测试。
- Seen 1× (2026-11-15).

**新增或修正测试后必须先实际运行，断言以真实归一化输出为准。**
- T03 实现期间（本提交前）：`HTMLToText` 相邻 `<p>` 归一化为 `\n\n`，测试误断言 `\n` 导致失败；同一轮测试文件漏引 `testing` 包导致编译失败，浪费一次验证循环。
- 修复 commit：2f158e5（T03 实现提交）。
- Done right: 先跑 `go test` 看真实输出，再按 `One\n\nTwo` 的空行语义断言；补全 import 后 `go vet` 与 `-race` 全绿。
- Seen 1× (2026-11-15).

**禁止在源码/测试中使用占位符语法承载凭据，避免被替换为真实密钥写入仓库。**
- T04 实现中在测试 URL 里写了凭据占位符，工具输入替换后真实密码被写入 rfchost_test.go；发现后改为字面量 user:pass 测试值。
- 修复 commit：待定位（本次 T04 提交）。
- Done right: 测试凭据一律使用明显非机密的字面量（如 user:pass），不引用任何密钥占位符。
- Seen 1× (2026-11-15).

**业务字段必须按样例保留完整词组，不能只截取限定词。**
- T04 验收退修：DiscountType 只存了 "Recurring"，样例要求 "Recurring Discount"；items.go 正则把 "Discount" 放在捕获组外，测试也错误期望 "Recurring"。
- 修复 commit：本次 T04 修复提交。
- Done right: 捕获组包含完整优惠方式词组（如 `(.*?Discount)`），测试断言精确样例值。
- Seen 1× (2026-11-15).

**Telegram 发送响应不可解析或缺少 message_id 时必须记为 UNKNOWN，不能记为确定失败或成功。**
- T07 初版 api.go 把 HTTP 200 但响应体损坏当作确定 apiError，把空 result 当成功；若消息实际已送达会漏记 ID 并误判状态。Codex 早期审查指出。
- 修复 commit：本分支 T07 实现提交（SHA 见 result.txt）。
- Done right: call 对 200 无法解析/result 解码失败返回 transportError；sendMessage 校验 MessageID != 0；delivery 仅把 apiError 记为已知失败，其余为 UNKNOWN。
- Seen 1× (2026-11-15).

**不可信长文本不得拼进 writeHTML 标签，必须先转义再按 rune 分块。**
- T07 初版 FormatInvoice 用 "<b>Invoice "+escape(Number)+"</b>"，超长 Number 会触发按 rune 拆分并可能拆断 HTML 实体。Codex 早期审查指出。
- 修复 commit：本分支 T07 实现提交（SHA 见 result.txt）。
- Done right: 固定标签单独 writeHTML("<b>Invoice</b>")，内容走 writeRaw/inv.Number 由 escapeChunks 保证实体完整；测试用 500 个 `A&B<"` 验证。
- Seen 1× (2026-11-15).

**测试断言必须隔离每次交互的调用记录，子串匹配要带边界。**
- T07 测试初版复用 mock 的累积调用导致 answer 数偏多，且 "INV-2" 命中 "INV-2026-001" 造成误判。
- 修复 commit：本分支 T07 实现提交（SHA 见 result.txt）。
- Done right: mock 增加 reset() 并在断言前清空；用 "INV-2 "（带空格）做边界匹配；跨交互断言取最后一次调用。
- Seen 1× (2026-11-15).

**可信链接校验必须对齐真实 fixture 的 host+path+query，不能只校验 host。**
- T07 初版把 RFCHOST 信任域写成 rfchost.com 且只校验 host，真实账单是 https://my.rfchost.com/viewinvoice.php?id=143058；会漏掉合法按钮或放行伪造路径/redirect 参数。正式验收退修指出。
- 修复 commit：本分支 T07 验收修复提交（SHA 见 result.txt）。
- Done right: 固定 my.rfchost.com + /viewinvoice.php + 单一数字 id；拒绝额外 query、重复 id、userinfo、非 443 端口、fragment；用精确 fixture 与恶意 redirect 用例断言。
- Seen 1× (2026-11-15).

**限流等待后、每次外部发送前必须重新校验业务状态。**
- T07 初版只在渲染前检查提醒是否 pending，限流等待期间用户确认支付后仍会发出过期提醒。正式验收退修指出。
- 修复 commit：本分支 T07 验收修复提交（SHA 见 result.txt）。
- Done right: 在 limiter.wait 之后、sendMessage 之前重新 GetInvoice；非 pending 立即 SkipDelivery；测试用可控 sleep 模拟 queued→wait→confirm→no send。
- Seen 1× (2026-11-15).

**长耗时命令不得在轮询 goroutine 内同步执行，后台任务需可取消并纳入退出等待。**
- T07 初版 /sync 同步执行最长 2 分钟，阻塞 polling 导致支付确认延迟。正式验收退修指出。
- 修复 commit：本分支 T07 验收修复提交（SHA 见 result.txt）。
- Done right: /sync 立即回应并在后台 goroutine 执行，重复触发拒绝；ctx 取消传播到 SyncAll，Run 退出时有界等待后台任务结束。
- Seen 1× (2026-11-15).

**用户可见时间必须使用动态配置时区并显式标注偏移，不得硬编码 UTC。**
- T07 初版 FormatEmail 强制 ReceivedAt.UTC()，忽略 cfg.Timezone 与动态 timezone 设置。正式验收退修指出。
- 修复 commit：本分支 T07 验收修复提交（SHA 见 result.txt）。
- Done right: 导出 FormatEmail/FormatInvoice 默认 Asia/Taipei，Bot 使用私有 formatEmailAt/formatInvoiceAt 读取动态 timezone；时间格式带 MST 与 -07:00 偏移；账单确认时间同样标注时区。
- Seen 1× (2026-11-15).

**只有 ErrNotFound 才能标记 SKIPPED；其他存储读取错误必须 FailDelivery 重试。**
- T07 renderDelivery 初版对 GetInvoice/GetEmail 任意错误都 SkipDelivery，数据库 busy/代理错误会永久丢弃邮件；GetAccount 暂时失败也被静默降级。验收补充指出。
- 修复 commit：本分支 T07 补充修复提交（SHA 见 result.txt）。
- Done right: errors.Is(err, core.ErrNotFound) 才 SkipDelivery，其余 FailDelivery(unknown=false)；GetAccount 暂时失败同样重试，仅 NotFound 用最小账户继续；测试覆盖 invoice/email/account 三类 transient read。
- Seen 1× (2026-11-15).

**同一账单的增强通知键盘必须指向触发本次通知的邮件，而不是账单首封。**
- T07 初版 keyboard 总用 inv.SourceEmailID，同一账单第二封邮件点“原始邮件”看到的是首封全文。验收补充指出。
- 修复 commit：本分支 T07 补充修复提交（SHA 见 result.txt）。
- Done right: clone invoice 后用 payload.EmailID 覆盖 SourceEmailID 再生成 keyboard，不修改持久账单；测试 duplicate invoice different email 断言按钮指向第二封且能取到第二封全文。
- Seen 1× (2026-11-15).

**依赖前置命令必须成功后才向 Worker 发送完成信号。**
- 主线集成 T08（85c0c52 之后）：rebase 因 LESSONS.md 追加冲突暂停，串行 shell 的下一条 hc send 仍发出了阶段B信号；在 Worker 改代码前中断并纠正。
- 修复 commit：待集成报告提交；Codex 保留双方教训解决冲突、实际完成 rebase 后才重新派工。
- Done right: 使用 check=True 或 && 连接有前置依赖的操作，并检查 rebase 状态再发送通知。
- Seen 1× (2026-11-15).

**面向用户的配置命令必须提供发现步骤（列出可用 ID），否则参数不可操作。**
- T07 最终可操作性补齐：/settings folder 需要 folderID，但 /accounts 与 /settings 都不展示 folderID，用户无法启用自定义文件夹；主线验收指出。
- 修复 commit：本次 T07 可操作性提交（SHA 见 result.txt）。
- Done right: /settings folder <accountID> 校验 owner 后列出 name/id/enabled；无目录提示先 /sync；help 与 settings 同步显示用法；mock 测试覆盖列表、空目录、异 owner。
- Seen 1× (2026-11-15).

**远程文件夹发现完成后，在写事务内重读用户的启用选择。**
- 集成回归 TestConcurrentFolderPreferenceSurvivesDiscovery：模拟 LIST 期间用户关闭 Inbox，旧实现将 Enabled 恢复为 true；真实 SQLite 测试已复现。
- 修复的 commit：c90f9286735c5bd6e30bb6a7f1912a31c769ecc4。
- Done right: 远程 I/O 在事务外，随后在同一事务读取最新目录偏好并保存描述字段，回归及 race 测试通过。
- Seen 1× (2026-11-15).

**Compose 文件位于子目录时，`${VAR}` 插值不读取仓库根 `.env`，应改用 `env_file`。**
- T08 阶段A：`deployments/docker-compose.yml` 使用 `${DOMAIN:?...}`，从仓库根执行 `docker compose config` 时报 `required variable DOMAIN is missing`；Compose 从 compose 文件所在目录（`deployments/`）加载 `.env`，根目录 `.env` 不参与插值。
- 修复 commit：本次 T08 阶段A 提交（`feat(app-deployment): implement T08 module and tests`）。
- Done right: app 与 caddy 都声明 `env_file: ../.env`，Caddy 在容器内读取 `{$DOMAIN}`；`docker compose -f deployments/docker-compose.yml config` 无需 `--env-file` 即通过，且 `TRUSTED_PROXY_CIDRS` 与 `backend` 网段一致。
- Seen 1× (2026-11-15).

**reminder.Tick 用传入的 now 作为 outbox available_at；模拟“次日”的测试必须先让投递到期。**
- T08 阶段B E2E：用 nextDay 调用 Tick 后立刻 DeliverOnce，ClaimDelivery 按真实 now 判定未到期，返回 handled=false，误判为提醒未创建。
- 修复 commit：本次 T08 阶段B 提交。
- Done right: 测试在 Tick 后把 PENDING/FAILED/UNKNOWN 的 available_at 改为 now-1s（固定宽度 UTC 布局 `2006-01-02T15:04:05.000000000Z`）再 DeliverOnce，真实 claim/deliver/fail/retry 路径不变。
- Seen 1× (2026-11-15).

**最终安全检查要覆盖 Go 工具链及反向代理的实际编译版本。**
- d40613c 后 govulncheck 发现 GO-2026-6859（net/http、Go 1.27.3）；实际 caddy:2.11.9-alpine build-info 也显示 Go 1.27.3，固定应用依赖不足以覆盖 SDK 漏洞。
- 修复 commit：5b2f617；当时将 go.mod 最低版本与两个镜像编译器统一 Go 1.27.4，固定 Caddy 2.11.9 源码重新编译。后续部署已按用户要求改为原生 systemd。
- Done right: 保存修复前扫描记录，以修复版本重跑测试、构建、govulncheck 和容器验证，并核对二进制 build-info。
- Seen 1× (2026-11-15).

**安装器的显式配置参数必须同步写入应用配置和服务定义。**
- Release 安装器静态审查发现 `--data-dir` 原先仅改变服务工作目录，已有配置升级时未触发 DATABASE_PATH 更新；尚未执行安装，未影响实际数据。
- 修复 commit：本次 Release/systemd 交付提交；显式数据目录触发配置更新，并检查旧配置派生路径是否被 ProtectHome 屏蔽。
- Done right: 同时核对参数解析、DATABASE_PATH 与 WorkingDirectory，更新配置时保留原密钥和其他参数；仅做静态检查，实际安装由用户验证。
- Seen 1× (2026-11-15).

**用户变更验证与发布边界后，要立即停止原流程并明确剩余交付。**
- 最终交付阶段用户要求停止 Docker/实际构建、由用户推送验证；核心功能完成不代表新增 Release workflow 与安装脚本已经完成。
- 修复 commit：本次 Release/systemd 交付提交；补齐 workflow/installer 后仅静态检查，不推送不发布，清理前核对分支均已合并。
- Done right: 如实报告新增交付尚未落盘，完成并保留审核证据后再声明完成。
- Seen 1× (2026-11-15).

**安装器必须让用户选择监听端口，并在启动前验证端口可用。**
- 0.0.1 Release 在 Debian VPS 首次安装失败：安装器固定使用 `127.0.0.1:8080`，端口被其他程序占用后应用启动即退出；安装器只报“服务未启动”，没有指出真实原因，只能整体回滚。
- 修复 commit：本次安装器修复提交（交互询问端口、`--port`、启动前 `ss` 检测、失败时输出 journal 最近日志）。
- Done right: 首次安装询问端口（默认 8080），升级保留原端口；启动前用 `ss -H -ltn "sport = :PORT"` 检测，占用则重新询问或明确报错；健康检查失败时打印 `journalctl -u <service> -n 30`，不再只提示自行查看日志。
- Seen 1× (2026-11-15).

**安装/更新脚本必须常驻仓库并可直接获取，不能只作为 Release 资产。**
- 0.0.1 交付把 `install.sh` 作为 Release 资产上传，导致修一个安装脚本也要重新构建、发版，用户明确反对。
- 修复 commit：本次脚本交付调整（`scripts/update.sh` 入库，文档改用 raw.githubusercontent.com 的 main 地址，Release 只上传二进制与校验和）。
- Done right: 脚本从仓库 `main` 获取；`update.sh` 负责取最新 `install.sh` 并执行升级；Release 工作流只发布二进制、`SHA256SUMS` 与压缩包，脚本修复无需发版。
- Seen 1× (2026-11-15).

**新增迁移必须同步更新既有 schema 版本断言，且迁移任务白名单要覆盖该测试文件。**
- T10 实现 0002 迁移后（本次提交前），`internal/storage/store_test.go` 仍断言 `MAX(version)=1` 与 `schema_migrations` 行数=1，导致 `go test ./...` 失败；该文件不在 T10 五文件白名单内，Worker 按规则不能自行修改。
- 修复 commit：本次 T10 提交（orchestrator 批准扩白名单后，store_test.go 两处期望值改为 2；SHA 见 result.txt）。
- Done right: 迁移任务的白名单包含会因新版本失效的既有迁移测试，或派工前由 orchestrator 先把断言改为“最新版本”。
- Seen 1× (2026-11-15).

**手动提醒的 notified_at 与 REMINDER_SENT 事件必须由投递完成写入，不能在入队时写。**
- T10 初版（本次修复前）CreateDispatch 在入队时写 notified_at，CompleteDelivery 从不写 REMINDER_SENT；语义上把“已通知”提前到“已排队”，且审计事件缺失。验收审查指出。
- 修复 commit：本次 T10 修复提交（SHA 见 result.txt）。
- Done right: CreateDispatch 只写 dispatch/join/outbox；CompleteDelivery 在同一事务内对仍 PENDING_CONFIRMATION 的关联 occurrence 写 notified_at，并为该 dispatch 追加一条 plan 级 REMINDER_SENT（occurrence_id 为空、metadata 含 dispatch_id），重放不重复；Fail/Skip 不写该事件；测试覆盖 pending/paid/重放/失败/跳过。
- Seen 1× (2026-11-15).

**CreateOccurrence 的 CAS 要求 occ.DueDate 等于已存 next_due_date；追赶跳跃必须先持久化 next_due_date 再创建当期 occurrence。**
- T11 初版 catch-up 分支直接为 target 调用 CreateOccurrence，仓储因 next_due_date 仍是旧值返回 false，当期 occurrence 静默丢失；TestCatchUpBeyondWindowJumpsForwardToToday 首次运行失败。
- 修复 commit：本次 T11 实现提交（SHA 见 result.txt）。
- Done right: persistJump 先 UpdatePlan 写入 target 并记 PLAN_UPDATED，再对 target 调用 CreateOccurrence；两步之间崩溃也安全，下一 tick 会补建。
- Seen 1× (2026-11-15).

**创建/恢复/改期时，今天是否算本期必须按计划本地提醒时刻判断，不能只比较日期。**
- T11 初版（c466e10 合并后）用 firstBillingOnOrAfter 只比较日历日期：在账单日 10:00 创建提醒时间为 09:00 的计划时，next_due_date 仍是今天，随后 tick 立即创建当期并发送，违反“超过提醒时间则安排下月、避免创建后立即触发”。
- 修复 commit：本次 orchestrator 修复提交（firstBillingAfter 按本地时刻比较；08:59/09:00 算今天，09:01 算下月）。
- Done right: 新增 TestCreatePlanRespectsReminderTimeOnBillingDay / TestResumePlanAfterReminderTimeSkipsToday / TestUpdateScheduleAfterReminderTimeSchedulesNextMonth。
- Seen 1× (2026-11-15).

**改期或恢复后的 next_due_date 必须跳过已有 occurrence 的日期，否则该计划永久卡在无法插入的日期。**
- T11 初版改期只重算日期，不检查 UNIQUE(plan_id,due_date)：若目标日期已有 occurrence，CreateOccurrence 永远返回 false，后续周期不再生成。
- 修复 commit：本次 orchestrator 修复提交（firstFreeBilling 读取该计划全部 occurrence，最多前进 24 个月找空位）。
- Done right: TestUpdateScheduleSkipsExistingOccurrence / TestResumeSkipsExistingOccurrence 断言跳到下一个空位月份。
- Seen 1× (2026-11-15).

**提交前必须扫描改动文件，确认没有残留占位符替换后的真实凭据。**
- T12 实现中两次在 `wizard_test.go` / `manual_delivery_test.go` 的测试 URL 写入凭据占位符语法，工具替换后真实值进入工作区；提交前发现并改为 `"user:"+"pass"` 运行时拼接，未进入提交。根因同 T04 凭据占位符条目。
- 修复 commit：待定位（本次 T12 提交）。
- Done right: 测试凭据只写明显非机密的字面量或运行时拼接；提交前对改动文件 grep `user:`/凭据占位符语法，确认只剩安全字面量。
- Seen 1× (2026-11-15).

**Telegram 手动提醒的支付确认必须两阶段，向导必须覆盖规格步骤，且不得用非冻结接口实现越界能力。**
- T12 初版 `mp` 一键确认支付；用 `store` 类型断言实现按 occurrence 取消与 dispatch 明细；遗漏规格 7.1 的 period/asset 步骤。评审要求退修。
- 修复 commit：待定位（本次 T12 修复提交）。
- Done right: `mp` 只显示确认提示（Confirm=`mc`、Cancel=`mv`），`mc` 才调用 `ConfirmOccurrence`；只用 `core.ManualReminderService` 与 `core.Store` 的冻结方法；向导含 period（仅 monthly）与 asset（`ListAssets(Limit:0)` + Skip）步骤。
- Seen 1× (2026-11-15).

**手动提醒摘要与邮件提醒一样，必须在每次发送前重新读取计划与 occurrence 状态。**
- T12 合入后（fb50306）仅在渲染阶段过滤已支付 occurrence；若用户在限流等待或分片发送期间确认支付/暂停计划，仍会发出过期摘要。评审发现。
- 修复 commit：本次 orchestrator 修复提交（manualDeliveryFresh 在每片发送前重读 plan 与 payload 中的 occurrence）。
- Done right: TestManualSummaryRechecksBeforeSend / TestManualSummarySkipsWhenPlanPausedBeforeSend 断言 sendMessage 为 0 且 delivery 被 SKIPPED。
- Seen 1× (2026-11-15).

**Go 的 time.Parse("15:04") 接受单数字小时，不能用它严格校验 HH:MM。**
- T13 初版测试（本次提交前）断言 MANUAL_DEFAULT_TIME="9:00" 应失败，实际 time.Parse 接受 1 位小时，TestLoadConfigRejectsBadManual/time 失败；若依赖它校验会接受非规范格式。
- 修复 commit: 本次 T13 实现提交（SHA 见 result.txt）；沿用既有 REMINDER_TIME 的宽松语义，测试改用真正非法值 "9am" 与 "24:00"。
- Done right: 明确要求严格 HH:MM 时用正则或自定义解析；仅需合法性时与既有 REMINDER_TIME 保持一致，并用真正非法值做回归测试。
- Seen 1× (2026-11-15).

**账单解析测试样例必须取自真实邮件原始格式，不能手工重排分行，否则真实邮件会静默丢字段。**
- 用户提供真实 RFCHOST 账单原始邮件（2026-10-13）后复现：商品行把服务周期和价格放在同一行、优惠码与折扣子句也在同一行；testdata fixture 被手工拆成多行，旧正则只匹配拆行格式，真实邮件解析出 items=0，型号/主机名/系统/周期/原价/折扣/优惠码全部丢失，通知看不到明细。
- 修复 commit：待定位（本次解析器修复提交）。
- Done right: itemHeaderRe 允许 hostname 后同行携带 "(start - end) price"，Promotional Code 行用 inlineDiscountRe 拆出折扣子句；新增 TestParseRealTemplateInlineItemLine / TestParseRealTemplateHTML 直接断言真实模板的 item 字段，同时保留拆行 fixture 覆盖旧格式。
- Seen 1× (2026-11-16).
