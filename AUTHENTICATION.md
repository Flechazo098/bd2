# 服主配置指南

本文面向部署 `bd2server` 的服主，不面向普通玩家。

此文为极其诡异不说人话的 GPT 系列模型编写，所以如果你看不懂，那么请丢给 AI。

为避免混淆，本文统一使用以下名称：

- `provider access token`、`provider ID token`：Discord/Google 在 OAuth/OIDC 流程中返回的凭据，仅由服务端临时使用。
- `game access token`、`game refresh credential`：`bd2server` 自己签发、供玩家客户端访问本服使用的凭据。

除非明确写出 `provider`，下文提到的 access token 或 refresh credential 都指 `bd2server` 自己签发的 game credential。

客户端会受到配置结果影响，例如显示哪些按钮、登录能否成功，但客户端不保存或管理 `client_secret`、master key。

也就是说，即使你服务器上什么都有，那他们的客户端 `GET /auth/config` 只能看到：

```json
{
  "mode": "oauth",
  "providers": ["discord", "google"]
}
```

玩家点击第三方登录后，服务端会为本次登录生成一次性随机 `state`、PKCE verifier/challenge，以及 Google OIDC `nonce`。Provider callback 必须匹配对应的短期 login transaction；`state`、授权码和登录事务不能跨事务复用。服主不需要手工生成或配置这些值。

## 1. 准备公网地址

联机服务器需要一个长期稳定、玩家和 provider 都能访问的 HTTPS origin（当然，我们都自部署服务器了，公网地址肯定是有的），例如：

```text
https://bd2.example.com
```

要求：

- `public_url` 只能包含 scheme、主机和可选端口，不能带路径、查询参数、片段或用户信息。
- **bd2server 自身要求**公网 `public_url` 使用 HTTPS；只有 `localhost`、`127.0.0.1` 或 `::1` 开发环境允许 HTTP。Provider 还可能施加额外的 redirect URI 限制，最终 URI 也必须通过 Discord/Google 后台的校验。
- 反向代理或内网穿透必须把 `/auth/`、`/game/` 和 `/assets/` 全部转发到我们启动的 `bd2server`。
- 不要让代理缓存 `/auth/` 响应。
- 域名变化后，必须同时修改 `authentication.json` 和 provider 后台的 redirect URI，然后重启服务端，并告知玩家。

固定回调地址为：

```text
Discord: <public_url>/auth/discord/callback
Google:  <public_url>/auth/google/callback
```

例如：

```text
https://bd2.example.com/auth/discord/callback
https://bd2.example.com/auth/google/callback
```

## 2. 创建 Discord OAuth 应用

1. 登录 <https://discord.com/developers/applications>。
2. 选择 **New App**，为自己的服务器创建一个应用。
3. 在应用的 **OAuth2** 页面复制 **Client ID**。
4. 创建应用后立即安全保存 **Client Secret**。如果后台已不显示 secret，再使用 Discord 提供的重置/轮换功能生成新 secret，并同步更新服务端环境变量。不要为了“查看”而无意义地重置仍在使用的 secret。
5. 在 **Redirects** 中添加：

   ```text
   https://bd2.example.com/auth/discord/callback
   ```

6. 保存设置。

若重置 Discord client secret，旧 secret 会立即失效；更新服务器环境变量并重启即可，玩家客户端不需要改变。Authorization Code 流程使用的一次性 `state` 和 PKCE 由 `bd2server` 自动生成并在 callback 时校验。

## 3. 创建 Google OAuth 应用

Google 当前主要在 **Google Auth Platform → Branding / Audience / Data Access / Clients** 中管理 OAuth。控制台名称以后可能调整，但需要完成的对象不变：配置应用展示与受众，并创建一个 **Web application** 类型的 OAuth client。

1. 登录 <https://console.cloud.google.com/> 并选择或创建项目。
2. 在 **Branding** 中配置应用名称、支持邮箱等公开信息。
3. 在 **Audience** 中选择实际目标受众并确认 Publishing status：
   - **测试阶段**：保持 Testing，并把实际参加登录测试的 Google 账号加入 Test users。未列入的玩家会被 Google 拒绝。
   - **向玩家开放前**：检查 Audience、Publishing status 和 Google 显示的发布要求，并按实际受众切换到 In production。
