# BD2.GameSdk

给 Brown Dust II 插件作者使用的 C# 开发包。通过标准 NuGet `PackageReference` 导入，自动完成 **生成可读引用程序集和全量源码导航 → 编译插件 → reobf → 验证运行 DLL**。源码可以使用可读类型和成员，字符串反射由配套的 `BD2.GameNames` 处理。转到定义可查看完整反编译方法体。

## 包与版本

| NuGet 包 | 内容 | 使用方式 |
| --- | --- | --- |
| `BD2.GameSdk` | .NET 8 构建工具、`build/*.props/targets`、压缩名字表 | 插件项目直接引用，`PrivateAssets="all"` |
| `BD2.GameNames` | `lib/netstandard2.0/BD2.GameNames.dll`、XML API 文档、同一份内嵌表 | SDK 固定依赖对应版本，自动引入；DLL 随插件部署 |

当前包版本为 `0.2.1-game.2.35.10`：`0.2.1` 是开发工具/API 的语义版本，`game.2.35.10` 指定游戏版本。采用 SemVer 的 prerelease 段，安装时明确指定版本。升级游戏后更新包并重新构建插件；相同游戏版本下的不同官方 DLL 也会因指纹不同而被拒绝。

包不包含游戏 DLL 或可读壳。维护者用官方映射生成名字表，提交为 `plugins/GameNames/Mappings/names.json.gz`，SDK 和运行时从同一文件嵌入，插件作者只需要对应游戏客户端和 BepInEx，无需官方映射、Python、仓库 `versions.json` 或本仓库源码。

包当前由维护者提供 `.nupkg` 或 NuGet 源，**尚未发布到 nuget.org**。包内包含仓库 LICENSE，许可条款沿用项目现有授权，不声明为开源许可。

## 导入到自己的插件项目

前提：安装 **.NET 8 SDK**，准备与包对应的游戏目录，并在该目录安装 BepInEx。推荐 SDK-style `.csproj` 与 `netstandard2.1`；运行时库本身兼容 `.NET Standard 2.0`。旧式 `packages.config` 不受支持。

将维护者提供的两个 `.nupkg` 放在一个目录，例如 `D:\NuGet\BD2`，添加为本地 NuGet 源：

```powershell
dotnet nuget add source 'D:\NuGet\BD2' --name BD2
dotnet add MyPlugin.csproj package BD2.GameSdk --version 0.2.1-game.2.35.10
```

Visual Studio / Rider 也可在 NuGet 包管理界面添加该源，打开“包含预发布版本”，安装指定版本。若维护者提供远程 NuGet 源，把上面的目录换成该源 URL。

最小项目文件：

```xml
<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>netstandard2.1</TargetFramework>
    <LangVersion>latest</LangVersion>
    <AssemblyName>MyPlugin</AssemblyName>
  </PropertyGroup>
  <ItemGroup>
    <PackageReference Include="BD2.GameSdk"
                      Version="0.2.1-game.2.35.10"
                      PrivateAssets="all" />
  </ItemGroup>
</Project>
```

`PrivateAssets="all"` 让构建步骤只用于当前插件项目；它不会阻止运行时 DLL 复制到输出目录。SDK 自动引入 `BD2.GameNames`，以及游戏目录中的 `BepInEx`、`0Harmony`、`UnityEngine`、`UnityEngine.CoreModule` 引用，无需手写 `<Import>`。**不要再引用真实的 `Assembly-CSharp.dll`**，游戏 API 编译引用由 SDK 提供。

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

IDE 项目加载/设计时构建会准备可读引用；CLI 首次构建同样会自动生成。使用 SDK 不需要运行额外的生成命令。完整可复制示例在 [samples/ExamplePlugin](samples/ExamplePlugin)，包含 `.csproj`、插件源码和本机配置示例。

## 完整源码导航

