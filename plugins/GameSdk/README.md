# BD2.GameSdk

本插件不会提供任何封装的游戏 API，任何功能的 patch 等操作需要自行处理。

给 Brown Dust II 插件作者使用的 C# 开发包。通过标准 NuGet `PackageReference` 导入，自动完成 **生成可读引用程序集和全量源码导航 → 编译插件 → reobf → 验证运行 DLL**。源码可以使用可读类型和成员，字符串反射由配套的 `BD2.GameNames` 处理。转到定义可查看完整反编译方法体。

## 本发布包

| NuGet 包 | 内容 | 使用方式 |
| --- | --- | --- |
| `BD2.GameSdk` | .NET 8 构建工具、`build/*.props/targets`、压缩名字表 | 插件项目直接引用，`PrivateAssets="all"` |
| `BD2.GameNames` | `lib/netstandard2.0/BD2.GameNames.dll`、XML API 文档、同一份内嵌表 | SDK 固定依赖对应版本，自动引入；DLL 随插件部署 |

升级游戏后更新此包并重新构建插件；相同游戏版本下的不同官方 DLL 也会因指纹不同而被拒绝。

包不包含游戏 DLL 或可读壳。用官方映射生成名字表，提交为 `plugins/GameNames/Mappings/names.json.gz`，SDK 和运行时从同一文件嵌入，插件作者只需要对应游戏客户端和 BepInEx。

包当前由维护者提供 `.nupkg` 或 NuGet 源，**尚未发布到 nuget.org**。包内包含仓库 LICENSE，许可条款沿用项目现有授权，不声明为开源许可。

## 导入到自己的插件项目

前提：安装 **.NET 8 SDK**，准备与包对应版本的游戏目录，并在该目录安装 BepInEx。推荐 SDK-style `.csproj` 与 `netstandard2.1`；运行时库本身兼容 `.NET Standard 2.0`。旧式 `packages.config` 不受支持。

将维护者提供的两个 `.nupkg` 放在一个目录，例如 `D:\NuGet\BD2`，添加为本地 NuGet 源：

```powershell
dotnet nuget add source 'D:\NuGet\BD2' --name BD2
dotnet add MyPlugin.csproj package BD2.GameSdk --version 0.2.3-game.2.35.10
```

Visual Studio / Rider 也可在 NuGet 包管理界面添加该源。

简单的一个项目文件：

```xml
<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>netstandard2.1</TargetFramework>
    <LangVersion>latest</LangVersion>
    <AssemblyName>MyPlugin</AssemblyName>
    <BD2GameVersion>2.35.10</BD2GameVersion>
  </PropertyGroup>
  <ItemGroup>
    <PackageReference Include="BD2.GameSdk" Version="0.2.3-game.2.35.10" PrivateAssets="all" />
  </ItemGroup>
</Project>
```

`PrivateAssets="all"` 让构建步骤只用于当前插件项目。SDK 自动引入 `BD2.GameNames`，以及游戏目录中的 `BepInEx`、`0Harmony`、`UnityEngine`、`UnityEngine.CoreModule` 引用，无需手写 `<Import>`。**不要再引用真实的 `Assembly-CSharp.dll`**，游戏 API 编译引用由 SDK 提供。

在项目旁创建仅保存在本机的 `Directory.Build.props`，并加入自己的 `.gitignore`：

```xml
<Project>
  <PropertyGroup>
    <GameDir>E:\Games\BrownDustII</GameDir>
  </PropertyGroup>
</Project>
```

或者构建时传入目录：

```powershell
dotnet build MyPlugin.csproj -c Release '-p:GameDir=E:\Games\BrownDustII'
```

IDE 项目加载时构建会准备可读引用，CLI 首次构建同样会自动生成。完整示例在 [samples/ExamplePlugin](samples/ExamplePlugin)。

## 完整源码导航

导入包、设置 `GameDir` 后，先完成一次构建。SDK 会使用 ILSpy 从匹配版本的真实游戏 DLL 生成**全部类型的可读 C# 源码**，保留方法体、私有成员、嵌套类型和编译器生成实现；同步生成 Portable PDB，将源码内嵌到 PDB。

在支持外部源码导航的 IDE 中，对 `NetworkManager.Send` 等可读 API 使用“转到定义”（例如 Visual Studio 的 F12），即可打开完整方法体。重载、泛型、参数、属性、字段、事件和嵌套类型使用真实元数据及 PDB 定位。

首次全量生成会花费数分钟，且体积稍微有些许庞大。本机 2.35.10 的一次验证生成了 **24,856 个源码文件、654,265 个声明、319,350 个带方法体的符号**。生成结果按名字表、生成器及其依赖、全部游戏 Managed DLL 的指纹缓存，同一台机器上的插件项目共用。

所有依赖 SDK 的插件必须在自己的 `.csproj` 明确声明目标**游戏版本**：