4. 在 **Data Access** 中核对 scope。`bd2server` 当前只申请最小的 `openid`，不申请 `profile` 或邮箱 scope。
5. 在 **Clients** 中选择 **Create Client**，应用类型选择 **Web application**。不要创建 Desktop application 类型的 client：浏览器回调落在服务端 HTTPS endpoint，需要 Web application client。
6. 在 **Authorized redirect URIs** 中添加：

   ```text
   https://bd2.example.com/auth/google/callback
   ```

7. 创建后立即安全保存 Client ID 和 Client Secret。如果 Google 后台不再允许查看或下载原 secret，就按后台提供的轮换流程创建新 secret，并同步更新服务端环境变量。

## 4. 生成并保存 master key

每台服务器生成一次 32-byte 随机 master key。

Windows（PowerShell 7）：

```powershell
$keyBytes = [byte[]]::new(32)
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
try {
    $rng.GetBytes($keyBytes)
    [Convert]::ToBase64String($keyBytes)
} finally {
    $rng.Dispose()
    [Array]::Clear($keyBytes, 0, $keyBytes.Length)
}
```

macOS/Linux（Bash 或 Zsh）：

```bash
master_key="$(openssl rand -base64 32)"
printf '%s\n' "$master_key"
unset master_key
```

把输出保存进密码管理器或服务部署系统的 secret store。不要提交到 Git，也不要写进 `authentication.json`。

master key 必须长期稳定：

- 重启服务端时继续使用同一个值。
- 备份 `auth.db` 时也要单独安全备份 master key。
- master key 会派生两类用途隔离密钥：HMAC 密钥用于 provider identity、game token、登录事务 secret、OAuth `state` 和客户端地址的稳定索引；AES-256-GCM 密钥用于 PKCE verifier、OIDC `nonce` 和待领取登录结果。
- 丢失或随意更换 master key 后，已有 HMAC 索引无法再匹配，已有 AES-GCM 密文也无法解密；现有 `auth.db` 认证状态不能继续使用。
- 不要在两台互不信任的服务器之间共用 master key。

## 5. 编辑服务器配置

首次运行时会在 `bd2server` 可执行文件旁生成 `local` 模式的默认配置。开发入口使用 `.build/config/authentication.json`；Docker 镜像默认使用 `/app/data/config/authentication.json`。启用 OAuth 前编辑生成的文件并配置环境变量，然后重启。Discord 和 Google 都启用时：

```json
{
  "mode": "oauth",
  "public_url": "https://bd2.example.com",
  "master_key_env": "BD2_AUTH_MASTER_KEY",
  "providers": {
    "discord": {
      "client_id": "123456789012345678",
      "client_secret_env": "BD2_DISCORD_CLIENT_SECRET"
    },
    "google": {
      "client_id": "example.apps.googleusercontent.com",
      "client_secret_env": "BD2_GOOGLE_CLIENT_SECRET"
    }
  },
  "session": {
    "access_ttl": "15m",
    "refresh_ttl": "720h",
    "device_transaction_ttl": "10m"
  }
}
```

配置字段 `device_transaction_ttl` 以及服务端日志中可能出现的 login transaction，指 `bd2server` 自己用于“游戏客户端发起登录 → 浏览器完成 Authorization Code → 游戏客户端领取结果”的短期交接机制。它不是 Discord 或 Google 的 OAuth Device Authorization Grant。

只启用一个 provider 时，删除另一个 provider 对象即可。`providers` 是对象映射，不是字符串数组。

JSON 中可以保存 `client_id`，因为它不是秘密；只能保存 secret 所在环境变量的名称，不能保存 secret 实值。

## 6. 向服务端进程注入 secret

临时测试时，在启动 `bd2server` 的同一个 shell 中设置。

Windows（PowerShell 7）：