导入包、设置 `GameDir` 后，先完成一次构建。SDK 使用 ILSpy 从匹配版本的真实游戏 DLL 生成**全部类型的可读 C# 源码**，保留方法体、私有成员、嵌套类型和编译器生成实现；同步生成 Portable PDB，将源码内嵌到 PDB。名字仍来自同一份内嵌表，没有另一份源码映射。

在支持外部源码导航的 IDE 中，对 `NetworkManager.Send` 等可读 API 使用“转到定义”（Visual Studio 的 F12），即可打开完整方法体。重载、泛型、参数、属性、字段、事件和嵌套类型使用真实元数据及 PDB 定位，不靠搜索同名字符串。

- **Visual Studio 2022**：在“工具 → 选项 → 文本编辑器 → C# → 高级”中启用“导航到 Source Link 和嵌入源”（不同语言/版本的名称可能略有差异）。如果仍打开旧的签名视图，关闭原外部源码页并重新加载项目后再 F12。只阅读源码不需要关闭“仅我的代码”调试设置。
- **Rider**：启用外部代码的源文件/PDB 或反编译导航。可读引用保留完整 IL，IDE 也可直接反编译出方法体；本项目的自动测试验证了 Visual Studio 的 Roslyn PDB 导航引擎，尚未手动验证 Rider 各版本界面。
- 项目中还会链接一个 **`Game Sources`** 文件夹，支持直接浏览和文本搜索。首次生成后若文件夹未出现，重新加载项目使生成的 MSBuild 导入生效。这些 `.cs` 是 `None` 项，**不参与插件编译，也不复制到部署目录**。

普通视图恢复 `async/await`、迭代器和 lambda，便于阅读；`generated` 视图补全被这些语法隐藏的状态机、访问器等，`metadata` 视图补全其他隐藏声明。无法作为 C# 标识符的 CLR 生成名在源码中显示为 `__generated_...`，`navigation.json` 和名字表仍保留真实元数据身份。

首次全量生成会花费数分钟，日志持续报告已处理的类型数。本机 2.35.10 的一次验证生成了 **24,856 个源码文件、654,265 个声明、319,350 个带方法体的符号**。生成结果按名字表、生成器及其依赖、全部游戏 Managed DLL 的指纹缓存，同一台机器上的插件项目共用；并行构建会等待同一个缓存生成完成。后续构建不再执行全量反编译。

默认共享缓存路径为 `%LOCALAPPDATA%\BD2\GameSdk\navigation`。可用环境变量改变位置：

```powershell
$env:BD2_GAME_SDK_CACHE = 'D:\Caches\BD2.GameSdk'
dotnet build MyPlugin.csproj -c Release '-p:GameDir=E:\Games\BrownDustII'
```

保持这个路径稳定；更改路径、工具或游戏 DLL 后首次构建会重新准备缓存。删除 `obj` 不影响共享源码缓存；缺失的缓存源码可在下次准备时从 PDB 恢复。可删除共享缓存来强制重新生成，它不存放手写源码。

插件 `obj/<配置>/<框架>/bd2-game-sdk` 中的布局遵循 .NET 的引用程序集查找约定（仓库插件使用 `game-sdk` 子目录）：

| 路径 | 用途 |
| --- | --- |
| `ref/Assembly-CSharp.Readable.dll` | 编译器引用，带引用程序集标记，禁止执行 |
| `lib/Assembly-CSharp.Readable.dll` + `.pdb` | IDE 对应实现和导航符号；源码内嵌，不依赖联网下载 |
| `GameSourceNavigation.props` | 把共享源码链接到项目浏览器 |
| `navigation.json` | 元数据 token 到文件/行列的全量索引、覆盖统计与 DLL/PDB 指纹；schema 2 的文件路径相对于 `SourceRoot` |

`lib` 中的程序集仅供开发导航，**不要部署或执行**。这里展示的是从当前 DLL 重建的源码，局部变量名和语法可能与开发商原始工程不同。导航 PDB 对应可读程序集；它不能用来在真实混淆游戏 DLL 上逐行调试。

