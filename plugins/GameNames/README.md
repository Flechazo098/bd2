# BD2.GameNames

Brown Dust II 插件的共享名字解析运行时，目标框架为 `.NET Standard 2.0`。内嵌当前游戏版本的官方名字表，支持字符串反射、Harmony 目标表达式、缓存和插件启动自检。

开发插件建议引用配套的 `BD2.GameSdk` NuGet 包，它会自动引入本包并接入可读引用程序集和构建期 reobf。只有需要字符串反射、完全不引用游戏类型的插件才单独引用本包。

```xml
<PackageReference Include="BD2.GameSdk" Version="{plugin_version}-game.{game_version}" PrivateAssets="all" />
```

```csharp
using BD2.GameNames;
using System.Reflection;

var intro = Game.FindType("IntroUI");
var enter = intro?.GetGameMethod("Enter", BindingFlags.Instance | BindingFlags.NonPublic,
    null, Type.EmptyTypes, null);
```

未知名字保留字面名；查不到成员返回 null，歧义抛出 `AmbiguousMatchException`。表中私有/编译器生成成员可通过字符串通道解析，公开调用可使用 `BD2.GameSdk` 生成的壳接受编译器检查。`nameof` 和协程字符串需显式经过名字表，普通字符串不会被 reobf 改写。

首次调用 `Game` API 时自动核对内嵌表与实际游戏 DLL 的 MVID 和 SHA-256；每个进程只核对一次。配套 SDK 还会在 reobf 时自动写入模块初始化器，在插件代码执行前核对插件编译指纹与运行时名字表，无需编写启动校验代码。同一客户端只安装一份 `BD2.GameNames.dll`，指纹不匹配会抛出明确异常并阻止插件继续初始化。

单独引用本包的 SDK-style 类库在 `.csproj` 中设置 `<CopyLocalLockFileAssemblies>true</CopyLocalLockFileAssemblies>`，让运行时 DLL 复制到 `bin`；`BD2.GameSdk` 会自动设置此默认值。

游戏版本变化后必须更新配套 SDK/运行时并重新构建插件。遵循包中附带的 LICENSE；本包并非 MIT 或其他开源许可。

完整导入和构建说明：[BD2.GameSdk 文档](https://github.com/Flechazo098/bd2/blob/main/plugins/GameSdk/README.md)。
