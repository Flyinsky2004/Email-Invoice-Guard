## 错误复盘与 LESSONS.md

- 开始工作、继续任务或创建/委派其他 agent 前，必须先阅读仓库根目录的 [LESSONS.md](LESSONS.md)，主动检查本次工作是否会重犯已记录的问题。
- 启动任何 agent（包括 Herdr 中的 CC/Codex、worktree agent 或子代理）时，必须在首条任务中明确宣告：先读并遵守 LESSONS.md；出现新错误要复盘并写入 LESSONS.md。不能只依赖对方自动发现该文件。
- 工作中实际犯过的错误、用户指出的问题以及排障得到的可复用教训，都要及时记录到 LESSONS.md。每条写清触发场景、错误/根因、修正办法、避免重犯的检查或回归测试，附必要的代码/任务证据；未知原因明确标为待查，不能编造结论。
- 同类教训更新已有条目，不反复堆砌；不得记录密钥、凭据、私密数据，也不得为免责而删除历史教训。交付前检查本次新教训是否已落盘。
- 并行 agent 各自在自己的 worktree 的 LESSONS.md 追加本路教训，不争写主工作区；集成人保留各路新增条目并去重，不可因合并冲突丢弃。旧 worktree 没有该文件时，先读取主工作区最新版本并据此建立本地副本，再追加。

- LESSONS.md 每条固定格式：`**<一句话规则>**`，随后依次四行：`- <哪个 commit / issue 出的事，后果是什么>`、`- <修复的 commit>`、`- Done right: <做对的例子>`、`- Seen N× (YYYY-MM-DD, YYYY-MM-DD).`。次数只计独立发生事件；来源/修复未核实则明确待定位，不编造。派工时一并宣告此格式。

## 通过 Herdr 启动和管理 coding agents

- 用户说“开 cc / Claude Code / codex / agy”等启动或安排 coding agent 的指令，默认指通过 Herdr 启动和管理这些 agent；除非用户明确指定其他方式。
- 执行前阅读 `~/.config/herdr-app/AGENTS.md`，需要时查阅该目录里的其他说明书及 `herdr-app commands`、`hc --help`、`hc --skill`，以当前安装版本为准。
- `herdr-app` 管理窗口、space、tab 和 pane；`hc`（herdr-core）负责启动、读取、发送指令及等待 agent 终端。根据任务使用相应接口。
- 不凭简称猜测实际启动命令；先核对 Herdr 的程序配置或已安装 agent。启动 Codex 时遵循 Herdr 文档的 `codex --no-daemon` 约定。
- 此约定规定启动方式，不表示每次对话都应自行启动额外 agent；依照用户当次要求及项目协作约定决定任务分配。
- 不修改 Herdr 自动生成的 `AGENTS.md` 或 `.d.ts` 文件。