## 插件代码

```csharp
using System;
using BD2.GameNames;
using BepInEx;
using HarmonyLib;

[BepInPlugin("example.my-plugin", "My Plugin", "1.0.0")]
public sealed class MyPlugin : BaseUnityPlugin
{
    private void Awake()
    {
        try
        {
            Game.Validate(typeof(MyPlugin).Assembly,
                message => Logger.LogInfo(message));

            var target = Game.Method<IntroUI>(ui => ui.SendMaintenanceInfo(false));
            new Harmony("example.my-plugin").Patch(target,
                prefix: new HarmonyMethod(typeof(MyPlugin), nameof(BeforeMaintenance)));
        }
        catch (Exception exception)
        {
            Logger.LogError("My Plugin initialization failed: " + exception);
        }
    }

    private static void BeforeMaintenance() { }
}
```

表达式只取得 `MethodInfo`，不会执行游戏调用；签名由 C# 编译器检查。已知游戏类型优先使用 `typeof`，公开成员优先使用强类型表达式或 `nameof`，可以获得 IDE 补全和编译检查。实际公开调用和字段访问也能直接使用可读名字。私有成员保持原访问性，通过已知类型和映射反射 API 查询：

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

`GetGameMethod/GetGameField/GetGameProperty/GetGameEvent` 采用 .NET 反射约定：查不到返回 null，重载歧义抛出 `AmbiguousMatchException`，null 名字参数抛出 `ArgumentNullException`。`MemberName` 的成员种类使用 `GameMemberKind` 枚举。未知映射保留字面名；普通 UI 文案、`nameof` 字符串和协程常量都不会被 reobf 自动替换。

`Game.FindType` 保留给运行时才知道类型名称的查询。只做字符串反射的项目可单独安装 `BD2.GameNames`，启动时用 `Game.ValidateGame(...)`。这种方式没有可读游戏类型引用，也没有编译指纹；使用 SDK 的项目必须用 `Game.Validate(typeof(MyPlugin).Assembly, ...)`。

## 构建与部署

构建成功后，将 `bin/Release/netstandard2.1/MyPlugin.dll` 和相邻的 **`BD2.GameNames.dll`** 安装到游戏 `BepInEx/plugins`。客户端只安装一份共享库；多个插件应使用同一游戏表。不要部署 `obj`、可读壳、源码、导航 PDB、`GameSdk.dll`、`Mono.Cecil.dll`、`ICSharpCode.Decompiler.dll` 或其他构建工具。

最终插件 DLL 已回映射，`obj` 保留可读编译产物和 PDB，最终目录删除改写前的旧 PDB。SDK 包是构建依赖；玩家电脑不需要 .NET 8 SDK 或 NuGet。这里支持 Unity 的 Mono/Managed 客户端，要求存在真实 `Assembly-CSharp.dll`；IL2CPP/AOT 客户端不受支持。

| MSBuild 属性 | 用途 |
| --- | --- |
| `GameDir` | 游戏根目录，常规项目唯一必填配置 |
| `BD2ManagedDir` | 自定义 Managed 路径，默认 `GameDir/BrownDust II_Data/Managed` |
| `BD2BepInExDir` | 自定义 BepInEx 路径，默认 `GameDir/BepInEx` |
| `BD2GameSdkCache` | 共享导航缓存目录，可在 `Directory.Build.props` 配置；默认取 `BD2_GAME_SDK_CACHE` 环境变量或用户缓存目录 |
| `BD2AddBepInExReferences=false` | 使用其他宿主或自己提供 BepInEx/Harmony 引用时关闭自动引用 |
| `BD2AddUnityReferences=false` | 自己提供 Unity 引用时关闭自动引用 |
| `BD2GameSdkEnabled=false` | 暂时关闭壳生成与 reobf；只适用于不依赖可读游戏 API 的项目 |

