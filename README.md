# BD2 本地服务器

用于本地开发、协议研究和服务端实现练习。

首先安装 Brown Dust II 客户端。

## 开发构建

开发时在两个终端中分别运行：

```powershell
.\bd2w runServer
```

```powershell
.\bd2w runClient
```

客户端窗口在插件准备完成后打开。首次生成完整游戏源码或者在 SDK / 游戏更新后可能等待数分钟，后续启动复用共享缓存。

将 `go/config.example.json` 改名为 `go/config.json`，将 `game_directory` 改为本机 Brown Dust II 安装目录。仍可在 `run` 后使用 `--game-dir` 临时覆盖。

发布脚本统一使用 `-tags release` 编译。

## 发布包

```powershell
.\bd2w build
.\bd2w build -GameDir 'D:\Brown Dust II' -SkipTests
.\bd2w build -SchedulesOnly
```

默认执行测试与静态检查；`-SkipTests` 跳过这些检查；`-SchedulesOnly` 只导出活动日历编排信息。

## 其他命令

在仓库根目录运行：

```powershell
.\bd2w check-csharp
.\bd2w check-csharp LoginUI GameNames
.\bd2w sdk pack --game-dir '<客户端目录>'
.\bd2w sdk verify --game-dir '<客户端目录>'
.\bd2w sdk update-names --game-dir '<客户端目录>' --game-mapping '<映射文件>'
.\bd2w deobfuscate --source '<Assembly-CSharp目录>' --mapping '<映射文件>' --output '<源码镜像目录>'
.\bd2w diff --a '<旧源码目录>' --b '<新源码目录>' --out '<报告目录>/report.txt'
.\bd2w download-client --package-dir '<客户端发布包目录>'
.\bd2w download-resources --output '<资源目录>' --bundle-version '<资源版本>' --game-data-version '<GameData版本>'
```

## 客户端插件项目

- `plugins/LocalIdentity/`：本地服务端客户端专用插件。
- `plugins/LoginUI/`：本地服 Discord/Google 登录界面插件。
- `plugins/CashShop`: 内购商品处理。
- `plugins/CaptureEnvironment/`：独立原版对照客户端的抓包插件，不要装进本地服务端客户端。
- `plugins/GameNames`/`plugins/GameSdk`: 插件开发工具链，让开发过程中使用反混淆名改善开发体验，只有 `GameNames` 需要随同安装。

## 可视化客户端工具

BD2 Launcher 是使用 Wails 的独立桌面游戏启动器。Windows 使用系统 WebView2 Runtime，macOS 使用系统 WKWebView。它提供以下操作：

- 选择并验证 Brown Dust II 安装目录。
- 填写服务器 origin 和端口，自动写入游戏目录下的 `BepInEx/config/bd2.client.json`。
- 应用带原文件备份的客户端入口补丁。
- 检查 BepInEx 后安装或更新 `BD2LocalIdentity.dll`、`BD2LoginUI.dll`、`BD2CashShop.dll` 及其共享运行时 `BD2.GameNames.dll`。
- 选择官方 CDN、服务器资源源或本地已下载资源。

服主同步官方资源、自建静态 CDN 或配置缓存反代的步骤见 [资源与 CDN 配置指南](RESOURCES.md)。

## 首次安装

### 启动服务端

双击 `bd2server.exe` 启动，单人本地服默认监听 `8080` 端口，可通过 `.\bd2server.exe serve --listen 127.0.0.1:xxxx` 修改监听端口。

服主部署、创建 OAuth 应用、取得 client ID/secret、配置回调地址等完整步骤见 [服主配置指南](AUTHENTICATION.md)。

`authentication.json`、`resources.json` 和 `game.json` 不随发布包分发，首次运行时在数据目录的 `config/` 下生成默认文件（默认为 `bd2server.exe` 旁的 `data/config/`），已有文件不会覆盖。开发入口使用 `.build/config/` 中的三个配置。配置修改后重启生效，详细见 [服务端游戏规则配置](GAME_CONFIGURATION.md)。

服务端自带网页管理台，地址为 `<服务器地址>/admin/`，可以可视化修改上述配置、给玩家寄送物品和调整背包容量，详细见 [网页管理台](ADMIN_CONSOLE.md)。

