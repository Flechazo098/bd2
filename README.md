# BD2 本地服务器

用于本地开发、协议研究和服务端实现练习。服务默认只监听环回地址，不应暴露到公网。

仓库根目录的 `versions.json` 是服务端与客户端插件共用的版本配置：
`client_version` 用于维护/启动响应，并约束当前 seed 与存档，
`game_data_version` 和 `bundle_version` 分别选择 GameData 与 ServerData，
`seed_directory` 相对该配置文件解析。开发时服务端会从可执行文件目录或当前目录向上查找；
也可使用 `serve --version-config <文件>` 或环境变量 `BD2_VERSION_CONFIG` 显式指定。
`plugins.local_identity`、`plugins.login_ui` 与 `plugins.capture_environment` 是三个 BepInEx 插件各自的版本。
插件构建会从仓库根配置生成临时 C# 常量，不在源码中重复版本值。

## 许可与免责声明

本项目原创内容由 Flechazo 保留全部权利，具体中英双语条款见 [LICENSE](LICENSE)。通过项目权利人指定渠道合法取得副本的人，仅可用于个人学习、研究和本地非商业测试；未经项目权利人事先明确书面许可，禁止向第三方转发、镜像、上传、二次分发项目源码或构建产物，禁止售卖、收费提供、商业化运营或对公众开放基于本项目的服务。公开可见不代表允许转载或再分发。

本项目是非官方开发与研究项目，与 Brown Dust II 及其权利人无隶属或授权关系。游戏名称、商标、客户端、游戏资源和数据归各自权利人所有；本项目的许可不覆盖它们，也不变更第三方依赖的许可证。项目按现状提供，无正确性、安全性、可用性或不侵权保证。使用者须自行确认适用法律、相关协议和第三方权利要求，并自行承担使用、客户端修改及本地服务运行风险。

## 开发构建

```powershell
# Go
Push-Location .\go
$env:GOCACHE = (Join-Path $PWD '.cache\go-build')
go test ./...
go build .\cmd\bd2server
go build .\cmd\bd2client
Pop-Location

$env:PYTHONDONTWRITEBYTECODE = '1'
python -m unittest discover -s .\tools\python\tests -p 'test_*.py' -v
```

开发时无需先整理发布目录。在两个终端中分别从 `go` 目录运行：

```powershell
$env:GOCACHE = (Join-Path $PWD '.cache\go-build')
go run .\cmd\bd2server --dev run
```

```powershell
$env:GOCACHE = (Join-Path $PWD '.cache\go-build')
go run .\cmd\bd2client --dev run
```

服务端开发入口会自动使用仓库根目录的版本、认证、资源、数据和存档配置；客户端开发入口会直接使用仓库版本清单和插件产物，并在已有游戏目录时先增量构建两个客户端插件。仍可在 `run` 后传入普通参数覆盖默认值，例如 `--listen` 或 `--game-dir`。

`--dev run` 由仅限非发布构建的 Go 文件提供。发布脚本统一使用 `-tags release` 编译，最终 `bd2client` 与 `bd2server` 不包含该入口，传入 `--dev` 会按未知参数或未知命令拒绝。

## 发布包

```powershell
.\build-release.ps1 -GameDir "<客户端目录>"
```

脚本分别生成 `.build\bd2server-windows-x64.zip` 和 `.build\bd2client-windows-x64.zip`。纯服务端包只包含 `bd2server.exe`、seed、认证/资源策略和服务端数据目录，不包含客户端 DLL、游戏补丁或客户端目录；客户端包只包含 `bd2client.exe`、客户端插件和版本文件，不包含服务端存档或 secret。Go 构建缓存只写入 `go\.cache`。

## 客户端插件项目

- `plugins/LocalIdentity/`：本地服务端客户端专用插件。
- `plugins/LoginUI/`：本地服 Discord/Google 登录界面插件。
- `plugins/CaptureEnvironment/`：独立原版对照客户端的抓包插件；不要装进本地服务端客户端。

## 可视化客户端工具

`bd2client.exe` 是独立的客户端设置工具，不会启动或依赖服务端进程。双击后会优先以 Microsoft Edge 应用窗口打开内嵌界面；系统没有 Edge 时回退到默认浏览器。它提供以下彼此独立的操作：

- 选择并验证 Brown Dust II 安装目录。
- 填写服务器 origin 和端口，并写入 `BepInEx/config/bd2.client.json`。
- 应用带原文件备份的客户端入口补丁。
- 检查 BepInEx 后安装或更新 `BD2LocalIdentity.dll` 与 `BD2LoginUI.dll`。
- 选择官方 CDN、服务器资源源或本地已下载资源；服务器资源源统一涵盖服主自建与反代，并通过 `PUT /client/resources` 公开接口预检资源 URL 和版本。

客户端配置保存服务器地址、资源模式及本地模式所需的客户端路径，不保存 OAuth secret、服主 master key 或玩家登录凭据。朋友小服或通过 FRP 联机时建议选择官方 CDN，避免让资源下载占用服主与隧道带宽。服主启用服务器资源源时，客户端插件会在运行时从该服读取资源 URL 和版本；本地资源路径不会发送到服务端。