额外 Unity 模块或第三方游戏依赖仍以普通 `<Reference>` 添加，并设 `<Private>false</Private>`。强签名插件需要重新签名流程，当前 reobf 明确拒绝。CI 用相同的 PackageReference 和 `GameDir` 参数构建，直接分发 Build 的 DLL 产物；不要把原始 `obj` DLL 放进发布包。

## 维护者打包

在本仓库运行：

```powershell
.\plugins\GameSdk\Pack.ps1 `
  -GameDir '<当前客户端目录>'
```

默认生成 `.build/nuget/BD2.GameSdk.<版本>.nupkg` 和 `BD2.GameNames.<版本>.nupkg`。工具版本来自 `plugins/PackageMetadata.props`，游戏版本来自根目录 `versions.json`，组合成 `工具版本-game.游戏版本`。可传 `-PackageVersion` 和 `-OutputDirectory`；发布后的同一包版本必须保持内容不可变，有任何变更都递增工具版本。

SDK 精确依赖同版本运行时包；两个包中的压缩表都直接来自同一生成结果。工具通过 `dotnet publish` 打包自带 Mono.Cecil，不把 Cecil 作为插件的 NuGet 依赖。包内携带 README、LICENSE、仓库地址和作者信息。本脚本只生成本地包，不上传到任何 NuGet 服务。

工具包也包含 Mono.Cecil、ILSpy 和相关 .NET 组件的 MIT 授权原文 `THIRD-PARTY-NOTICES.txt`；第三方组件保持其自身许可。打包后运行 `VerifyPackages.ps1 -GameDir '<客户端目录>'`，它在仓库外的全新目录和 NuGet 缓存中构建示例插件，检查运行时复制、禁止部署的文件、回映射引用、完整内嵌表、源码/PDB 覆盖和重复构建。

打包工程在普通 IDE 加载/源码构建时直接引用 `GameNames.csproj`，无需先生成包，也不会到 nuget.org 查找未发布的 `BD2.GameNames`。`Pack.ps1` 用 `BD2Packaging=true` 切换到同版本的精确 NuGet 依赖，并把打包用 `obj/bin` 放在独立 staging 目录，避免 IDE restore 与 pack 互相覆盖依赖资产。不要直接对打包 `.csproj` 执行 `dotnet pack`，使用脚本才能保证两个包版本配套。

仓库中的 `samples/ExamplePlugin/NuGet.Config` 指向 `.build/nuget`，示例保持真正的 NuGet 消费方式；先运行 `Pack.ps1` 再构建示例。复制示例到其他目录时按前文配置自己的包源，不要照搬仓库相对路径。

## 仓库源码构建与内嵌表

仓库的三个插件与 NuGet 消费项目采用同样的内嵌表流程。开发启动命令保持不变：

公共配置见 `plugins/Directory.Build.props`，本机游戏安装位置见不提交的 `plugins/Directory.Build.local.props`。可从 `Directory.Build.local.props.example` 复制并设置 `BD2LocalGameDir`、`BD2CaptureGameDir`、可选的 `BD2GameSdkCache`。配置和目标框架确定后，由 `Directory.Build.targets` 计算中间目录并导入 SDK/版本构建步骤。

仓库插件通过普通 `ProjectReference` 使用共享运行时；IDE 设计时加载不会嵌套构建三份运行时。SDK 准备过程按输出目录加锁，导航 `.props` 只在内容改变时原子替换，重复设计时构建不会因重写该文件触发连续项目加载。设计时构建不执行 reobf。

```powershell
# 从 go 目录运行；游戏目录来自 go/config.json
go run .\cmd\bd2client --dev run
```

直接构建源码插件也只需要游戏目录：

```powershell
dotnet build plugins/LocalIdentity/LocalIdentity.csproj -c Release '-p:GameDir=<客户端目录>'
```

构建工具从自身资源导出表到 `obj` 缓存，用本机真实 DLL 的元数据生成壳，再做回映射。`GameSdk` 和 `BD2.GameNames` 的唯一名字数据源是仓库内的 `GameNames/Mappings/names.json.gz`。两份程序集内嵌的是同一份压缩字节，不维护第二份映射；`obj` 里的表只是可以删除重建的缓存。NuGet 包同样不再包含单独的 `tools/data` 表文件。

表包含 `game_version`、完整类型名、成员声明类型/签名/metadata token、参数映射、真实 DLL 的 MVID/SHA-256 和官方映射 SHA-256。构建时先验证 DLL 指纹，仓库构建还核对 `versions.json`；不匹配会要求更新 SDK，不会尝试使用其他版本或猜名字。插件启动时核对表指纹、游戏 DLL 和少量已知条目。

## 更新游戏版本的名字表

只有维护者更新 SDK 名字数据时需要官方 `.obfuscate`。先更新仓库 `versions.json`，然后执行：

```powershell
.\plugins\GameSdk\UpdateNames.ps1 `
  -GameDir '<新版本客户端目录>' `
  -GameMapping '<ObfuscationTranslation_新版本.obfuscate>'
