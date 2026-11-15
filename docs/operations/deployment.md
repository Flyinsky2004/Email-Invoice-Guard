# Linux / systemd 部署

推荐 Debian / Ubuntu + systemd。程序以独立非登录用户运行，不使用 Docker，不在 VPS 上编译。

## 一键安装

在 GitHub 已发布 Release 且 Actions 完成后：

```bash
curl -fsSL https://github.com/Flyinsky2004/Email-Invoice-Guard/releases/latest/download/install.sh | sudo bash
```

安装器询问 Bot Token、信任用户 ID、监听端口（默认 8080，被占用时会提示更换）和可选 HTTPS 地址。默认将通知发到第一个信任用户的私聊；先向 Bot 发送 `/start`。

参数示例：

```bash
curl -fsSL https://github.com/Flyinsky2004/Email-Invoice-Guard/releases/latest/download/install.sh -o install.sh
sudo bash install.sh --public-url https://invoice.example.com --chat-id -1001234567890
sudo bash install.sh --port 9090
sudo bash install.sh --version v1.0.0
```

完整选项：`bash install.sh --help`。发布、校验与无人值守安装见 [releases.md](releases.md)。

## 文件与服务

| 路径 | 用途 |
|---|---|
| `/usr/local/lib/email-invoice-guard/email-invoice-guard` | Release 二进制 |
| `/etc/email-invoice-guard/env` | systemd 环境配置，root `0600` |
| `/etc/email-invoice-guard/encryption.key` | 自动生成的 32 字节密钥，服务用户只读；升级保留 |
| `/var/lib/email-invoice-guard/invoice-guard.db` | SQLite 数据库 |
| `/etc/systemd/system/email-invoice-guard.service` | 服务定义 |

```bash
sudo systemctl status email-invoice-guard
sudo journalctl -u email-invoice-guard -f
sudo systemctl restart email-invoice-guard
sudo systemctl stop email-invoice-guard
/usr/local/lib/email-invoice-guard/email-invoice-guard --version
/usr/local/lib/email-invoice-guard/email-invoice-guard --healthcheck
```

默认服务随系统启动，数据目录仅服务用户可写。退出等待 30 秒；网络失败按应用重试规则处理。修改环境配置后执行 `systemctl restart`；修改单元后先 `systemctl daemon-reload`。

## HTTPS 接入

默认监听 `127.0.0.1:8080`（安装时可用 `--port` 或交互提示更换）。未提供公网地址时，先使用本机 HTTP 模式启动 Bot。QQ 授权码页面仍强制 HTTPS；添加邮箱前需配置真正的 HTTPS 源站。

在已有 Caddy 中使用：

```caddyfile
invoice.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

也可用现有 Nginx，向应用传递正确的 `X-Forwarded-Proto`。将 `/etc/email-invoice-guard/env` 设置为：

```dotenv
PUBLIC_BASE_URL="https://invoice.example.com"
ALLOW_INSECURE_HTTP="false"
TRUSTED_PROXY_CIDRS="127.0.0.1/32,::1/128"
```

只信任实际反代的来源网段，不对公网开放应用端口。TLS 反代本身由管理员配置；安装器不覆盖现有 Web 服务配置。

Microsoft 接入另需配置应用 Client ID、Client Secret 与同源回调 `/oauth/microsoft/callback`，见 `docs/setup/microsoft-oauth.md`。QQ 托管别名可通过 `QQ_ALLOWED_DOMAINS` 添加，仍须通过 QQ TLS 登录验证。

## 升级与移除

升级前备份数据库和原密钥，再重复运行安装脚本。配置与密钥不会因普通升级重新生成。若指定过自定义目录/服务名，升级时传入相同参数。

停用只需 `systemctl disable --now email-invoice-guard`。数据库与密钥由管理员单独保存；安装器不提供自动删数据的卸载操作。
