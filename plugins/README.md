# 客户端插件工程

对外开发库采用 NuGet `PackageReference` 导入；第三方插件无需本仓库源码或官方映射。导入、项目配置、Harmony/字符串 API 示例及部署步骤见 [BD2.GameSdk 使用说明](GameSdk/README.md)。维护者运行 `GameSdk/Pack.ps1` 生成 `BD2.GameSdk` 和 `BD2.GameNames` 两个版本配套的 `.nupkg`。

- `LocalIdentity/`：连接根目录 `versions.json` 所选客户端与本地服务器；构建产物为 `BD2LocalIdentity.dll`。
- `LoginUI/`：把客户端登录面板收敛为 Discord 与 Google 两个本服认证入口；构建产物为 `BD2LoginUI.dll`。
- `CashShop/`：把现金商品改为服务端核算的付费钻石购买；充值商品遵守 `game.json` 的兑换配置，产物为 `BD2CashShop.dll`。
- `CaptureEnvironment/`：独立原版对照客户端抓包使用；构建产物为 `BD2CaptureEnvironment.dll`。

`LocalIdentity`、`LoginUI` 与 `CashShop` 用于本地服客户端；`CaptureEnvironment` 用于独立原版抓包环境。原版抓包环境不得安装 `BD2LocalIdentity.dll`、`BD2LoginUI.dll` 或 `BD2CashShop.dll`。
客户端、资源与插件版本都来自根目录 `versions.json`；MSBuild 在中间目录生成 C# 常量，源码不保存第二份版本值。

SDK 和共享运行时已内嵌当前游戏版本的同一份名字表。源码构建只需客户端目录，无需提供官方映射。`.NET 8 SDK` 构建工具；插件为 `netstandard2.1`，运行时为 `netstandard2.0`。构建自动生成可读引用、完整源码和内嵌源码 PDB，编译插件、执行 reobf 并验证真实 DLL 的引用。首次生成耗时数分钟，三个插件和 Debug/Release 共用本机缓存；IDE 转到定义可以查看方法体，`Game Sources` 文件夹提供全量浏览与搜索。IDE 设置见 [完整源码导航](GameSdk/README.md#完整源码导航)。

仓库公共路径和默认值集中在 `plugins/Directory.Build.props`；配置/框架相关的输出路径与构建步骤由 `Directory.Build.targets` 自动导入。三个插件 `.csproj` 保留插件属性、明确的 `<BD2GameVersion>2.35.10</BD2GameVersion>` 和需要的程序集引用。目标游戏版本必须由每个插件声明，缺失或与 SDK 表不符时构建报错。首次开发复制 `Directory.Build.local.props.example` 为 `Directory.Build.local.props`，填写 `BD2LocalGameDir` 和可选的 `BD2CaptureGameDir`；本机文件被 Git 忽略。配置后 IDE 与命令行共用这些路径，不需要在三个子目录分别放一份 `Directory.Build.props`。命令行 `-p:GameDir=...` 仍可覆盖本机设置。

`BD2GameSdkCache` 可放在本机 props 中指定共享源码缓存。特殊目录布局可以设置 `BD2ManagedDir`、`BD2BepInExDir`；`BD2SdkDir` 可覆盖中间产物目录。新增子目录 `Directory.Build.props` 会阻断 MSBuild 自动查找父文件，确有需要时应显式导入公共 `plugins/Directory.Build.props`。

```powershell
dotnet build .\plugins\LocalIdentity\LocalIdentity.csproj -c Release -p:GameDir="<本地服客户端目录>"
dotnet build .\plugins\LoginUI\LoginUI.csproj -c Release -p:GameDir="<本地服客户端目录>"
dotnet build .\plugins\CashShop\CashShop.csproj -c Release -p:GameDir="<本地服客户端目录>"
dotnet build .\plugins\CaptureEnvironment\CaptureEnvironment.csproj -c Release -p:GameDir="<原版客户端目录>"
```

`bin` 中的插件是回映射后的运行产物，部署时同时复制相邻的 `BD2.GameNames.dll`。可读程序集、PDB、源码和名字表统一保存在 `.build/game-sdk/<游戏版本>/<内容指纹>`，各插件 `obj` 只留小型指针和生成配置，不部署。客户端开发入口 `go run .\cmd\bd2client --dev run` 自动使用内嵌表；发布和原版抓包启动脚本也携带共享库。

唯一名字数据源为 `GameNames/Mappings/names.json.gz`。只有维护者更新游戏版本的表时才需要官方 `.obfuscate`，执行 `GameSdk/UpdateNames.ps1`，验证后提交新表，再发布新版本 SDK。

开发方式、工具命令、字符串与编译期引用的边界见 [GameSdk/README.md](GameSdk/README.md)。现有 `tools/python/deobfuscate_client_source.py` 继续生成阅读镜像；它的全局标识符替换不作为程序集映射表使用。

认证策略由服务端同目录的 `authentication.json` 决定。`mode=local` 保持本地自动登录；公网或联机服可设为 `oauth`，并在 `providers` 中开启 `discord`、`google` 或两者。LoginUI 从当前连接的服务器读取这项策略，客户端不能自行启用服务端未开放的 provider。

OAuth access token 只保存在客户端进程内存中。自动登录 refresh credential 按规范化服务器 origin 隔离：Windows 使用当前用户作用域的 DPAPI 加密，再把密文按 `BD2OAuthRefreshV1_<origin 哈希>` 键写入 PlayerPrefs 对应的用户注册表；macOS 使用系统 Keychain Services。没有受支持安全凭据存储的平台会禁用自动登录，不会回退到明文文件或 PlayerPrefs。

自动登录选择也按服务器 origin 隔离，使用 `BD2LoginV1_IsAutoLogin_<origin 哈希>` 和 `BD2LoginV1_StandaloneAutoLogin_<origin 哈希>`。LocalIdentity 接管原版 `PlatformManager.IsAutoLogin` 与 `UseAutoLoginPC` 的全部读写，原有勾选框、确认和清除登录流程使用这些独立键；LoginUI 使用同一套键。scheme、主机和非默认端口区分服务器，主机大小写、默认端口和尾部斜杠不产生不同状态。插件不读取、覆盖或删除官方 `IsAutoLogin`、`StandaloneAutoLogin`、`AccessToken`，LocalIdentity 的启动标识通过 getter 在内存中提供。旧版写入的官方键不自动迁移或清理，避免影响官方账号；升级后各服务器需要重新选择自动登录。

启动原版抓包环境：

```powershell
.\tools\python\start-official-capture.ps1 `
  -GameDir "<原版客户端目录>" `
  -LocalClientDir "<本地服客户端目录>"
```

用户数据共享目录默认从当前 Windows 用户目录推导；特殊环境可另外传入 `-SharedDataDir`。
