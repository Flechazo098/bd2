using System.IO.Compression;
using System.Security.Cryptography;
using System.Text.Json;
using BD2.GameNames.Internal;
using Mono.Cecil;
using Mono.Cecil.Cil;

namespace BD2.GameSdk;

internal static class Program
{
    internal const string ShellName = "Assembly-CSharp.Readable";
    internal static readonly JsonSerializerOptions Json = new() { IncludeFields = true };

    private static int Main(string[] args)
    {
        try
        {
            switch (args.FirstOrDefault())
            {
                case "prepare" when args.Length == 5: Prepare(args[1], args[2], args[3], args[4]); break;
                case "prepare-embedded" when args.Length is 3 or 5:
                    if (args.Length == 5 && args[3] != "--game-version") throw new ArgumentException("Invalid prepare-embedded options");
                    PrepareEmbedded(args[1], args[2], args.Length == 5 ? args[4] : null); break;
                case "export-names" when args.Length == 2: ExportNames(args[1]); break;
                case "names" when args.Length == 5: GenerateNames(args[1], args[2], args[3], args[4]); break;
                case "shell" when args.Length == 4: GenerateShell(args[1], args[2], args[3]); break;
                case "source-navigation" when args.Length == 3: SourceNavigation.Generate(args[1], args[2]); break;
                case "verify-navigation" when args.Length == 2: SourceNavigation.Verify(args[1]); break;
                case "reobf" when args.Length is 4 or 5 or 6: Reobfuscate(args[1], args[2], args[3], args.Length >= 5 ? args[4] : null, args.Length == 6 ? args[5] : null); break;
                case "verify" when args.Length is 3 or 4: Verify(args[1], args[2], args.Length == 4 ? args[3] : null); break;
                case "verify-runtime" when args.Length == 3: SelfTest.VerifyRuntime(args[1], args[2]); break;
                case "self-test": SelfTest.Run(); break;
                default: throw new ArgumentException("Usage: prepare-embedded <Assembly-CSharp.dll> <output-dir> [--game-version version] | export-names <names.json> | names <Assembly-CSharp.dll> <mapping.obfuscate> <versions.json> <names.json> | shell <names.json> <Assembly-CSharp.dll> <output.dll> | source-navigation <readable-implementation.dll> <dependency-directory> | verify-navigation <sdk-directory> | reobf <names.json> <input.dll> <output.dll> [Assembly-CSharp.dll] [dependency-directory] | verify <names.json> <plugin.dll> [Assembly-CSharp.dll] | verify-runtime <names.json> <BD2.GameNames.dll> | self-test");
            }
            return 0;
        }
        catch (Exception ex) { Console.Error.WriteLine("BD2 GameSdk: " + ex); return 1; }
    }

    internal static string Hash(string path) => Convert.ToHexString(SHA256.HashData(File.ReadAllBytes(path))).ToLowerInvariant();
    private static string GeneratorStamp() => Hash(typeof(Program).Assembly.Location) + "|" +
        Hash(typeof(ICSharpCode.Decompiler.CSharp.CSharpDecompiler).Assembly.Location) + "|" + Hash(typeof(ModuleDefinition).Assembly.Location);
    private static string DependencyStamp(string assembly) => string.Join("|",
        Directory.EnumerateFiles(Path.GetDirectoryName(Path.GetFullPath(assembly)), "*.dll").OrderBy(p => p, StringComparer.Ordinal)
            .Select(p => Path.GetFileName(p) + ":" + Hash(p)));
    internal static NameTable ReadTable(string path) => JsonSerializer.Deserialize<NameTable>(File.ReadAllText(path), Json);
    internal static string Stamp(NameTable table) => table.game_version + "|" + table.assembly_sha256 + "|" + table.mapping_sha256;

    internal static byte[] EmbeddedNames()
    {
        using Stream stream = typeof(Program).Assembly.GetManifestResourceStream("BD2.GameNames.names.json.gz")
            ?? throw new InvalidDataException("SDK embedded names table is missing; rebuild the SDK");
        using var output = new MemoryStream();
        stream.CopyTo(output);
        return output.ToArray();
    }

    internal static void ExportNames(string tablePath)
    {
        Directory.CreateDirectory(Path.GetDirectoryName(Path.GetFullPath(tablePath)));
        byte[] compressed = EmbeddedNames();
        File.WriteAllBytes(tablePath + ".gz", compressed);
        using var gzip = new GZipStream(new MemoryStream(compressed), CompressionMode.Decompress);
        using FileStream destination = File.Create(tablePath);
        gzip.CopyTo(destination);
    }

    internal static string ResolveSdkDirectory(string directory)
    {
        string pointer = Path.Combine(directory, "shared-sdk.txt");
        return File.Exists(pointer) ? File.ReadAllText(pointer).Trim() : Path.GetFullPath(directory);
    }

    internal static void PrepareEmbedded(string assembly, string output, string expectedGameVersion = null) =>
        PrepareShared(EmbeddedNames(), assembly, output, expectedGameVersion);