启动器和服务端会自动检查新版本；服务端更新可在网页管理台“版本更新”页或用 `bd2server update status|check|apply|cancel` 应用，应用前会通知在线玩家并保存存档。游戏内“新闻”公告来自签名的 [更新日志](CHANGELOG.md)，按玩家的游戏语言显示。

### 在 Linux 服务器上用 Docker 部署

[`tools/server/bd2-host.sh`](tools/server/bd2-host.sh) 负责 Docker 主机上的目录与密钥初始化、更新交接单元安装、镜像构建、容器切换和就绪检查。手动部署直接使用指定目录的当前源码（包括未提交的改动），或指定的 Docker 镜像，与 Git 提交、推送无关。需要 Docker（含 Compose 插件）、jq、curl、flock、nsenter、realpath 和 openssl，以 root 运行。从源码目录安装并部署：

```bash
sudo sh tools/server/bd2-host.sh setup "$PWD"
```

脚本安装到 `/opt/bd2/bd2-host.sh`，数据在 `/opt/bd2/data`，Compose 文件首次从 [模板](tools/server/compose.yaml) 生成为 `/opt/bd2/compose.yaml`（默认只在本机 `8080` 端口提供服务，请放在 HTTPS 反向代理之后）。之后：

```bash
sudo /opt/bd2/bd2-host.sh deploy /path/to/bd2  # 构建该目录当前源码
sudo /opt/bd2/bd2-host.sh deploy --image bd2server:my-build
sudo /opt/bd2/bd2-host.sh status
sudo /opt/bd2/bd2-host.sh rollback   # 换回上次切换前的镜像；存档不会回滚
```

省略源码路径时，`deploy` 使用 `/opt/bd2/source` 中已有的源码。指定镜像时，优先使用本机镜像，不存在则拉取，并以解析出的不可变镜像 ID 切换。首次安装若没有相邻的 Compose 模板和本地源码，脚本会用 Git 从 `SOURCE_URL` 获取初始源码。

在管理台应用的服务端更新也由同一脚本完成交接，继续使用经过验证的发布请求；发布提供固定摘要的镜像时无需 Git，按源码更新时才获取对应提交。路径、源码地址和 Compose 文件可在 `/etc/bd2/host.conf` 中以 `BD2_DIR=`、`SOURCE_URL=`、`COMPOSE_FILE=`、`SERVICE=`、`READY_PORT=`、`SEED_CONFIG_DIR=` 修改。不用 Docker 时，可使用 [systemd 单元模板](tools/server/bd2server.service)。

服务端日志默认输出 INFO 及以上级别，仅终端启用颜色：TRACE 灰、DEBUG 青、INFO 绿、WARN 黄、ERROR 红。可用 `serve --log-level debug --log-color auto` 调整。级别支持 `trace/debug/info/warn/error`，颜色支持 `auto/always/never`。`auto` 下文件和重定向保持纯文本，`NO_COLOR` 或 `TERM=dumb` 也会关闭颜色；如需在 Docker 日志流中显示颜色，可显式选择 `always`。

### 启动客户端

打开我们的客户端工具 `bd2client.exe`。

进入客户端工具后首先要求你选择游戏根目录，也就是有 `BrownDust II.exe` 文件的目录，然后要填写服务端 url，如果是单人本地服则填写 `127.0.0.1:<服务端实际监听的端口>`，默认为 `8080`。如果为联机服则需要填写服主要求的 url。


## 许可与免责声明

本项目原创内容由 Flechazo 保留全部权利，具体条款见 [LICENSE](LICENSE)。通过项目权利人指定渠道合法取得副本的人，仅可用于个人学习、研究和本地非商业测试；未经项目权利人事先明确书面许可，禁止向第三方转发、镜像、上传、二次分发项目源码或构建产物，禁止售卖、收费提供、商业化运营或对公众开放基于本项目的服务。公开可见不代表允许转载或再分发。

本项目是非官方开发与研究项目，与 Brown Dust II 及其权利人无隶属或授权关系。游戏名称、商标、客户端、游戏资源和数据归各自权利人所有；本项目的许可不覆盖它们，也不变更第三方依赖的许可证。项目按现状提供，无正确性、安全性、可用性或不侵权保证。使用者须自行确认适用法律、相关协议和第三方权利要求，并自行承担使用、客户端修改及本地服务运行风险。
