# GitHub Release 构建

仓库：[Flyinsky2004/Email-Invoice-Guard](https://github.com/Flyinsky2004/Email-Invoice-Guard)。代码推送与 Release 发布由维护者执行；本次未推送、未创建远端 Release。

## 发布

1. 将 `main` 与 `.github/workflows/release.yml` 推送到仓库。
2. 在 GitHub Releases 中创建并**发布** Release，例如 tag `v1.0.0`。仅保存草稿或仅推送 tag 不触发该流程。
3. 等待 Actions 的 `Release Linux binaries` 成功。工作流先运行格式、依赖、静态与 race 测试，再交叉编译静态 Linux 二进制。
4. 在同一 Release 下获取：

   - `email-invoice-guard_v1.0.0_linux_amd64.tar.gz`
   - `email-invoice-guard_v1.0.0_linux_arm64.tar.gz`
   - `SHA256SUMS`
   - `install.sh`

每个压缩包含程序、systemd 单元、配置示例、安装脚本与接入/运维文档。版本、提交与构建时间写入 `--version` 输出。Actions 使用 `go.mod` 的 Go 版本，`CGO_ENABLED=0`，无 Docker。

工作流支持手动输入一个已存在 Release 的 tag 重新构建。重新上传采用 `--clobber`；二进制与校验文件上传后，最后上传安装入口。Prerelease 发布同样构建，但安装器的 `latest` 只选择最新稳定 Release；预发布需指定 `--version`。

## 安装

首次发布构建成功后：

```bash
curl -fsSL https://github.com/Flyinsky2004/Email-Invoice-Guard/releases/latest/download/install.sh | sudo bash
```

标准输入是脚本时，交互问题从 `/dev/tty` 读取。安装器会隐藏询问 Bot Token、询问信任用户 ID，并可选询问公网 HTTPS 地址。默认 Chat ID 是第一个信任用户，请先在 Telegram 与机器人私聊发送 `/start`。群组通知使用 `--chat-id` 指定群组的负数 ID。

需要先查看脚本或传参数时：

```bash
curl -fsSL https://github.com/Flyinsky2004/Email-Invoice-Guard/releases/latest/download/install.sh -o install.sh
sudo bash install.sh --public-url https://invoice.example.com --chat-id -1001234567890
sudo bash install.sh --help
```

自动化部署可用 `--non-interactive --bot-token-file /安全路径/bot-token --user-ids 123456789 --public-url https://invoice.example.com`。也可通过 `TELEGRAM_BOT_TOKEN`、`TELEGRAM_ALLOWED_USER_IDS`、`TELEGRAM_CHAT_ID`、`PUBLIC_BASE_URL` 提供配置。交互或 Token 文件可避免 Token 出现在命令历史与进程参数中。

安装器默认自动安装 Debian/Ubuntu 依赖。其他 systemd Linux 系统可自行安装帮助中所列依赖，使用 `--skip-deps`。支持 amd64、arm64；目标机不需要 Go。

## 升级与指定版本

重复运行同一命令会下载最新稳定版，保留原配置、数据库和加密密钥。指定版本：

```bash
sudo bash install.sh --version v1.0.0
```

`--reconfigure` 或配置参数允许更新配置，保留未涉及的配置项与原密钥。自定义安装目录与服务名称需在后续升级时使用相同参数。`--no-start` 只更新文件，不启用/启动或停止现有服务。

下载包必须通过 SHA-256 校验和 `--version` 检查才会替换现有程序。失败时尽力恢复原程序、配置和服务文件；**不回滚数据库**。升级前按备份文档保存 SQLite 快照与原密钥。安装器不自动删除数据目录。

## 权限与待验证事项

只有附件上传 job 申请 `contents: write`；其余 job 仅需读取。使用仓库自带的 `GITHUB_TOKEN`，无需额外发布 Token。官方 Action 固定为核对过的完整 commit SHA。

依用户要求，本次只做脚本/YAML 静态检查，未触发 CI、未运行新的实际构建、未在 VPS 执行安装。首次发布后需由维护者验证 Release 资产、systemd 启动、HTTPS 和真实外部服务。

参考：[GitHub Release 事件](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#release)、[systemd 执行环境](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html)。