```

脚本从官方映射和真实元数据生成表，完成全量壳转换验证后才替换 `plugins/GameNames/Mappings/names.json.gz`。提交这份表与对应版本变更；重新构建插件并发布新版本配套包。更新过程中产生的明文表和壳留在 `.build/names-update`，不作为源码提交。

`tools/python/deobfuscate_client_source.py` 继续生成阅读镜像；程序集映射使用这里的元数据名字表，保留作用域、泛型位置和重载签名。官方映射冲突或可读签名冲突会报错。

工具也提供独立命令：

```powershell
dotnet build plugins/GameSdk/GameSdk.csproj -c Release
$tool = 'plugins/GameSdk/bin/Release/net8.0/GameSdk.dll'
dotnet $tool prepare-embedded '<Assembly-CSharp.dll>' '<输出目录>'
dotnet $tool export-names '<导出的 names.json>'
dotnet $tool reobf '<names.json>' '<可读插件.dll>' '<运行插件.dll>' '<Assembly-CSharp.dll>' '<BepInEx/core>'
dotnet $tool verify '<names.json>' '<运行插件.dll>' '<Assembly-CSharp.dll>'
dotnet $tool verify-runtime '<names.json>' '<BD2.GameNames.dll>'
dotnet $tool verify-navigation '<生成的 SDK 目录>'
dotnet $tool self-test
```

手动编译须引用 `ref/Assembly-CSharp.Readable.dll` 并编译同目录 `GameSdkIdentity.g.cs`，保留相邻 `lib` 以供 IDE 查找。`verify-navigation` 全量检查 PE/PDB 身份、内嵌/本地源码校验和、类型文档和全部方法体的符号。

`self-test` 生成合成游戏 DLL，验证重载、泛型、继承、嵌套/编译器生成类型、私有成员、事件、参数、表达式、字符串不变和版本拒绝；它会打印合成 SDK 目录。使用实际 Roslyn 引擎验证该目录的导航：

```powershell
dotnet run --project plugins/GameSdk.NavigationTests -- '<合成 SDK 目录>' --embedded-only
# 或验证完整游戏导航：
dotnet run --project plugins/GameSdk.NavigationTests -- '<插件 obj 中的 SDK 目录>' '<游戏 Managed 目录>'
```

嵌入源码测试临时移走合成 fixture 的 `.cs`，结束时恢复；它限制在 `.build/game-sdk-tests` 下运行。测试断言类型、重载、泛型、参数、字段、事件和嵌套类型跳到准确的源码标识符，并拒绝签名/反编译回退。`VerifyPackages.ps1` 验证仓库外 NuGet 项目。实际 Unity/Harmony 行为需在游戏启动后检查日志。