```powershell
$env:BD2_AUTH_MASTER_KEY = '<Base64 32-byte master key>'
$env:BD2_DISCORD_CLIENT_SECRET = '<Discord client secret>'
$env:BD2_GOOGLE_CLIENT_SECRET = '<Google client secret>'

.\bd2server.exe serve
```

macOS/Linux（Bash 或 Zsh）：

```bash
export BD2_AUTH_MASTER_KEY='<Base64 32-byte master key>'
export BD2_DISCORD_CLIENT_SECRET='<Discord client secret>'
export BD2_GOOGLE_CLIENT_SECRET='<Google client secret>'

./bd2server serve
```

如果只启用 Discord，就不需要设置 Google secret；反之亦然。服务端在 OAuth 配置或任何必需环境变量缺失时会拒绝启动。

## 7. 启动后检查

先检查服务健康。

Windows（PowerShell 7）：

```powershell
$publicUrl = 'https://bd2.example.com'
(Invoke-WebRequest -Uri "$publicUrl/healthz").Content
```

macOS/Linux（Bash 或 Zsh）：

```bash
PUBLIC_URL='https://bd2.example.com'
curl --fail --silent --show-error "$PUBLIC_URL/healthz"
```

预期输出：

```text
ok
```

再检查公开认证策略。

Windows（PowerShell 7）：

```powershell
Invoke-RestMethod -Uri "$publicUrl/auth/config" | ConvertTo-Json
```

macOS/Linux（Bash 或 Zsh）：

```bash
curl --fail --silent --show-error "$PUBLIC_URL/auth/config"
```

正常响应只应包含公开认证策略，例如 `mode` 和 provider 名称：

```json
{
  "mode": "oauth",
  "providers": ["discord", "google"]
}
```

最后再启动玩家客户端。首次进入应显示服主启用的 Discord/Google 按钮；浏览器地址应属于 Discord/Google，回调应返回配置的 `public_url`。

## 9. 常见问题

### 客户端需要填写 client ID 或 secret 吗？

不需要。客户端只读取服务端公开的 provider 列表。Client ID 由服务端构造 OAuth 授权 URL 时使用；client secret 只参与服务端与 provider token endpoint 之间的通信。浏览器中的 provider 授权 URL 会包含 client ID，这是正常行为，因为 client ID 本来就不是 secret。

### client ID 写错会破坏玩家存档吗？

这些配置错误通常不会修改 `state.db`，但可能导致认证失败、callback 无法完成、请求被送到错误 host，或 login transaction 超时。修正配置并重启服务端即可；客户端不会因此得到 client secret。

### 可以让所有服主共用项目作者的一组 client ID/secret 吗？

不建议，也不应分发共享 client secret。每位服主应创建自己的 OAuth 应用，独立控制回调域名、配额、停用和审计。共享应用会把所有服务器的信任、限流和泄漏风险绑在一起。

### 换域名需要重新编译客户端插件吗？

登录插件本身不需要重新编译，但客户端最初连接的服务端入口、`authentication.json` 的 `public_url`、反向代理以及 provider redirect URI 必须全部指向新地址并保持一致。

### 可以把服务直接暴露在公网 HTTP 上吗？

不可以。公网 OAuth 会传输一次性 device secret、登录结果和游戏凭据，必须由 HTTPS 保护。只有同机 loopback 开发测试允许 HTTP。

### 如何撤销当前客户端自动登录？

客户端退出账号时会删除当前服务器 origin 对应的安全 refresh credential。服主还可以调用服务端 session revoke 流程；不要通过删除 `state.db` 来“清登录”，玩家存档与认证数据是分开的。

### 如何重置服务器 owner？

这属于破坏性管理操作。当前没有公开的 owner 重置 API，不要手工改 SQLite。应先停止服务、完整备份并校验 `state.db`、`auth.db` 和 master key，再使用未来提供的受控管理工具。删除 `state.db` 会删除玩家存档，不是正确的认证重置方法。

## 10. 官方教程

- Discord OAuth2：<https://discord.com/developers/docs/topics/oauth2>
- Google OAuth 2.0 for Web Server Applications：<https://developers.google.com/identity/protocols/oauth2/web-server>
