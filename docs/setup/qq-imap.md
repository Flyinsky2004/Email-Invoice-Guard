# QQ 邮箱接入（IMAP + 一次性 HTTPS 授权码）

QQ 邮箱通过 IMAP over TLS（默认 `imap.qq.com:993`）同步，认证使用**第三方授权码**，绝不使用 QQ 登录密码。

## 1. 获取授权码

1. 登录 QQ 邮箱网页版 → **设置** → **账户**。
2. 找到「POP3/IMAP/SMTP/Exchange/CardDAV/CalDAV 服务」。
3. 开启 **IMAP/SMTP 服务**，按提示验证并生成**授权码**（16 位左右，只显示一次）。
4. 妥善保存授权码；它等同于该邮箱的 IMAP 密码。

## 2. 生成一次性接入链接

1. 在 Telegram 中向机器人发送 `/add qq`。
2. 机器人返回形如 `https://<你的域名>/setup/qq?token=...` 的链接。
3. 链接绑定发起人的 Telegram 用户 ID，默认 15 分钟有效（`IMAP_SESSION_TTL`），**只能成功提交一次**。
4. 打开链接，填写：
   - 邮箱地址（默认要求 `@qq.com`）
   - 上一步得到的授权码
5. 服务端会先真实连接 IMAP 验证，成功后才创建账号；失败不会保存任何可用凭据。

## 3. 自定义 QQ 别名域名（CR003）

默认只接受 `@qq.com` 与 `@foxmail.com`。如果你已确认某个别名地址可以走 QQ IMAP
（例如企业邮箱别名），可通过 `QQ_ALLOWED_DOMAINS` 显式加入白名单：

```dotenv
QQ_ALLOWED_DOMAINS=qq.com,foxmail.com,my-alias.qq.com
```

规则：

- 逗号分隔的**裸域名**，自动转小写并去重。
- 禁止 scheme、路径、端口、userinfo、通配符：`https://qq.com`、`qq.com/path`、
  `*.qq.com`、`qq.com:993`、`user@qq.com` 都会被拒绝。
- 必须是至少两段的合法 DNS 名称；`localhost`、`127.0.0.1`、`qq..com`、
  `-qq.com`、`qq.com.`、`qq.123` 都会被拒绝。
- 只固定 IMAP 主机 `imap.qq.com`；`IMAP_ADDRESS` / `IMAP_SERVER_NAME`
  指向其他主机会在启动时报错，不支持任意 IMAP 主机。
- 白名单只决定“允许尝试哪些地址”；每个别名仍必须通过真实的 QQ TLS 登录验证，
  认证失败不会保存任何可用账号。
- 空值表示使用默认 `qq.com,foxmail.com`。

## 4. 传输安全要求（CR001）
QQ 接入页只接受**安全传输**的请求：

- 直接访问时，请求必须自带 TLS（`Request.TLS != nil`）。
- 经过反向代理时，只有 `RemoteAddr` 落在 `TRUSTED_PROXY_CIDRS` 内的代理，才允许通过 `X-Forwarded-Proto: https` 声明 HTTPS。
- `TRUSTED_PROXY_CIDRS` 为空表示**不信任任何转发头**，此时请直接使用 HTTPS 访问，或按下方配置可信代理。

`PUBLIC_BASE_URL` 为 HTTPS 只说明链接长什么样，**不能**证明当前请求就是 HTTPS；服务端始终按上述规则独立判断。

### Docker Compose 场景

`deployments/docker-compose.yml` 中：

- 应用与 Caddy 位于固定私有网段 `10.89.0.0/24`（`backend` 网络）。
- 应用不发布任何端口，只能经 Caddy 访问。
- `TRUSTED_PROXY_CIDRS` 默认 `10.89.0.0/24`，与上述网段一致。

修改网段时必须同步修改 `TRUSTED_PROXY_CIDRS`；不要使用 `0.0.0.0/0`、`::/0` 或过宽前缀（配置加载会直接拒绝）。

### 直接暴露 / 其他反代

若不用 Caddy，可让应用直接监听并自行终止 TLS，或仅信任你自己的反代地址段，例如：

```dotenv
TRUSTED_PROXY_CIDRS=192.168.50.0/24
```

切勿把公网任意地址加入该列表。

## 5. 安全说明

- 授权链接使用随机 256 位 bearer token，数据库只保存其 SHA-256 哈希。
- GET 只校验不消费；POST 成功后原子消费，重放无效。
- 页面响应带 `no-store`、`no-referrer`、CSP 等头，不缓存、不外泄来源。
- 授权码与邮箱凭据加密存储，日志与错误信息中不出现明文。
- 失败认证不会创建可用账号。

## 6. 常见问题

| 现象 | 原因 | 处理 |
|---|---|---|
| 链接提示已过期 | 超过 `IMAP_SESSION_TTL` | 重新 `/add qq` |
| 链接提示已使用 | token 已被成功消费 | 重新 `/add qq` |
| 提示必须使用 HTTPS | 请求既非直连 TLS，也不来自可信代理 | 检查 Caddy 与 `TRUSTED_PROXY_CIDRS` |
| 登录失败 | 授权码错误或 IMAP 未开启 | 重新生成授权码，确认已开启 IMAP |
| 同步报 `ErrReauthorization` | 授权码被重置 | 重新 `/add qq` |
