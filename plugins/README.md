# 客户端插件工程

- `LocalIdentity/`：连接根目录 `versions.json` 所选客户端与本地服务器；构建产物为 `BD2LocalIdentity.dll`。
- `LoginUI/`：把客户端登录面板收敛为 Discord 与 Google 两个本服认证入口；构建产物为 `BD2LoginUI.dll`。
- `CaptureEnvironment/`：独立原版对照客户端抓包使用；构建产物为 `BD2CaptureEnvironment.dll`。

`LocalIdentity` 与 `LoginUI` 用于本地服客户端；`CaptureEnvironment` 用于独立原版抓包环境。原版抓包环境不得安装 `BD2LocalIdentity.dll` 或 `BD2LoginUI.dll`。
客户端、资源与插件版本都来自根目录 `versions.json`；MSBuild 在中间目录生成 C# 常量，源码不保存第二份版本值。

构建时必须显式传入对应客户端目录：

```powershell
dotnet build .\plugins\LocalIdentity\LocalIdentity.csproj -c Release -p:GameDir="<本地服客户端目录>"
dotnet build .\plugins\LoginUI\LoginUI.csproj -c Release -p:GameDir="<本地服客户端目录>"
dotnet build .\plugins\CaptureEnvironment\CaptureEnvironment.csproj -c Release -p:GameDir="<原版客户端目录>"
```

认证策略由服务端同目录的 `authentication.json` 决定。`mode=local` 保持本地自动登录；公网或联机服可设为 `oauth`，并在 `providers` 中开启 `discord`、`google` 或两者。LoginUI 从当前连接的服务器读取这项策略，客户端不能自行启用服务端未开放的 provider。

OAuth access token 只保存在客户端进程内存中。自动登录 refresh credential 按规范化服务器 origin 隔离：Windows 使用当前用户作用域的 DPAPI 加密，再把密文按 origin 哈希键写入 PlayerPrefs 对应的用户注册表；macOS 使用系统 Keychain Services。没有受支持安全凭据存储的平台会禁用自动登录，不会回退到明文文件或 PlayerPrefs。除 Windows 的 DPAPI 密文外，PlayerPrefs 只保存原版的 `IsAutoLogin` 与 `StandaloneAutoLogin` 非敏感选择。

启动原版抓包环境：

```powershell
.\tools\python\start-official-capture.ps1 `
  -GameDir "<原版客户端目录>" `
  -LocalClientDir "<本地服客户端目录>"
```

用户数据共享目录默认从当前 Windows 用户目录推导；特殊环境可另外传入 `-SharedDataDir`。
