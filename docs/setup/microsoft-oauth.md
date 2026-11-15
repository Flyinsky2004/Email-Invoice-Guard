# Microsoft / Outlook 账号接入

本项目通过 Microsoft Graph 读取 Outlook、Hotmail、Live 邮箱所有启用接收文件夹的新邮件。授权使用 OAuth 2.0 授权码 + PKCE，令牌由服务端加密保存。

## 1. 在 Azure 注册应用

1. 打开 <https://entra.microsoft.com> → **应用注册** → **新注册**。
2. 名称任意，例如 `Invoice Guard`。
3. **受支持的账户类型** 选择「个人 Microsoft 账户」（`consumers`）。若需同时支持工作/学校账户，选择多租户选项并相应设置 `MICROSOFT_TENANT`。
4. **重定向 URI** 选择「Web」，填写：

   ```
   https://<你的域名>/oauth/microsoft/callback
   ```

   该地址必须与 `PUBLIC_BASE_URL` 同源，且路径固定为 `/oauth/microsoft/callback`，否则启动时会报配置错误。
5. 在 **证书和密码** 中新建客户端密码，记录值（只显示一次）。
6. 在 **API 权限** 中添加 Microsoft Graph 的**委托权限**：
   - `Mail.Read`（读取邮件）
   - `offline_access`（刷新令牌）
   - `User.Read`（本实现读取 `/me` 以绑定邮箱地址，需要保留）

## 2. 写入配置

```dotenv
MICROSOFT_CLIENT_ID=<应用(客户端) ID>
MICROSOFT_CLIENT_SECRET=<客户端密码>
MICROSOFT_TENANT=consumers
MICROSOFT_REDIRECT_URL=https://<你的域名>/oauth/microsoft/callback
```

`MICROSOFT_CLIENT_ID` 留空即**禁用** Microsoft 接入，QQ 邮箱仍可正常使用；此时其余 Microsoft 变量会被忽略。

## 3. 发起授权

1. 在 Telegram 中向机器人发送 `/add microsoft`。
2. 机器人返回一次性授权链接（绑定发起人的 Telegram 用户 ID，默认 10 分钟有效，`MICROSOFT_SESSION_TTL` 可调）。
3. 在浏览器打开链接并同意授权。
4. 回调成功后账号自动创建并启用，归属绑定到发起授权的用户。

## 4. 安全说明

- 服务端从不保存明文令牌；凭据以 AES-256-GCM 加密后写入数据库，密钥来自 `ENCRYPTION_KEY`。
- state 与 PKCE 校验、一次性会话消费、过期校验均在服务端完成。
- 分页链接与重定向来源被固定校验，防止令牌被转发到外部主机。
- 授权失败不会写入任何可用账号。
- 撤销授权：在 Microsoft 账户的「隐私 / 应用和 services」中移除该应用，并在机器人中用 `/remove <accountID>` 停用账号。

## 5. 常见问题

| 现象 | 原因 | 处理 |
|---|---|---|
| 启动报 `MICROSOFT_REDIRECT_URL: must share the PUBLIC_BASE_URL origin` | 回调地址与 `PUBLIC_BASE_URL` 不同源 | 改为同一域名与协议 |
| 授权回调失败或账号要求重新授权 | 授权码过期或已被使用 | 重新执行 `/add microsoft` |
| 同步报 `ErrReauthorization` | 刷新令牌失效（改密、撤销、长期未用） | 重新授权 |
| 收不到个人邮箱 | 账户类型未选 `consumers` | 修改应用注册或调整 `MICROSOFT_TENANT` |

官方参考：[授权码与 PKCE](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-auth-code-flow)、[Graph Immutable ID](https://learn.microsoft.com/en-us/graph/outlook-immutable-id)、[文件夹与本地化 well-known 名称](https://learn.microsoft.com/en-us/graph/api/resources/mailfolder?view=graph-rest-1.0)。