服主同步官方资源、自建静态 CDN 或配置缓存反代的步骤见 [服主资源与 CDN 配置指南](docs/RESOURCES.md)。

开发构建后可直接运行：

```powershell
.\go\bd2client.exe
```

## 首次安装本地客户端

先手动安装 BepInEx：<https://github.com/BepInEx/BepInEx/releases>。然后修改客户端入口：

```powershell
& ".\.build\package\bd2client\bd2client.exe"
```

客户端工具会选择游戏目录、写入服务器 origin 和 CDN 模式、执行入口补丁，并安装或更新 `BD2LocalIdentity.dll` 与 `BD2LoginUI.dll`。服务端不会检查 BepInEx，也不会读取或修改客户端目录。不要把原版抓包插件 `BD2CaptureEnvironment.dll` 安装进这个客户端。

## 启动顺序

顺序必须是：

1. 确认客户端已经安装 BepInEx。
2. 启动纯服务端；服务端只准备自己的 GameData 和玩家存档。
3. 运行 `bd2client.exe` 完成客户端配置和插件安装。
4. 健康检查返回成功后，再启动游戏客户端。

启动服务器：

```powershell
& ".\.build\package\bd2server\bd2server.exe" serve
```

启动时会准备服务端逻辑所需 GameData，随后打开玩家 SQLite 存档。数据库以明确的 `schema_version` 管理格式，Go 迁移器只允许按 `N→N+1` 顺序升级，并在同一个 SQLite 事务里完成全部迁移、版本写入和最终状态校验；任一步失败都会整体回滚。服务端不需要客户端、ServerData 或 Windows 游戏目录。客户端由 `bd2client.exe` 单独配置。

认证策略由 `bd2server.exe` 同目录的 `authentication.json` 权威决定。`mode=local`（默认）不依赖第三方登录；公网或联机服可设为 `oauth`。登录界面插件从当前连接的服务器读取公开策略，只显示服务器允许的 Discord/Google 入口，客户端不能自行打开未启用的 provider。服主创建 OAuth 应用、取得 client ID/secret、配置回调地址和安全注入环境变量的完整步骤见 [服主第三方登录配置指南](docs/AUTHENTICATION.md)。

OAuth 模式示例：

```json
{
  "mode": "oauth",
  "public_url": "https://bd2.example.com",
  "master_key_env": "BD2_AUTH_MASTER_KEY",
  "providers": {
    "discord": {
      "client_id": "Discord application client ID",
      "client_secret_env": "BD2_DISCORD_CLIENT_SECRET"
    },
    "google": {
      "client_id": "Google OAuth client ID",
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

`public_url` 是客户端、浏览器和 OAuth provider 都能访问的固定 origin；公网地址必须使用 HTTPS，只有 loopback 开发地址允许 HTTP。OAuth 模式下服务端从它派生 `/game/` 地址；ServerData 与 GameData 地址由独立的 `resources.json` 决定。反向代理或内网穿透必须转发 `/auth/`、`/game/`、`/client/resources` 和健康检查；静态资源是否经过服主入口取决于所选 CDN 模式。provider 控制台的 redirect URI 必须分别注册为：

```text
https://bd2.example.com/auth/discord/callback
https://bd2.example.com/auth/google/callback
```

`authentication.json` 只保存 client ID 和环境变量名。master key 必须是 Base64 编码的 32 个随机字节；Discord/Google client secret 与 master key 的实际值只通过配置所指向的环境变量注入。master key 必须长期稳定并单独备份：更换或丢失它会使现有 `auth.db` 的身份摘要、token 摘要和待领取密文失效。服务端不会把 secret、第三方 token或游戏 token写入配置或日志。

认证数据位于玩家 `state.db` 同目录的独立 `auth.db`。access token、refresh token、device secret、OAuth state 和 provider subject 只保存带用途隔离的 HMAC-SHA-256；必须临时恢复的 PKCE verifier、OIDC nonce 和待领取登录结果使用 AES-256-GCM。provider token 只在回调内存中使用，不落盘。客户端 OAuth access token 只在进程内存中；Windows refresh credential 由当前用户 DPAPI 加密后以密文写入 PlayerPrefs 注册表，macOS 写入 Keychain Services，并按规范化服务器 origin 隔离。其他平台禁用自动登录，不会退化成明文存储。

当前版本正在先完成纯服务端与客户端工具分离；领域状态仍按现有单存档运行，真正的多玩家独立存档会在后续按 `account_id` 拆分为每账号 SQLite runtime，不会通过共享一份 `state.db` 冒充多账号。

健康检查：

```powershell
Invoke-WebRequest http://127.0.0.1:8080/healthz
```

健康检查成功后才能启动本地客户端。服务器尚未监听时启动客户端，会在登录或维护信息阶段连接失败。

客户端入口补丁和插件安装由独立的 `bd2client.exe` 完成，服务端不再提供 `patch-client` 子命令。
