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
- 修复 commit：57cb4b2（T03 实现提交）。
- Done right: 先跑 `go test` 看真实输出，再按 `One\n\nTwo` 的空行语义断言；补全 import 后 `go vet` 与 `-race` 全绿。
- Seen 1× (2026-11-15).