```xml
<BD2GameVersion>2.35.10</BD2GameVersion>
```

构建核对声明值、SDK 内嵌表、真实游戏 DLL 指纹。缺少声明或版本不匹配会报错；不会自动选用其他版本。NuGet 项目仍需安装带对应游戏版本的包。

仓库默认缓存根为 `.build/game-sdk`，与源码位于同一盘。第三方 NuGet 项目默认使用 `%LOCALAPPDATA%\BD2\GameSdk\navigation`；可在本机 `Directory.Build.props` 设置 `BD2GameSdkCache`，或设置环境变量 `BD2_GAME_SDK_CACHE`。 `obj` 不影响共享缓存。缺失的共享源码可在下次准备时从 PDB 恢复。真实游戏 DLL、映射表、游戏依赖、生成逻辑或反编译依赖改变时会生成新的缓存。

`lib` 中的程序集仅供开发导航，**不要部署或执行**。这里展示的是当前 DLL 的反编译源码，局部变量名和语法可能与原始工程不同。导航 PDB 对应可读程序集，不能用于真实混淆游戏 DLL 的逐行调试。

公开方法调用、字段和属性访问可以直接使用可读名字，获得 IDE 补全和编译检查。需要通过反射或 Harmony 定位成员时，已知游戏类型优先使用 `typeof(GameType)`，避免用字符串查找类型，可访问的成员可以使用 `nameof` 提供经过编译检查的名字，或者通过强类型表达式取得 `MethodInfo`，让编译器检查所选重载及参数类型。表达式只用于取得方法信息，不执行方法。私有成员保留原访问性，通过已知类型和映射反射 API 查询，例如：

```csharp
using System.Reflection;

var intro = typeof(IntroUI);
var enter = intro.GetGameMethod("Enter",
    BindingFlags.Instance | BindingFlags.NonPublic,
    null, Type.EmptyTypes, null);

var field = typeof(IntroUI).GetGameField("_maintenanceTimeoutCts",
    BindingFlags.Instance | BindingFlags.NonPublic);

// nameof 的结果仍是可读字符串，必须传给 GetGameMethod 等运行时 API。
var maintenance = typeof(IntroUI).GetGameMethod(nameof(IntroUI.SendMaintenanceInfo),
    BindingFlags.Instance | BindingFlags.Public, null, new[] { typeof(bool) }, null);

// 对实际协程可读名调用 MemberName，再交给 Unity 的字符串 API。
owner.StartCoroutine(Game.MemberName(owner.GetType(), "ReadableCoroutineName"));
```

`GetGameMethod/GetGameField/GetGameProperty/GetGameEvent` 采用 .NET 反射约定：查不到返回 null，重载歧义抛出 `AmbiguousMatchException`，null 名字参数抛出 `ArgumentNullException`。`MemberName` 的成员种类使用 `GameMemberKind` 枚举。未知映射保留字面名，普通 UI 文案、`nameof` 字符串和协程常量都不会被 reobf 自动替换。

`Game.FindType` 保留给运行时才知道类型名称的查询。只做字符串反射的项目可单独安装 `BD2.GameNames`，首次调用 `Game` API 时自动核对游戏 DLL 与名字表。SDK 项目在 reobf 时自动写入模块初始化器，在插件代码执行前核对插件编译指纹、运行时名字表和游戏 DLL；即使插件只使用可读类型、没有调用 `Game` API，也会自动检查，无需编写启动校验代码。

## 构建与部署

构建成功后，将你的插件产物和相邻的 **`BD2.GameNames.dll`** 安装到游戏 `BepInEx/plugins`。当然，客户端只需要安装一个`BD2.GameNames.dll`，`GameSdk.dll`只是编写时需要。

| MSBuild 属性 | 用途 |
| --- | --- |
| `BD2GameVersion` | 每个插件 csproj 必填的目标游戏版本，例如 `2.35.10`；不从 SDK 包版本或仓库版本自动推断 |
| `GameDir` | 游戏根目录，本机安装路径（另须在插件 csproj 声明 BD2GameVersion） |
| `BD2ManagedDir` | 自定义 Managed 路径，默认 `GameDir/BrownDust II_Data/Managed` |
| `BD2BepInExDir` | 自定义 BepInEx 路径，默认 `GameDir/BepInEx` |
| `BD2GameSdkCache` | 共享导航缓存目录，可在 `Directory.Build.props` 配置；默认取 `BD2_GAME_SDK_CACHE` 环境变量或用户缓存目录 |
| `BD2AddBepInExReferences=false` | 使用其他宿主或自己提供 BepInEx/Harmony 引用时关闭自动引用 |
| `BD2AddUnityReferences=false` | 自己提供 Unity 引用时关闭自动引用 |
| `BD2GameSdkEnabled=false` | 暂时关闭壳生成与 reobf；只适用于不依赖可读游戏 API 的项目 |
