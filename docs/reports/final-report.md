# 本地交付与清理报告

## 完成范围

核心项目与 T01–T08 的实现、修复已合并到 main，包括 Microsoft OAuth/Graph、QQ TLS IMAP、全量邮件流水线、RFCHOST 模板、SQLite、支付确认、每日提醒、资产与 Telegram。原始 AGENT.md 保留。

按用户后续要求，交付方式改为 GitHub Release + 原生 systemd，移除 Docker 构建/Compose 文件。新增：

- `.github/workflows/release.yml`：发布 Release 后测试、构建 Linux amd64/arm64、上传压缩包与 SHA256SUMS；安装/更新脚本常驻仓库，不随 Release 发布。
- `scripts/install.sh`：交互隐藏 Token、信任用户 ID、监听端口、可选参数、最新稳定版下载、校验、独立服务用户、systemd、升级保留配置/密钥/数据库及失败文件恢复。
- `scripts/update.sh`：从仓库 main 获取最新 install.sh 并执行升级，脚本修复无需重新发版或构建。
- `deployments/email-invoice-guard.service` 与原生 HTTPS 反代示例。
- `--version` 输出包含 tag、commit、build time；部署/发布/备份文档更新。

## 验证边界

部署变更前的 Go 1.27.4 检查记录：499 个顶层测试、含子测试 592 项通过，零跳过；18 个含测试的包通过，另有测试辅助包；全量 vet、race、依赖校验及 Go 漏洞扫描通过。原始证据保存在 evidence/，仅代表当时检查点。

用户随后明确不再执行实际构建、由用户负责验证，因此最新 workflow/installer 只执行 Bash 语法、YAML 解析、Action SHA 固定、workflow 内 shell 语法、格式与 git diff 检查。未运行安装器、未触发 Actions、未做新的构建或 VPS 部署。未来 Release 工作流会重新执行测试与构建。

没有真实 Microsoft、QQ 或 Telegram 凭证参与自动化测试。公开 HTTPS、真实邮箱授权和通知由用户部署后验证。

## Git 与 Agent 清理

8 个 Claude Code Worker 均已停止并移除；所有 task 分支先验证为 main 的祖先，再删除对应 Worktree 与分支；校准分支亦已清理。详细清单见 cleanup.json；报告与原始提交信息保留。主工作区与其他项目/用户会话未移除。

本次未推送 Git、未创建 tag、未发布 GitHub Release。推送、发布与实际验证由用户执行。
