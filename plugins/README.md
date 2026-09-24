# 客户端插件工程

- `LocalIdentity/`：连接根目录 `versions.json` 所选客户端与本地服务器；构建产物为 `BD2LocalIdentity.dll`。
- `CaptureEnvironment/`：独立原版对照客户端抓包使用；构建产物为 `BD2CaptureEnvironment.dll`。

两个插件用于不同客户端环境。原版抓包环境不得安装 `BD2LocalIdentity.dll`。
客户端、资源与插件版本都来自根目录 `versions.json`；MSBuild 在中间目录生成 C# 常量，源码不保存第二份版本值。

构建时必须显式传入对应客户端目录：

```powershell
dotnet build .\plugins\LocalIdentity\LocalIdentity.csproj -c Release -p:GameDir="<本地服客户端目录>"
dotnet build .\plugins\CaptureEnvironment\CaptureEnvironment.csproj -c Release -p:GameDir="<原版客户端目录>"
```

启动原版抓包环境：

```powershell
.\tools\python\start-official-capture.ps1 `
  -GameDir "<原版客户端目录>" `
  -LocalClientDir "<本地服客户端目录>"
```

用户数据共享目录默认从当前 Windows 用户目录推导；特殊环境可另外传入 `-SharedDataDir`。
