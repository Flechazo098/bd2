# BD2 本地服务器

用于本地开发、协议研究和服务端实现练习。

首先安装 Brown Dust II 客户端。

## 开发构建

```powershell
# Go
Push-Location .\go
go test ./...
go build .\cmd\bd2server
go build .\cmd\bd2client
Pop-Location
```

开发时在两个终端中分别从 `go` 目录运行：

```powershell
go run .\cmd\bd2server --dev run
```

```powershell
go run .\cmd\bd2client --dev run
```

服务端开发入口会自动使用仓库根目录的版本、认证、资源、数据和存档配置；客户端开发入口会直接使用仓库版本清单和插件产物，并从本机开发配置读取游戏目录，随后用 SDK 内嵌的当前版本名字表增量构建两个客户端插件，无需提供官方映射文件。首次开发前创建该配置：

客户端窗口在插件准备完成后打开。首次生成完整游戏源码或 SDK/游戏更新后可能等待数分钟，终端会实时显示构建和反编译进度；后续启动复用共享缓存。

```powershell
Copy-Item .\go\config.example.json .\go\config.json
```

编辑 `go/config.json`，将 `game_directory` 改为本机 Brown Dust II 安装目录。仍可在 `run` 后使用 `--game-dir` 临时覆盖；显式参数的优先级高于开发配置。

发布脚本统一使用 `-tags release` 编译。

## 发布包

```powershell
.\build-release.ps1
```

脚本在 Windows 本机构建版本化归档，例如 `.build\bd2server-2.35.10+server.0.1.0-windows-x64.zip` 和 `.build\bd2client-2.35.10+client.0.1.0-windows-x64.zip`。纯服务端包只包含 `bd2server.exe`、seed、认证/资源策略、游戏规则配置和服务端数据目录；客户端包只包含 `bd2client.exe`、客户端插件和版本文件。

macOS 客户端使用系统 Cocoa / WKWebView，需要 macOS 原生构建环境。本地 Windows 发布脚本不会交叉编译或生成 macOS 发布包；找到 macOS 测试者或准备公开发布时，再通过 GitHub Actions 的 macOS runner 完成原生构建、测试及 `.app` 打包。签名和公证也应在该流程中配置。

## 客户端插件项目

- `plugins/LocalIdentity/`：本地服务端客户端专用插件。
- `plugins/LoginUI/`：本地服 Discord/Google 登录界面插件。
- `plugins/CaptureEnvironment/`：独立原版对照客户端的抓包插件，不要装进本地服务端客户端。

## 可视化客户端工具

BD2 Client Studio 是使用 Wails 的独立桌面客户端设置工具。Windows 使用系统 WebView2 Runtime，macOS 使用系统 WKWebView；应用自己管理窗口和退出，原生窗口关闭后整个客户端进程会退出。它提供以下操作：

- 选择并验证 Brown Dust II 安装目录。
- 填写服务器 origin 和端口，自动写入游戏目录下的 `BepInEx/config/bd2.client.json`。
- 应用带原文件备份的客户端入口补丁。
- 检查 BepInEx 后安装或更新 `BD2LocalIdentity.dll`、`BD2LoginUI.dll` 及其共享运行时 `BD2.GameNames.dll`。
- 选择官方 CDN、服务器资源源或本地已下载资源。

服主同步官方资源、自建静态 CDN 或配置缓存反代的步骤见 [资源与 CDN 配置指南](RESOURCES.md)。

## 首次安装

### 启动服务端

双击 `bd2server.exe` 启动，单人本地服默认监听 `8080` 端口，可通过 `.\bd2server.exe serve --listen 127.0.0.1:xxxx` 修改监听端口。

服主部署、创建 OAuth 应用、取得 client ID/secret、配置回调地址和安全注入环境变量的完整步骤见 [服主配置指南](AUTHENTICATION.md)。


认证策略由 `bd2server.exe` 同目录的 `authentication.json` 权威决定。`mode=local`（默认）不依赖第三方登录，单人本地服保持不动，联机服可设为 `oauth` 来让玩家通过 Discord/Google 登录。

游戏规则统一使用服务端同目录的 `game.json`，修改后重启生效；开发入口读取仓库根目录的文件。联动 UR 专武是否加入 UR 必得装备券池及升级保留配置的方法见 [服务端游戏规则配置](GAME_CONFIGURATION.md)。

服务端日志默认输出 INFO 及以上级别，仅终端启用颜色：TRACE 灰、DEBUG 青、INFO 绿、WARN 黄、ERROR 红。可用 `serve --log-level debug --log-color auto` 调整，或设置 `BD2_LOG_LEVEL` 和 `BD2_LOG_COLOR` 环境变量；显式命令行参数优先。级别支持 `trace/debug/info/warn/error`，颜色支持 `auto/always/never`。`auto` 下文件和重定向保持纯文本，`NO_COLOR` 或 `TERM=dumb` 也会关闭颜色；如需在 Docker 日志流中显示颜色，可显式选择 `always`。

### 启动客户端

先手动安装 [BepInEx](https://github.com/BepInEx/BepInEx/releases)。然后打开我们的客户端工具 `bd2client.exe`。

进入客户端工具后首先要求你选择游戏根目录，也就是有 `BrownDust II.exe` 文件的目录，然后要填写服务端 url，如果是单人本地服则填写 `127.0.0.1:<服务端实际监听的端口>`，默认为 `8080`。如果为联机服则需要填写服主要求的 url。


## 许可与免责声明

本项目原创内容由 Flechazo 保留全部权利，具体中英双语条款见 [LICENSE](LICENSE)。通过项目权利人指定渠道合法取得副本的人，仅可用于个人学习、研究和本地非商业测试；未经项目权利人事先明确书面许可，禁止向第三方转发、镜像、上传、二次分发项目源码或构建产物，禁止售卖、收费提供、商业化运营或对公众开放基于本项目的服务。公开可见不代表允许转载或再分发。

本项目是非官方开发与研究项目，与 Brown Dust II 及其权利人无隶属或授权关系。游戏名称、商标、客户端、游戏资源和数据归各自权利人所有；本项目的许可不覆盖它们，也不变更第三方依赖的许可证。项目按现状提供，无正确性、安全性、可用性或不侵权保证。使用者须自行确认适用法律、相关协议和第三方权利要求，并自行承担使用、客户端修改及本地服务运行风险。
