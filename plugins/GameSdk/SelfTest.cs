using System.Diagnostics;
using System.Runtime.Loader;
using Mono.Cecil;
using Mono.Cecil.Cil;

namespace BD2.GameSdk;

/// <summary>End-to-end tests with synthetic binaries. Never execute the production game or readable shell.</summary>
internal static class SelfTest
{
    private static void Assert(bool condition, string message)
    {
        if (!condition) throw new InvalidOperationException("Self-test: " + message);
    }
    private static void Build(string project, params string[] properties)
    {
        var start = new ProcessStartInfo("dotnet") { RedirectStandardOutput = true, RedirectStandardError = true };
        foreach (string arg in new[] { "build", project, "--nologo", "-c", "Release" }.Concat(properties)) start.ArgumentList.Add(arg);
        using var process = Process.Start(start);
        var stdout = process.StandardOutput.ReadToEndAsync(); var stderr = process.StandardError.ReadToEndAsync();
        process.WaitForExit();
        string output = stdout.GetAwaiter().GetResult() + stderr.GetAwaiter().GetResult();
        if (process.ExitCode != 0) throw new InvalidOperationException(output);
    }
    private static string Project(string name, string references = "") => $"""
        <Project Sdk="Microsoft.NET.Sdk">
          <PropertyGroup><TargetFramework>netstandard2.1</TargetFramework><LangVersion>latest</LangVersion><AssemblyName>{name}</AssemblyName></PropertyGroup>
          <ItemGroup>{references}</ItemGroup>
        </Project>
        """;
    internal static void Run()
    {
        string repository = Path.GetFullPath(Path.Combine(AppContext.BaseDirectory, "../../../../../"));
        Assert(File.Exists(Path.Combine(repository, "versions.json")), "run from a built GameSdk project");
        string root = Path.Combine(repository, ".build", "game-sdk-tests", Guid.NewGuid().ToString("N"));
        string gameDir = Path.Combine(root, "game"), sdkDir = Path.Combine(root, "sdk"), probeDir = Path.Combine(root, "probe");
        Directory.CreateDirectory(gameDir); Directory.CreateDirectory(probeDir);
        string exported = Path.Combine(root, "embedded", "names.json");
        Program.ExportNames(exported);
        Assert(File.ReadAllBytes(exported + ".gz").SequenceEqual(Program.EmbeddedNames()), "SDK exports the exact embedded table");
        Assert(Program.ReadTable(exported).schema_version == 1, "embedded names have supported schema");
        string gameProject = Path.Combine(gameDir, "Game.csproj");
        File.WriteAllText(gameProject, Project("Assembly-CSharp"));
        File.WriteAllText(Path.Combine(gameDir, "Game.cs"), """
            using System;
            public class AppManager { public bool IsPlatformLogin { get { return true; } } }
            public class IntroUI { public void SendMaintenanceInfo(bool force) {} }
            namespace BDNetwork { public class NetworkManager { public string GetPachedGameDataPath() { return "path"; } } }
            namespace Readable {
              public interface IThing { string Name { get; } }
              public class Agent : IThing {
                string IThing.Name { get { return "agent"; } }
                public event EventHandler Changed;
                public int Value;
                public int Ping(int value) { return value; }
                public string Ping(string value) { return value; }
                public void Act() {}
                public void Awake() {}
                private void PauseRoutine() {}
                private int secret = 3;
                public class State {}
                public T Echo<T>(T value) { return value; }
                public Agent[,] Grid(Agent[,] value) { return value; }
                public void Ref(ref Agent value) {}
              }
              public class Box<T> { public T Item; public T Echo(T value) { return value; } public class Nested<U> { public U Item; } }
              public class Derived : Box<Agent> {}
            }
            """);
        Build(gameProject);
        string gamePath = Path.Combine(gameDir, "bin/Release/netstandard2.1/Assembly-CSharp.dll");
        string mappingPath = Path.Combine(root, "ObfuscationTranslation_9.8.7.obfuscate"), versionPath = Path.Combine(root, "versions.json");
        File.WriteAllText(versionPath, "{\"game_version\":\"9.8.7\"}");
        var rows = new List<string> { "#ReverseOrder", "#Classes", "#Methods", "#Fields", "#Properties", "#Events", "#Parameters" };
        using (var game = Program.ReadModule(gamePath))
        {
            var table = new BD2.GameNames.Internal.NameTable { assembly_name = "Assembly-CSharp" };
            var defs = game.GetTypes().ToArray(); int counter = 0;
            string Obfuscated() => "ὠ" + string.Concat((counter++).ToString().Select(c => (char)('ὠ' + c - '0')));
            var newNames = new Dictionary<TypeDefinition, string>();
            string Full(TypeDefinition t) => t.DeclaringType != null ? Full(t.DeclaringType) + "+" + newNames[t] : newNames.TryGetValue(t, out var n) ? n : Program.TypeKey(t);
            foreach (var t in defs)
                if (t.Namespace == "Readable" || t.DeclaringType?.Namespace == "Readable")
                {
                    newNames[t] = Obfuscated();
                    string meaning = t.DeclaringType == null ? Program.TypeKey(t) : newNames[t.DeclaringType] + "/" + (t.Name == "State" ? "<Run>d__0" : t.Name);
                    rows.Add(newNames[t] + "⇨" + meaning);
                }
            foreach (var t in defs) table.types.Add(new BD2.GameNames.Internal.TypeName { token = t.MetadataToken.ToInt32(), original = Program.TypeKey(t), readable = Full(t) });
            foreach (var t in defs)
            {
                void Rename(IMemberDefinition member, string kind, string signature = null, MethodDefinition method = null)
                {
                    bool renamed = member.Name != ".ctor" && member.Name != "Awake" && t.Name != "<Module>";
                    string original = member.Name, target = renamed ? Obfuscated() : original;
                    if (renamed) rows.Add(target + "⇨" + original);
                    var entry = new BD2.GameNames.Internal.MemberName { token = member.MetadataToken.ToInt32(), declaring_type = Program.TypeKey(t), original = original, readable = target, kind = kind, signature = signature };
                    if (method != null) foreach (var p in method.Parameters)
                    {
                        if (string.IsNullOrEmpty(p.Name)) continue;
                        string name = Obfuscated(); rows.Add(name + "⇨" + p.Name);
                        entry.parameters.Add(new BD2.GameNames.Internal.ParameterName { position = p.Index, original = p.Name, readable = name });
                    }
                    table.members.Add(entry);
                }
                foreach (var m in t.Methods) Rename(m, "method", Program.Signature(m), m);
                foreach (var f in t.Fields) Rename(f, "field", Program.Signature(f));
                foreach (var p in t.Properties) Rename(p, "property");
                foreach (var e in t.Events) Rename(e, "event", Program.TypeKey(e.EventType));
            }
            Program.Rewrite(game, table, true);
            var entries = table.members.ToDictionary(m => m.token);
            foreach (var t in defs)
            {
                foreach (var m in t.Methods)
                {
                    var entry = entries[m.MetadataToken.ToInt32()]; m.Name = entry.readable;
                    foreach (var p in entry.parameters) m.Parameters[p.position].Name = p.readable;
                }
                foreach (var f in t.Fields) f.Name = entries[f.MetadataToken.ToInt32()].readable;
                foreach (var p in t.Properties) p.Name = entries[p.MetadataToken.ToInt32()].readable;
                foreach (var e in t.Events) e.Name = entries[e.MetadataToken.ToInt32()].readable;
                Program.SetTypeName(t, table.types.Single(e => e.token == t.MetadataToken.ToInt32()).readable);
            }
            game.Write(gamePath);
        }
        File.WriteAllLines(mappingPath, rows);
        bool embeddedMismatchRejected = false;
        try { Program.PrepareEmbedded(gamePath, Path.Combine(root, "mismatched-embedded")); }
        catch (InvalidDataException ex) { embeddedMismatchRejected = ex.Message.Contains("does not match SDK names for game"); }
        Assert(embeddedMismatchRejected, "embedded table rejects another game binary without seeking an external mapping");
        Program.Prepare(gamePath, mappingPath, versionPath, sdkDir);
        SourceNavigation.Verify(sdkDir);
        string tablePath = Path.Combine(sdkDir, "names.json");
        var generated = Program.ReadTable(tablePath);
        Assert(generated.types.Any(t => t.readable == "Readable.Agent+<Run>d__0"), "compiler-generated scoped type");
        using (var shell = Program.ReadModule(Path.Combine(sdkDir, Program.ShellName + ".dll")))
        {
            Assert(shell.Assembly.CustomAttributes.Any(a => a.AttributeType.Name == "ReferenceAssemblyAttribute"), "reference assembly marker");
            Assert(shell.GetType("Readable.Agent").Methods.First(m => m.Name == "Ping").Body.Instructions.Any(i => i.OpCode == OpCodes.Ret), "readable IL retained for offline decompiler fallback");
            Assert(shell.GetType("Readable.Agent").Methods.Count(m => m.Name == "Ping") == 2, "overload names restored");
        }
        string runtimeProject = Path.Combine(repository, "plugins/GameNames/GameNames.csproj"), runtimeBin = Path.Combine(root, "runtime-bin") + Path.DirectorySeparatorChar;
        Build(runtimeProject, "-p:GameNamesTable=" + tablePath, "-p:BaseIntermediateOutputPath=" + Path.Combine(root, "runtime-obj") + Path.DirectorySeparatorChar, "-p:OutputPath=" + runtimeBin);
        string runtimePath = Path.Combine(runtimeBin, "BD2.GameNames.dll");
        string references = $"<Compile Include=\"{System.Security.SecurityElement.Escape(Path.Combine(sdkDir, "GameSdkIdentity.g.cs"))}\" /><Reference Include=\"{Program.ShellName}\"><HintPath>{System.Security.SecurityElement.Escape(Path.Combine(sdkDir, "ref", Program.ShellName + ".dll"))}</HintPath><Private>false</Private></Reference><Reference Include=\"BD2.GameNames\"><HintPath>{System.Security.SecurityElement.Escape(runtimePath)}</HintPath></Reference>";
        string probeProject = Path.Combine(probeDir, "Probe.csproj");
        File.WriteAllText(probeProject, Project("Probe", references));
        File.WriteAllText(Path.Combine(probeDir, "Probe.cs"), """
            using System;
            using System.Collections.Generic;
            using System.Reflection;
            using BD2.GameNames;
            using Readable;
            public sealed class MarkerAttribute : Attribute { public Type Target; public MarkerAttribute(Type target) { Target = target; } }
            [Marker(typeof(Box<Agent>))]
            public class Probe {
              private static void Check(bool condition, string name) { if (!condition) throw new Exception(name); }
              public static string Run() {
                var log = new List<string>();
                Game.Validate(typeof(Probe).Assembly, "9.8.7", log.Add);
                Game.Validate(typeof(Probe).Assembly, log.Add);
                Game.ValidateGame(log.Add);
                var a = new Agent(); a.Value = 42;
                Check(a.Value == 42 && a.Ping(7) == 7 && a.Ping("ok") == "ok", "fields and overloads");
                Check(((IThing)a).Name == "agent", "explicit interface property");
                a.Changed += (s, e) => {};
                Check(typeof(Probe).GetCustomAttribute<MarkerAttribute>().Target == typeof(Box<Agent>), "custom attribute type arguments");
                Check(a.Echo(a) == a && new Box<Agent>().Echo(a) == a, "generic methods/types");
                var box = new Box<Agent>(); box.Item = a; Check(box.Item == a, "generic field");
                var derived = new Derived(); derived.Item = a; Check(derived.Echo(a) == a && derived.Item == a, "inherited generic members");
                var nested = new Box<Agent>.Nested<string>(); nested.Item = "nested"; Check(nested.Item == "nested", "nested generic field");
                var grid = new Agent[1,1]; grid[0,0] = a; Check(a.Grid(grid)[0,0] == a, "array pseudo methods");
                a.Ref(ref a);
                Check(typeof(Derived).BaseType == typeof(Box<Agent>), "generic base");
                Check(Game.Getter<AppManager, bool>(x => x.IsPlatformLogin).Invoke(new AppManager(), null) is true, "property expression");
                Check(Game.Method<Agent>(x => x.Act()).Name != "Act", "Harmony method expression");
                Check(nameof(Agent.Ping) == "Ping", "nameof is unchanged");
                const BindingFlags all = BindingFlags.Public | BindingFlags.NonPublic | BindingFlags.Instance | BindingFlags.Static;
                var ping = typeof(Agent).GetGameMethod(nameof(Agent.Ping), all, null, new[] { typeof(int) }, null);
                Check((int)ping.Invoke(a, new object[] { 12 }) == 12, "overloaded runtime lookup");
                Check(Game.ParameterName(ping, "value") == ping.GetParameters()[0].Name, "parameter mapping");
                Check(typeof(Derived).GetGameMethod("Echo", all, null, new[] { typeof(Agent) }, null).Invoke(derived, new object[] { a }) == a, "inherited generic runtime method");
                Check(typeof(Derived).GetGameField("Item", all).GetValue(derived) == a, "inherited generic runtime field");
                bool ambiguous = false;
                try { typeof(Agent).GetGameMethod("Ping", all); } catch (AmbiguousMatchException) { ambiguous = true; }
                Check(ambiguous, "ambiguous overloads require a signature");
                Check(Game.MemberName(typeof(Agent), "PauseRoutine") != "PauseRoutine", "coroutine string mapping");
                Check(typeof(Agent).GetGameField("secret", all).GetValue(a) is 3, "private field");
                Check(Game.MemberName(typeof(Agent), "secret", GameMemberKind.Field) != "secret", "typed member kind");
                bool nullRejected = false;
                try { Game.TypeName(null); } catch (ArgumentNullException) { nullRejected = true; }
                Check(nullRejected, "null input uses .NET exception contract");
                Check(typeof(Agent).GetGameEvent("Changed", all).Name != "Changed", "event string mapping");
                Check(Game.FindType("Readable.Agent+<Run>d__0").DeclaringType == typeof(Agent), "generated nested type");
                Check(typeof(Agent).GetGameMethod("Awake", all).Name == "Awake", "Unity literal fallback");
                Check(Game.TypeName("Unmapped.Type") == "Unmapped.Type" && Game.MemberName(typeof(Agent), "Missing") == "Missing", "unknown literal fallback");
                Check(typeof(string).GetGameMethod("Contains", all, null, new[] { typeof(string) }, null) != null, "external member unaffected");
                bool rejected = false;
                try { Game.Validate(typeof(Probe).Assembly, "wrong", log.Add); } catch { rejected = true; }
                Check(rejected && log.Exists(s => s.Contains("SELF-CHECK FAILED")), "version mismatch diagnosed");
                return "All SDK/runtime integration probes passed; UI Ping remains Ping";
              }
              public static Agent[] Signature(Agent[] value) { return value; }
            }
            """);
        Build(probeProject);
        string readablePlugin = Path.Combine(probeDir, "bin/Release/netstandard2.1/Probe.dll"), pluginPath = Path.Combine(root, "Probe.dll");
        using (var plugin = Program.ReadModule(readablePlugin))
        {
            var foreign = new TypeReference("Readable", "Agent", plugin, new AssemblyNameReference("Foreign", new Version(1, 0)));
            var holder = new TypeDefinition("", "ExternalHolder", TypeAttributes.Public, plugin.TypeSystem.Object);
            holder.Fields.Add(new FieldDefinition("Other", FieldAttributes.Public, foreign)); plugin.Types.Add(holder);
            plugin.Write(readablePlugin);
        }
        Program.Reobfuscate(tablePath, readablePlugin, pluginPath, gamePath);
        using (var rewritten = Program.ReadModule(pluginPath))
        {
            Assert(rewritten.GetType("ExternalHolder").Fields[0].FieldType.FullName == "Readable.Agent", "external assembly scope isolation");
            Assert(!rewritten.AssemblyReferences.Any(a => a.Name == "System.Private.CoreLib"), "target framework preserved");
            Assert(Strings(readablePlugin).SequenceEqual(Strings(pluginPath)), "all ldstr operands preserved byte-for-byte");
        }
        var context = new AssemblyLoadContext("BD2 GameSdk self-test", isCollectible: true);
        context.Resolving += (_, name) => name.Name switch
        {
            "Assembly-CSharp" => context.LoadFromAssemblyPath(gamePath),
            "BD2.GameNames" => context.LoadFromAssemblyPath(runtimePath),
            _ => null
        };
        try { Console.WriteLine(context.LoadFromAssemblyPath(pluginPath).GetType("Probe").GetMethod("Run").Invoke(null, null)); }
        finally { context.Unload(); }
        string wrongTable = Path.Combine(root, "wrong-names.json");
        generated.game_version = "wrong";
        File.WriteAllText(wrongTable, System.Text.Json.JsonSerializer.Serialize(generated, Program.Json));
        bool mismatchRejected = false;
        string protectedOutput = Path.Combine(root, "protected-output.dll");
        File.WriteAllText(protectedOutput, "untouched");
        try { Program.Reobfuscate(wrongTable, readablePlugin, protectedOutput); } catch (InvalidDataException) { mismatchRejected = true; }
        Assert(mismatchRejected && File.ReadAllText(protectedOutput) == "untouched", "mismatched table rejected before publishing");
        File.AppendAllText(mappingPath, "\nὠὡ⇨Conflicting.Name\n");
        bool conflictRejected = false;
        try { Program.Prepare(gamePath, mappingPath, versionPath, sdkDir); } catch (InvalidDataException) { conflictRejected = true; }
        Assert(conflictRejected, "ambiguous mapping rejected");
        Console.WriteLine("Self-test artifacts: " + root);
    }
    private static string[] Strings(string path)
    {
        using var module = Program.ReadModule(path);
        return module.GetTypes().SelectMany(t => t.Methods).Where(m => m.HasBody).SelectMany(m => m.Body.Instructions).Where(i => i.OpCode == OpCodes.Ldstr).Select(i => (string)i.Operand).ToArray();
    }
    internal static void VerifyRuntime(string tablePath, string runtimePath)
    {
        var table = Program.ReadTable(tablePath);
        using (var runtime = Program.ReadModule(runtimePath))
        {
            var resource = (EmbeddedResource)runtime.Resources.Single(r => r.Name == "BD2.GameNames.names.json.gz");
            Assert(resource.GetResourceData().SequenceEqual(File.ReadAllBytes(tablePath + ".gz")), "embedded runtime table equals the shared table");
        }
        var context = new AssemblyLoadContext("BD2 full runtime-table check", isCollectible: true);
        try
        {
            var runtime = context.LoadFromAssemblyPath(Path.GetFullPath(runtimePath));
            Assert(runtime.GetExportedTypes().Select(t => t.FullName).OrderBy(n => n).SequenceEqual(new[] { "BD2.GameNames.Game", "BD2.GameNames.GameMemberKind" }), "serialization models are not public API");
            var game = runtime.GetType("BD2.GameNames.Game");
            Assert((string)game.GetProperty("GameVersion").GetValue(null) == table.game_version, "runtime deserializes the full table");
            var translate = game.GetMethod("TypeName").CreateDelegate<Func<string, string>>();
            foreach (var type in table.types) Assert(translate(type.readable) == type.original, "runtime type translation: " + type.readable);
            Assert(translate("Unknown.Type") == "Unknown.Type", "runtime literal fallback");
            Console.WriteLine($"Verified embedded runtime table: game={table.game_version}, {table.types.Count} type lookups; {runtimePath}");
        }
        finally { context.Unload(); }
    }
}
