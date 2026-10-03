# BD2.GameNames

Brown Dust II 插件的共享名字解析运行时，目标框架为 `.NET Standard 2.0`。内嵌当前游戏版本的官方名字表，支持字符串反射、Harmony 目标表达式、缓存和插件启动自检。

开发插件建议引用配套的 `BD2.GameSdk` NuGet 包，它会自动引入本包并接入可读引用程序集和构建期 reobf。只有需要字符串反射、完全不引用游戏类型的插件才单独引用本包。

```xml
<PackageReference Include="BD2.GameSdk" Version="0.2.0-game.2.35.10" PrivateAssets="all" />
```

```csharp
using BD2.GameNames;
using System.Reflection;

Game.Validate(typeof(MyPlugin).Assembly, message => Logger.LogInfo(message));
var intro = Game.FindType("IntroUI");
var enter = intro?.GetGameMethod("Enter", BindingFlags.Instance | BindingFlags.NonPublic,
    null, Type.EmptyTypes, null);
```

未知名字保留字面名；查不到成员返回 null，歧义抛出 `AmbiguousMatchException`。表中私有/编译器生成成员可通过字符串通道解析，公开调用可使用 `BD2.GameSdk` 生成的壳接受编译器检查。`nameof` 和协程字符串需显式经过名字表，普通字符串不会被 reobf 改写。

单独引用本包的插件使用 `Game.ValidateGame` 校验游戏和内嵌表；`Game.Validate` 还会校验 SDK 在插件中写入的编译指纹。运行环境只需要共享 DLL，无需 .NET SDK、Mono.Cecil 或构建工具。同一客户端只安装一份 `BD2.GameNames.dll`，不同表指纹的插件会在启动时明确报错。

单独引用本包的 SDK-style 类库在 `.csproj` 中设置 `<CopyLocalLockFileAssemblies>true</CopyLocalLockFileAssemblies>`，让运行时 DLL 复制到 `bin`；`BD2.GameSdk` 会自动设置此默认值。

包版本使用 `工具语义版本-game.游戏版本`，例如 `0.2.0-game.2.35.10`；游戏版本由 `Game.GameVersion` 获取。游戏版本变化后必须更新配套 SDK/运行时并重新构建插件。遵循包中附带的 LICENSE；本包并非 MIT 或其他开源许可。

完整导入和构建说明：[BD2.GameSdk 文档](https://github.com/Flechazo098/bd2/blob/main/plugins/GameSdk/README.md)。包目前由仓库打包脚本生成，通过维护者提供的 NuGet 源或本地目录安装。
