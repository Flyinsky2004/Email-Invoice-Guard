# Lessons

此文件记录已发生且可复用的工程教训。当前新仓库尚无历史条目。

新条目格式：`**一句话规则**`，依次四行：事件证据、修复 commit、Done right 示例、Seen N× (日期)。未知 commit 明确待定位，禁止写入凭据。

**Herdr 派工前验证完整 PATH，工具存在不代表 agent 环境可见。**
- 初始化校准（551cad8 后、冻结契约前）：直接启动与非交互登录 shell 中 Node 缺失，Claude hooks 报错；未派正式工作。
- 修复 commit：待冻结契约提交；启动包装改为交互登录 shell 并显式补齐已安装 Node bin，重新校准。
- Done right: 在 agent 中实际运行 command -v go git python3 node，再确认 Go 构建/测试可用。
- Seen 1× (2026-11-15).
