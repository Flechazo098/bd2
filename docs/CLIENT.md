# BD2 Client Studio

BD2 Client Studio 是独立的 Windows / macOS 客户端配置工具，不是服务端，也不包含玩家存档、`auth.db`、OAuth secret 或 master key。Linux 可以运行服务端，但不提供游戏客户端工具。

Windows 使用 Wails 和系统 WebView2 Runtime 创建桌面窗口，macOS 使用 Cocoa 和系统 WKWebView。窗口由客户端进程管理；关闭窗口会退出整个工具，退出工具也会关闭窗口。Windows 首次使用前应确保已安装 Microsoft Edge WebView2 Runtime：<https://developer.microsoft.com/microsoft-edge/webview2/>。

当前本机发布脚本只生成 Windows 包。macOS 源码保留，后续找到测试者或公开发布时，将使用 GitHub Actions 的 macOS runner 进行原生构建、测试和 `.app` 打包；目前未完成 macOS 实机验证。

启动后会打开配置工作台：

1. 选择 Brown Dust II 安装目录。Windows 选择包含 `BrownDust II.exe` 的目录；macOS 可选择 `BrownDust II.app` 或它的父目录。
2. 填写要进入的服务器 origin，例如 `https://play.example.com` 或 `http://127.0.0.1:8080`。
3. 选择 CDN：
   - **官方 CDN**：玩家直接从版本锁定的官方节点下载资源，适合朋友通过 FRP 联机。
   - **本机已下载资源**：选择同时包含 `ServerData` 和 `GameData` 的 CDN 根目录；LI 插件会在游戏进程内启动仅监听 loopback 的只读资源服务。
   - **服主资源**：通过服务器 `PUT /client/resources` 取得版本锁定的资源 URL；服主自建和反代对玩家客户端是同一种模式。
4. 点击“应用客户端补丁”和“安装 / 更新插件”。

工作台的“连接设置”提供“代理 → 设置代理”入口。默认直接连接，不自动读取系统或终端环境中的代理。需要代理时，选择“手动 HTTP 代理”，填写例如 `http://127.0.0.1:12451` 的地址，点击“保存连接设置”，再通过工作台启动游戏。不要填写 Markdown 链接或 SOCKS 地址。

代理地址必须包含端口，不包含账号密码、路径或查询参数。HTTP 代理可通过 CONNECT 转发 HTTPS 请求，证书仍正常验证；本机 loopback 地址始终直连。配置同时用于客户端资源策略请求和新启动游戏的 HTTP 请求。修改代理后，需要正常退出游戏再重新启动；浏览器 OAuth 登录仍使用浏览器自身的网络设置。

客户端配置写入：

```text
Windows: <游戏目录>/BepInEx/config/bd2.client.json
macOS:   <BrownDust II.app 的父目录>/BepInEx/config/bd2.client.json
```

macOS 同时兼容把 BepInEx 放在 `BrownDust II.app/Contents/BepInEx` 的布局；若两个位置都存在，优先使用 App Bundle 外的标准 BepInEx 目录。

工具只写入：

```json
{
  "schema_version": 2,
  "server_origin": "https://play.example.com",
  "cdn_mode": "official",
  "proxy_url": "http://127.0.0.1:12451"
}
```

`proxy_url` 可选，省略或留空表示直接连接。

`bd2client.exe` 启动时严格读取同目录的 `versions.json`。`game_version` 是支持的官方游戏版本；`client_version` 与 `server_version` 是分别演进的软件发布版本，格式为 `游戏版本+client.X.Y.Z` 与 `游戏版本+server.X.Y.Z`。资源仍由 `bundle_version` 和 `game_data_version` 锁定：

- 从所选客户端的 `BrownDust II_Data/globalgamemanagers` 读取 Unity `bundleVersion`，必须精确等于 `game_version`。不能使用 `BrownDust II.exe` 的 Windows `FileVersion`，该值是 Unity 引擎版本。
- 本机资源必须包含当前 `bundle_version` 对应的 `catalog_alpha.json` / `.hash`，以及当前 `game_data_version` 对应的 `common-dbdata.info` / `.bin`。
- 服主资源接口返回的 Bundle 和 GameData 版本必须分别精确匹配，不能互相替代。
- `versions.json` 缺失、字段错误、出现未知字段或尾随 JSON 时，客户端工具拒绝启动。

本机资源模式会额外保存玩家自己选择的目录：

```json
{
  "schema_version": 2,
  "server_origin": "http://127.0.0.1:8080",
  "cdn_mode": "local",
  "local_resource_directory": "E:\\bd2\\dl"
}
```

本机路径不会发送给服务器。插件不会直接依赖各加载链对 `file://` 的兼容性，而是在游戏进程内通过随机 loopback 端口提供 `GET`、`HEAD` 和单段 Range；该端口不会暴露给局域网。

补丁只把 Intro 中的地址替换成固定的本地占位地址，并保留 `resources.assets.bak`。真实服务器地址由 `BD2LocalIdentity.dll` 从 `bd2.client.json` 读取，因此不受 Unity 内置 27-byte URL 替换限制。

选择服主资源时，服务器必须在同一版本的 `resources.json` 中启用统一的 `server` 模式；客户端会使用严格的 `PUT /client/resources` 读取版本锁定 URL。客户端不会接受模式不匹配、版本不匹配、非 HTTPS（loopback 除外）或带凭据的资源地址。

界面语言按系统时区自动选择：中国大陆、香港、澳门和台湾地区使用简体中文，其他地区使用英文。诊断日志始终使用英文；Windows 保存在 `bd2client.exe` 同目录的 `logs/bd2client.log`，macOS 保存在 `~/Library/Logs/BD2 Client Studio/bd2client.log`。单文件最大 `2 MiB`，并只保留一个 `bd2client.log.1` 备份；日志不会写入 UI session、OAuth token 或 secret。

工具需要玩家事先安装 BepInEx。服务端不再安装客户端插件，也不需要知道玩家的 Windows 游戏目录。关闭游戏后再执行补丁或插件安装。

补丁与插件安装均可重复安全执行。Intro 已经指向当前占位地址时不会再次改写 `resources.assets`；两个已安装 DLL 与发布包内容一致时不会再次复制，并会明确提示已经完成或已经是最新版本。连接配置仍会按当前界面设置进行原子保存。

工作台右上角的“启动游戏”会再次验证所选客户端及精确版本，然后通过 Windows 进程启动或 macOS Launch Services 打开游戏。macOS 不会打开 Terminal。