    internal static void PreparePackage(string compressedTable, string assembly, string output) =>
        PrepareShared(File.ReadAllBytes(compressedTable), assembly, output, null);

    private static void PrepareShared(byte[] compressed, string assembly, string output, string expectedGameVersion)
    {
        output = Path.GetFullPath(output);
        Directory.CreateDirectory(output);
        using FileStream outputLock = AcquireCacheLock(Path.Combine(output, "prepare.lock"), "Waiting for this project's game SDK preparation to finish...");
        NameTable table;
        using (var gzip = new GZipStream(new MemoryStream(compressed), CompressionMode.Decompress))
            table = JsonSerializer.Deserialize<NameTable>(gzip, Json);
        string gameVersion = table.game_version;
        if (string.IsNullOrWhiteSpace(gameVersion) || gameVersion.Any(c => !(char.IsLetterOrDigit(c) || c is '.' or '-' or '_')))
            throw new InvalidDataException("Invalid game_version in names table");
        if (expectedGameVersion != null && expectedGameVersion != gameVersion)
            throw new InvalidDataException($"Plugin requires game {expectedGameVersion}, but SDK names target game {gameVersion}. Install the matching SDK package.");
        if (Hash(assembly) != table.assembly_sha256)
            throw new InvalidDataException("Game DLL does not match SDK names for game " + gameVersion);
        string cacheRoot = Environment.GetEnvironmentVariable("BD2_GAME_SDK_CACHE");
        if (string.IsNullOrEmpty(cacheRoot)) cacheRoot = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "BD2", "GameSdk", "navigation");
        string tableHash = Convert.ToHexString(SHA256.HashData(compressed)).ToLowerInvariant();
        string inputs = tableHash + "|" + GeneratorStamp() + "|" + DependencyStamp(assembly);
        // Game version is the public grouping; the fingerprint prevents incompatible
        // tools or binaries within one game version from overwriting active references.
        string key = Convert.ToHexString(SHA256.HashData(System.Text.Encoding.UTF8.GetBytes(inputs))).ToLowerInvariant();
        string shared = Path.Combine(Path.GetFullPath(cacheRoot), gameVersion, key);
        Directory.CreateDirectory(shared);
        using (AcquireCacheLock(Path.Combine(shared, "generation.lock")))
        {
            string tablePath = Path.Combine(shared, "names.json"), ready = Path.Combine(shared, "ready.txt");
            string[] relativeFiles = [ "names.json", "names.json.gz", ShellName + ".dll", ShellName + ".xml", "navigation.json",
                "ref/" + ShellName + ".dll", "ref/" + ShellName + ".xml", "lib/" + ShellName + ".dll", "lib/" + ShellName + ".pdb", "lib/" + ShellName + ".xml", "lib/navigation.json" ];
            string[] files = relativeFiles.Select(p => Path.Combine(shared, p)).ToArray();
            string StampFiles() => inputs + "|" + string.Join("|", files.Select(Hash));
            if (!File.Exists(ready) || !files.All(File.Exists) || File.ReadAllText(ready) != StampFiles())
            {
                File.Delete(ready);
                File.WriteAllBytes(tablePath + ".gz", compressed);
                using (var gzip = new GZipStream(new MemoryStream(compressed), CompressionMode.Decompress))
                using (FileStream destination = File.Create(tablePath)) gzip.CopyTo(destination);
                GenerateShell(tablePath, assembly, Path.Combine(shared, ShellName + ".dll"));
                File.WriteAllText(ready, StampFiles());
            }
            WriteNavigationItems(shared);
            WriteGeneratedFile(Path.Combine(output, "GameSdkIdentity.g.cs"), System.Text.Encoding.UTF8.GetBytes(
                "// Generated from the shared names table.\n[assembly: System.Reflection.AssemblyMetadataAttribute(\"BD2.GameNames\", " + JsonSerializer.Serialize(Stamp(table)) + ")]\n"));
            WriteGeneratedFile(Path.Combine(output, "shared-sdk.txt"), System.Text.Encoding.UTF8.GetBytes(shared));
            var properties = new System.Xml.Linq.XElement("PropertyGroup",
                new System.Xml.Linq.XElement("BD2SharedSdkDir", shared));
            var import = new System.Xml.Linq.XElement("Import",
                new System.Xml.Linq.XAttribute("Project", Path.Combine(shared, "GameSourceNavigation.props")),
                new System.Xml.Linq.XAttribute("Condition", "Exists('" + Path.Combine(shared, "GameSourceNavigation.props") + "')"));
            using var props = new MemoryStream();
            new System.Xml.Linq.XDocument(new System.Xml.Linq.XElement("Project", properties, import)).Save(props);
            WriteGeneratedFile(Path.Combine(output, "GameSourceNavigation.props"), props.ToArray());
        }
        // Remove only obsolete generated copies in this SDK obj directory after
        // publishing a complete shared reference. Never touch hand-written files.
        foreach (string name in new[] { "names.json", "names.json.gz", ShellName + ".dll", ShellName + ".xml", "navigation.json", "package-ready.txt", "ready.txt" })
            File.Delete(Path.Combine(output, name));
        foreach (string folder in new[] { "ref", "lib", "runtime-obj", "runtime-bin" })
        {
            string target = Path.GetFullPath(Path.Combine(output, folder));
            if (Path.GetDirectoryName(target) != output) throw new InvalidDataException("Invalid generated cleanup path");
            if (Directory.Exists(target)) Directory.Delete(target, recursive: true);
        }
        Console.WriteLine($"Shared game SDK {gameVersion}: {shared}");
    }

    private static FileStream AcquireCacheLock(string path, string message = "Waiting for the shared game source-navigation cache to finish generating...")
    {
        var elapsed = System.Diagnostics.Stopwatch.StartNew();
        bool announced = false;
        while (true)
        {
            try { return new FileStream(path, FileMode.OpenOrCreate, FileAccess.ReadWrite, FileShare.None); }
            catch (IOException) when (elapsed.Elapsed < TimeSpan.FromMinutes(30))
            {
                if (!announced) { Console.WriteLine(message); announced = true; }
                Thread.Sleep(500);
            }
        }
    }

    private static void WriteNavigationItems(string output)
    {
        SourceNavigation.Manifest manifest = JsonSerializer.Deserialize<SourceNavigation.Manifest>(File.ReadAllText(Path.Combine(output, "navigation.json")));
        if (!Directory.Exists(manifest.SourceRoot) || Directory.EnumerateFiles(manifest.SourceRoot, "*.cs", SearchOption.AllDirectories).Count() != manifest.Documents)
            SourceNavigation.RestoreSources(output);
        var items = new System.Xml.Linq.XElement("ItemGroup");
        foreach (string path in Directory.EnumerateFiles(manifest.SourceRoot, "*.cs", SearchOption.AllDirectories).OrderBy(p => p, StringComparer.Ordinal))
            items.Add(new System.Xml.Linq.XElement("None", new System.Xml.Linq.XAttribute("Include", path),
                new System.Xml.Linq.XElement("Link", "Game Sources/" + Path.GetRelativePath(manifest.SourceRoot, path)),
                new System.Xml.Linq.XElement("CopyToOutputDirectory", "Never")));
        using var stream = new MemoryStream();
        new System.Xml.Linq.XDocument(new System.Xml.Linq.XElement("Project", items)).Save(stream);
        WriteGeneratedFile(Path.Combine(output, "GameSourceNavigation.props"), stream.ToArray());
    }

    private static void WriteGeneratedFile(string path, byte[] content)
    {
        // Rewriting an imported props file triggers another IDE project reload.
        // Keep its timestamp stable when the source list has not changed.
        if (File.Exists(path) && File.ReadAllBytes(path).SequenceEqual(content)) return;
        string temporary = path + "." + Guid.NewGuid().ToString("N") + ".tmp";
        try
        {
            File.WriteAllBytes(temporary, content);
            var elapsed = System.Diagnostics.Stopwatch.StartNew();
            while (true)
            {
                try { File.Move(temporary, path, overwrite: true); break; }
                catch (IOException ex) when (ex.HResult == unchecked((int)0x80070020) && elapsed.Elapsed < TimeSpan.FromSeconds(10))
                {
                    Thread.Sleep(100);
                }
            }
        }
        finally { if (File.Exists(temporary)) File.Delete(temporary); }
    }

    private static void WriteIdentity(string tablePath, string identityPath) =>
        File.WriteAllText(identityPath, "// Generated from the shared names table.\n[assembly: System.Reflection.AssemblyMetadataAttribute(\"BD2.GameNames\", " + JsonSerializer.Serialize(Stamp(ReadTable(tablePath))) + ")]\n");
    internal static IEnumerable<TypeDefinition> Types(ModuleDefinition module) => module.GetTypes();
    internal static string TypeKey(TypeReference type) => type switch
    {
        GenericParameter p => (p.Type == GenericParameterType.Method ? "!!" : "!") + p.Position,
        GenericInstanceType g => TypeKey(g.ElementType) + "<" + string.Join(",", g.GenericArguments.Select(TypeKey)) + ">",
        ArrayType a => TypeKey(a.ElementType) + "[" + new string(',', a.Rank - 1) + "]",
        ByReferenceType b => TypeKey(b.ElementType) + "&",
        PointerType p => TypeKey(p.ElementType) + "*",
        OptionalModifierType m => TypeKey(m.ElementType) + " modopt(" + TypeKey(m.ModifierType) + ")",
        RequiredModifierType m => TypeKey(m.ElementType) + " modreq(" + TypeKey(m.ModifierType) + ")",
        FunctionPointerType f => "fnptr(" + TypeKey(f.ReturnType) + ":" + string.Join(",", f.Parameters.Select(p => TypeKey(p.ParameterType))) + ")",
        _ => type.FullName.Replace('/', '+')
    };
    internal static string Signature(MemberReference member) => member switch
    {
        MethodReference m => $"{m.GenericParameters.Count}:{TypeKey(m.ReturnType)}({string.Join(",", m.Parameters.Select(p => TypeKey(p.ParameterType)))})",
        FieldReference f => TypeKey(f.FieldType),
        _ => throw new ArgumentException("Unsupported member " + member)
    };

    internal static void Prepare(string assembly, string mapping, string versions, string output)
    {
        string version = JsonDocument.Parse(File.ReadAllText(versions)).RootElement.GetProperty("game_version").GetString();
        if (!Path.GetFileName(mapping).Contains(version, StringComparison.Ordinal))
            throw new InvalidDataException("Mapping filename must identify game_version " + version);
        Directory.CreateDirectory(output);
        string tablePath = Path.Combine(output, "names.json");
        string shellPath = Path.Combine(output, ShellName + ".dll");
        string assemblyHash = Hash(assembly), mappingHash = Hash(mapping), generatorHash = Hash(typeof(Program).Assembly.Location);
        string identityPath = Path.Combine(output, "GameSdkIdentity.g.cs"), readyPath = Path.Combine(output, "ready.txt");
        string inputStamp = version + "|" + assemblyHash + "|" + mappingHash + "|" + generatorHash;
        if (File.Exists(readyPath) && File.Exists(tablePath) && File.Exists(shellPath) && File.Exists(tablePath + ".gz") && File.Exists(identityPath))
        {
            string completeStamp = inputStamp + "|" + Hash(shellPath) + "|" + Hash(tablePath) + "|" + Hash(tablePath + ".gz") + "|" + Hash(identityPath);
            if (File.ReadAllText(readyPath) == completeStamp) return;
        }
        File.Delete(readyPath);
        GenerateNames(assembly, mapping, versions, tablePath);
        GenerateShell(tablePath, assembly, shellPath);
        WriteIdentity(tablePath, identityPath);
        File.WriteAllText(readyPath, inputStamp + "|" + Hash(shellPath) + "|" + Hash(tablePath) + "|" + Hash(tablePath + ".gz") + "|" + Hash(identityPath));
    }

    internal static void GenerateNames(string assembly, string mapping, string versions, string tablePath)
    {
        using var config = JsonDocument.Parse(File.ReadAllText(versions));
        string version = config.RootElement.GetProperty("game_version").GetString();
        if (string.IsNullOrWhiteSpace(version) || !Path.GetFileName(mapping).Contains(version, StringComparison.Ordinal))
            throw new InvalidDataException("Mapping filename must identify game_version " + version);
        var map = new Dictionary<string, string>(StringComparer.Ordinal);
        foreach (string raw in File.ReadLines(mapping))
        {
            string line = raw.Trim().TrimStart('\ufeff');
            if (line.Length == 0 || line.StartsWith('#') || line.StartsWith("//", StringComparison.Ordinal)) continue;
            string[] parts = line.Split('⇨');
            if (parts.Length != 2 || string.IsNullOrWhiteSpace(parts[0]) || string.IsNullOrWhiteSpace(parts[1]))
                throw new InvalidDataException("Invalid mapping row: " + line);
            string key = parts[0].Trim(), value = parts[1].Trim();
            if (map.TryGetValue(key, out string old) && old != value) throw new InvalidDataException("Ambiguous mapping: " + key);
            map[key] = value;
        }
        string Translate(string name)
        {
            // Dots in explicit interface members are part of the CLR name, not a scope prefix.
            if (map.TryGetValue(name, out string value)) return value.Split('/').Last();
            // Accessors can retain their CLR prefix while the property/event is renamed.
            foreach (string prefix in new[] { "get_", "set_", "add_", "remove_" })
                if (name.StartsWith(prefix, StringComparison.Ordinal) && map.TryGetValue(name[prefix.Length..], out value))
                {
                    string property = value.Split('/').Last();
                    int dot = property.LastIndexOf('.');
                    return property[..(dot + 1)] + prefix + property[(dot + 1)..];
                }
            return name;
        }
        using ModuleDefinition module = ReadModule(assembly);
        if (module.Assembly.Name.Name != "Assembly-CSharp") throw new InvalidDataException("Expected Assembly-CSharp");
        var table = new NameTable
        {
            game_version = version,
            assembly_name = module.Assembly.Name.Name,
            assembly_mvid = module.Mvid.ToString(),
            assembly_sha256 = Hash(assembly),
            mapping_sha256 = Hash(mapping),
            generator_sha256 = Hash(typeof(Program).Assembly.Location)
        };
        TypeDefinition[] definitions = Types(module).ToArray();
        var originalTypes = definitions.ToDictionary(t => t, TypeKey);
        string ReadableType(TypeDefinition t)
        {
            if (t.DeclaringType != null) return ReadableType(t.DeclaringType) + "+" + Translate(t.Name);
            if (map.TryGetValue(t.Name, out string full)) return full.Replace('/', '+');
            return string.IsNullOrEmpty(t.Namespace) ? Translate(t.Name) : t.Namespace + "." + Translate(t.Name);
        }
        foreach (TypeDefinition type in definitions)
            table.types.Add(new TypeName { token = type.MetadataToken.ToInt32(), original = originalTypes[type], readable = ReadableType(type) });
        if (table.types.GroupBy(t => t.readable).Any(g => g.Count() > 1)) throw new InvalidDataException("Readable type collision");
        var memberKeys = new HashSet<string>(StringComparer.Ordinal);
        foreach (TypeDefinition type in definitions)
        {
            void Add(IMemberDefinition member, string kind, string signature = null, MethodDefinition method = null)
            {
                var entry = new MemberName
                {
                    token = member.MetadataToken.ToInt32(),
                    kind = kind,
                    declaring_type = originalTypes[type],
                    original = member.Name,
                    readable = Translate(member.Name),
                    signature = signature
                };
                if (!memberKeys.Add(originalTypes[type] + "|" + kind + "|" + entry.readable + "|" + signature))
                    throw new InvalidDataException("Readable member collision: " + originalTypes[type] + "." + entry.readable + " " + signature);
                if (method != null)
                    foreach (ParameterDefinition p in method.Parameters)
                        if (Translate(p.Name) != p.Name) entry.parameters.Add(new ParameterName { position = p.Index, original = p.Name, readable = Translate(p.Name) });
                table.members.Add(entry);
            }
            foreach (MethodDefinition m in type.Methods) Add(m, "method", Signature(m), m);
            foreach (FieldDefinition f in type.Fields) Add(f, "field", Signature(f));
            foreach (PropertyDefinition p in type.Properties) Add(p, "property", TypeKey(p.PropertyType) + "(" + string.Join(",", p.Parameters.Select(a => TypeKey(a.ParameterType))) + ")");
            foreach (EventDefinition e in type.Events) Add(e, "event", TypeKey(e.EventType));
        }
        // Keep unrenamed overloads that share a readable name with a renamed member.
        // Otherwise runtime lookup would accidentally omit the literal overload.
        var renamedGroups = table.members.Where(m => m.readable != m.original)
            .Select(m => m.declaring_type + "|" + m.kind + "|" + m.readable).ToHashSet(StringComparer.Ordinal);
        table.members = table.members.Where(m => m.readable != m.original || m.parameters.Count != 0 || renamedGroups.Contains(m.declaring_type + "|" + m.kind + "|" + m.readable)).ToList();
        Directory.CreateDirectory(Path.GetDirectoryName(Path.GetFullPath(tablePath)));
        File.WriteAllText(tablePath, JsonSerializer.Serialize(table, Json));
        using (var gz = new GZipStream(File.Create(tablePath + ".gz"), CompressionLevel.SmallestSize))
            gz.Write(File.ReadAllBytes(tablePath));
        Console.WriteLine($"Game names {version}: {table.types.Count} types, {table.members.Count} member deltas => {tablePath}");
    }

    internal static void GenerateShell(string tablePath, string assembly, string shellPath)
    {
        NameTable table = ReadTable(tablePath);
        if (Hash(assembly) != table.assembly_sha256) throw new InvalidDataException($"Assembly-CSharp does not match SDK names for game {table.game_version}; install the matching client or update the SDK names table");
        using ModuleDefinition module = ReadModule(assembly);
        TypeDefinition[] definitions = Types(module).ToArray();
        // Rename references while their declaring types still have original names.
        Rewrite(module, table, toReadable: true);
        var membersByToken = table.members.ToDictionary(m => m.token);
        var typesByToken = table.types.ToDictionary(t => t.token);
        foreach (TypeDefinition type in definitions)
        {
            foreach (IMemberDefinition member in type.Methods.Cast<IMemberDefinition>().Concat(type.Fields).Concat(type.Properties).Concat(type.Events))
            {
                membersByToken.TryGetValue(member.MetadataToken.ToInt32(), out MemberName entry);
                if (entry != null) member.Name = entry.readable;
                if (member is MethodDefinition m)
                {
                    if (entry != null) foreach (ParameterName p in entry.parameters) m.Parameters[p.position].Name = p.readable;
                    // Retain IL only for offline source navigation/decompilation.
                    // ReferenceAssemblyAttribute prevents this assembly from executing.
                }
            }
            SetTypeName(type, typesByToken[type.MetadataToken.ToInt32()].readable);
        }
        module.Assembly.Name.Name = ShellName;
        module.Name = ShellName + ".dll";
        Directory.CreateDirectory(Path.GetDirectoryName(Path.GetFullPath(shellPath)));
        // Visual Studio resolves reference-source navigation via the standard ref -> lib
        // layout. The companion is never a compiler/runtime dependency; it is only for IDEs.
        string root = Path.GetDirectoryName(Path.GetFullPath(shellPath));
        string implementation = Path.Combine(root, "lib", ShellName + ".dll");
        Directory.CreateDirectory(Path.GetDirectoryName(implementation));
        module.Write(implementation);
        SourceNavigation.Generate(implementation, Path.GetDirectoryName(Path.GetFullPath(assembly)));
        // The CLR refuses to execute a reference assembly even if it is copied accidentally.
        var marker = new TypeReference("System.Runtime.CompilerServices", "ReferenceAssemblyAttribute", module, module.TypeSystem.CoreLibrary);
        module.Assembly.CustomAttributes.Add(new CustomAttribute(new MethodReference(".ctor", module.TypeSystem.Void, marker) { HasThis = true }));
        Directory.CreateDirectory(Path.GetDirectoryName(Path.GetFullPath(shellPath)));
        module.Write(shellPath);
        string reference = Path.Combine(root, "ref", ShellName + ".dll");
        Directory.CreateDirectory(Path.GetDirectoryName(reference));
        File.Copy(shellPath, reference, overwrite: true);
        File.Copy(Path.ChangeExtension(implementation, ".xml"), Path.ChangeExtension(reference, ".xml"), overwrite: true);
        File.Copy(Path.ChangeExtension(implementation, ".xml"), Path.ChangeExtension(shellPath, ".xml"), overwrite: true);
        File.Copy(Path.Combine(root, "lib", "navigation.json"), Path.Combine(root, "navigation.json"), overwrite: true);
        Console.WriteLine($"Readable reference assembly {table.game_version} => {shellPath}");
    }

    internal static ModuleDefinition ReadModule(string path, params string[] searchDirectories)
    {
        var resolver = new DefaultAssemblyResolver();
        resolver.AddSearchDirectory(Path.GetDirectoryName(Path.GetFullPath(path)));
        foreach (string directory in searchDirectories) resolver.AddSearchDirectory(directory);
        return ModuleDefinition.ReadModule(path, new ReaderParameters { InMemory = true, AssemblyResolver = resolver });
    }

    internal static void SetTypeName(TypeReference type, string full)
    {
        if (type.DeclaringType != null) { type.Name = full[(full.LastIndexOf('+') + 1)..]; return; }
        int dot = full.LastIndexOf('.');
        type.Namespace = dot < 0 ? "" : full[..dot];
        type.Name = full[(dot + 1)..];
    }

    internal static void Rewrite(ModuleDefinition module, NameTable table, bool toReadable)
    {
        var types = table.types.ToDictionary(t => toReadable ? t.original : t.readable);
        var readableByOriginal = table.types.ToDictionary(t => t.original, t => t.readable);
        string TranslateSignature(string sig)
        {
            // Type references in signatures are tokenized, so substring collisions are avoided.
            return System.Text.RegularExpressions.Regex.Replace(sig, @"[^,:()\[\]&*<>\s]+", m => readableByOriginal.TryGetValue(m.Value, out string v) ? v : m.Value);
        }
        var members = table.members.Where(m => m.kind is "method" or "field").ToDictionary(
            m => (toReadable ? m.declaring_type : readableByOriginal[m.declaring_type]) + "|" + m.kind + "|" + (toReadable ? m.original : m.readable) + "|" + (toReadable ? m.signature : TranslateSignature(m.signature)));
        var namedMembers = table.members.Where(m => m.original != m.readable && m.kind is "method" or "field")
            .Select(m => (toReadable ? m.declaring_type : readableByOriginal[m.declaring_type]) + "|" + m.kind + "|" + (toReadable ? m.original : m.readable)).ToHashSet(StringComparer.Ordinal);
        bool IsGame(TypeReference type)
        {
            while (type is TypeSpecification s) type = s.ElementType;
            while (type.DeclaringType != null) type = type.DeclaringType;
            return type.Scope is AssemblyNameReference a ? a.Name == (toReadable ? table.assembly_name : ShellName) : toReadable && type.Scope == module;
        }
        var memberRefs = module.GetMemberReferences().ToList();
        var attributes = new List<CustomAttribute>();
        void Attributes(ICustomAttributeProvider provider)
        {
            if (provider.HasCustomAttributes) attributes.AddRange(provider.CustomAttributes);
        }
        Attributes(module); Attributes(module.Assembly);
        foreach (TypeDefinition t in Types(module))
        {
            Attributes(t);
            foreach (GenericParameter gp in t.GenericParameters) Attributes(gp);
            foreach (InterfaceImplementation i in t.Interfaces) Attributes(i);
            foreach (FieldDefinition f in t.Fields) Attributes(f);
            foreach (PropertyDefinition p in t.Properties) Attributes(p);
            foreach (EventDefinition e in t.Events) Attributes(e);
            foreach (MethodDefinition m in t.Methods)
            {
                Attributes(m); Attributes(m.MethodReturnType);
                foreach (ParameterDefinition p in m.Parameters) Attributes(p);
                foreach (GenericParameter gp in m.GenericParameters) Attributes(gp);
            }
        }
        foreach (CustomAttribute attribute in attributes) memberRefs.Add(attribute.Constructor);
        foreach (TypeDefinition type in Types(module))
            foreach (MethodDefinition method in type.Methods)
            {
                foreach (MethodReference ov in method.Overrides) memberRefs.Add(ov);
                if (method.HasBody) foreach (Instruction instruction in method.Body.Instructions)
                        if (instruction.Operand is MemberReference mr) memberRefs.Add(mr);
            }
        foreach (MemberReference reference in memberRefs.Distinct())
        {
            MemberReference member = reference is GenericInstanceMethod gm ? gm.ElementMethod : reference;
            if (member is not MethodReference && member is not FieldReference || member is IMemberDefinition || !IsGame(member.DeclaringType)) continue;
            TypeReference declaring = member.DeclaringType is GenericInstanceType gi ? gi.ElementType : member.DeclaringType;
            // CLR array Get/Set/Address pseudo-methods have no metadata definitions.
            if (declaring is ArrayType) continue;
            string key = TypeKey(declaring) + "|" + (member is MethodReference ? "method" : "field") + "|" + member.Name + "|" + Signature(member);
            if (members.TryGetValue(key, out MemberName entry)) member.Name = toReadable ? entry.readable : entry.original;
            else if (namedMembers.Contains(key[..key.LastIndexOf('|')])) throw new InvalidDataException("Game member signature does not match the names table: " + key);
        }
        // Snapshot names before renaming a parent of a nested type.
        var renames = new Dictionary<TypeReference, string>();
        void Visit(TypeReference type)
        {
            if (type == null || type is GenericParameter) return;
            if (type is FunctionPointerType fp) { Visit(fp.ReturnType); foreach (ParameterDefinition p in fp.Parameters) Visit(p.ParameterType); return; }
            if (type is TypeSpecification spec)
            {
                Visit(spec.ElementType);
                if (type is GenericInstanceType gi) foreach (TypeReference a in gi.GenericArguments) Visit(a);
                if (type is IModifierType modifier) Visit(modifier.ModifierType);
                return;
            }
            if (!renames.ContainsKey(type) && IsGame(type))
            {
                if (!types.TryGetValue(TypeKey(type), out TypeName entry)) throw new InvalidDataException("Unmapped game type: " + TypeKey(type));
                renames[type] = toReadable ? entry.readable : entry.original;
            }
            Visit(type.DeclaringType);
        }
        void VisitArgument(CustomAttributeArgument argument)
        {
            Visit(argument.Type);
            if (argument.Value is TypeReference type) Visit(type);
            if (argument.Value is CustomAttributeArgument boxed) VisitArgument(boxed);
            if (argument.Value is CustomAttributeArgument[] array) foreach (CustomAttributeArgument item in array) VisitArgument(item);
        }
        foreach (CustomAttribute attribute in attributes)
        {
            // Decode blobs before changing assembly scopes. System.Type arguments are typed
            // metadata even though the ECMA-335 blob stores assembly-qualified text.
            foreach (CustomAttributeArgument argument in attribute.ConstructorArguments) VisitArgument(argument);
            void NamedArguments(Mono.Collections.Generic.Collection<CustomAttributeNamedArgument> arguments, string kind)
            {
                for (int i = 0; i < arguments.Count; i++)
                {
                    CustomAttributeNamedArgument argument = arguments[i]; VisitArgument(argument.Argument);
                    if (!IsGame(attribute.AttributeType)) continue;
                    string declaring = TypeKey(attribute.AttributeType);
                    MemberName entry = table.members.SingleOrDefault(m => m.kind == kind &&
                        (toReadable ? m.declaring_type : readableByOriginal[m.declaring_type]) == declaring &&
                        (toReadable ? m.original : m.readable) == argument.Name);
                    if (entry != null) arguments[i] = new CustomAttributeNamedArgument(toReadable ? entry.readable : entry.original, argument.Argument);
                }
            }
            NamedArguments(attribute.Fields, "field"); NamedArguments(attribute.Properties, "property");
        }
        foreach (TypeReference t in module.GetTypeReferences()) Visit(t);
        foreach (MemberReference member in memberRefs)
        {
            Visit(member.DeclaringType);
            if (member is MethodReference m) { Visit(m.ReturnType); foreach (ParameterDefinition p in m.Parameters) Visit(p.ParameterType); if (m is GenericInstanceMethod gm) foreach (TypeReference a in gm.GenericArguments) Visit(a); }
            if (member is FieldReference f) Visit(f.FieldType);
        }
        foreach (TypeDefinition type in Types(module))
        {
            Visit(type.BaseType);
            foreach (InterfaceImplementation i in type.Interfaces) Visit(i.InterfaceType);
            foreach (FieldDefinition f in type.Fields) Visit(f.FieldType);
            foreach (PropertyDefinition p in type.Properties) { Visit(p.PropertyType); foreach (ParameterDefinition a in p.Parameters) Visit(a.ParameterType); }
            foreach (EventDefinition e in type.Events) Visit(e.EventType);
            foreach (GenericParameter gp in type.GenericParameters) foreach (GenericParameterConstraint c in gp.Constraints) Visit(c.ConstraintType);
            foreach (MethodDefinition m in type.Methods)
            {
                Visit(m.ReturnType);
                foreach (ParameterDefinition p in m.Parameters) Visit(p.ParameterType);
                foreach (GenericParameter gp in m.GenericParameters) foreach (GenericParameterConstraint c in gp.Constraints) Visit(c.ConstraintType);
                if (m.HasBody) { foreach (VariableDefinition v in m.Body.Variables) Visit(v.VariableType); foreach (ExceptionHandler h in m.Body.ExceptionHandlers) Visit(h.CatchType); foreach (Instruction i in m.Body.Instructions) if (i.Operand is TypeReference t) Visit(t); }
            }
        }
        foreach (KeyValuePair<TypeReference, string> pair in renames) SetTypeName(pair.Key, pair.Value);
    }

    internal static void Reobfuscate(string tablePath, string input, string output, string originalAssembly = null, string dependencyDirectory = null)
    {
        NameTable table = ReadTable(tablePath);
        var dependencies = new List<string> { Path.GetDirectoryName(Path.GetFullPath(tablePath)), Path.GetDirectoryName(Path.GetFullPath(output)) };
        if (originalAssembly != null) dependencies.Add(Path.GetDirectoryName(Path.GetFullPath(originalAssembly)));
        if (dependencyDirectory != null) dependencies.Add(Path.GetFullPath(dependencyDirectory));
        using ModuleDefinition module = ReadModule(input, [.. dependencies]);
        if (module.Assembly.Name.HasPublicKey) throw new InvalidDataException("Signed plugins require an explicit signing workflow");
        VerifyStamp(module, table);
        Rewrite(module, table, false);
        foreach (AssemblyNameReference a in module.AssemblyReferences.Where(a => a.Name == ShellName)) a.Name = table.assembly_name;
        // Preserve compiler PDBs in obj; rewritten runtime DLL deliberately has no stale symbols.
        string absoluteOutput = Path.GetFullPath(output);
        Directory.CreateDirectory(Path.GetDirectoryName(absoluteOutput));
        string temporary = absoluteOutput + "." + Guid.NewGuid().ToString("N") + ".tmp";
        try
        {
            module.Write(temporary);
            Verify(tablePath, temporary, originalAssembly);
            File.Move(temporary, absoluteOutput, overwrite: true);
        }
        finally { File.Delete(temporary); }
        Console.WriteLine("Reobfuscated runtime plugin: " + output);
    }

    internal static void Verify(string tablePath, string plugin, string originalAssembly = null)
    {
        NameTable table = ReadTable(tablePath);
        using ModuleDefinition module = originalAssembly == null ? ReadModule(plugin) : ReadModule(plugin, Path.GetDirectoryName(Path.GetFullPath(originalAssembly)));
        if (module.AssemblyReferences.Any(a => a.Name == ShellName)) throw new InvalidDataException("Runtime DLL still references the readable shell");
        VerifyStamp(module, table);
        if (originalAssembly != null)
        {
            if (Hash(originalAssembly) != table.assembly_sha256) throw new InvalidDataException("Verification game binary does not match the names table");
            bool IsGame(TypeReference t) => t.GetElementType().Scope is AssemblyNameReference a && a.Name == table.assembly_name;
            foreach (TypeReference type in module.GetTypeReferences().Where(IsGame))
                if (type.Resolve() == null) throw new InvalidDataException("Unresolvable game type reference: " + type.FullName);
            foreach (MemberReference member in module.GetMemberReferences().Where(m => IsGame(m.DeclaringType)))
            {
                if (member.DeclaringType is ArrayType) continue;
                if (member is MethodReference method && method.Resolve() == null || member is FieldReference field && field.Resolve() == null)
                    throw new InvalidDataException("Unresolvable game member reference: " + member.FullName);
            }
        }
    }

    private static void VerifyStamp(ModuleDefinition module, NameTable table)
    {
        CustomAttribute[] markers = module.Assembly.CustomAttributes.Where(a => a.AttributeType.FullName == "System.Reflection.AssemblyMetadataAttribute" && a.ConstructorArguments.Count == 2 && a.ConstructorArguments[0].Value as string == "BD2.GameNames").ToArray();
        if (markers.Length != 1 || markers[0].ConstructorArguments[1].Value as string != Stamp(table))
            throw new InvalidDataException("Missing/mismatched SDK stamp: compile with GameSdkIdentity.g.cs from this names table");
    }
}
