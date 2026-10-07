using System.IO.Compression;
using BD2.GameSdk.Generation;
using System.Security.Cryptography;
using System.Text.Json;
using BD2.GameNames.Internal;
using Mono.Cecil;

using static BD2.GameSdk.Generation.AssemblyNames;
using static BD2.GameSdk.Generation.GenerationInputs;
using static BD2.GameSdk.Generation.ShellGenerator;

namespace BD2.GameSdk;

internal static class Program
{
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
                case "names" when args.Length == 5: NameGenerator.Generate(args[1], args[2], args[3], args[4]); break;
                case "shell" when args.Length == 4: ShellGenerator.Generate(args[1], args[2], args[3]); break;
                case "source-navigation" when args.Length == 3: SourceNavigation.Generate(args[1], args[2]); break;
                case "verify-navigation" when args.Length == 2: SourceNavigationVerifier.Verify(args[1]); break;
                case "reobf" when args.Length is 4 or 5 or 6: Reobfuscate(args[1], args[2], args[3], args.Length >= 5 ? args[4] : null, args.Length == 6 ? args[5] : null); break;
                case "verify" when args.Length is 3 or 4: Verify(args[1], args[2], args.Length == 4 ? args[3] : null); break;

                default: throw new ArgumentException("Usage: prepare-embedded <Assembly-CSharp.dll> <output-dir> [--game-version version] | export-names <names.json> | names <Assembly-CSharp.dll> <mapping.obfuscate> <versions.json> <names.json> | shell <names.json> <Assembly-CSharp.dll> <output.dll> | source-navigation <readable-implementation.dll> <dependency-directory> | verify-navigation <sdk-directory> | reobf <names.json> <input.dll> <output.dll> [Assembly-CSharp.dll] [dependency-directory] | verify <names.json> <plugin.dll> [Assembly-CSharp.dll]");
            }
            return 0;
        }
        catch (Exception ex) { Console.Error.WriteLine("BD2 GameSdk: " + ex); return 1; }
    }

    private static string DependencyStamp(string assembly) => string.Join("|",
        Directory.EnumerateFiles(Path.GetDirectoryName(Path.GetFullPath(assembly)), "*.dll").OrderBy(p => p, StringComparer.Ordinal)
            .Select(p => Path.GetFileName(p) + ":" + Hash(p)));
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
        string inputs = tableHash + "|" + GenerationInputs.Stamp() + "|" + DependencyStamp(assembly);
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
                ShellGenerator.Generate(tablePath, assembly, Path.Combine(shared, ShellName + ".dll"));
                File.WriteAllText(ready, StampFiles());
            }
            else Console.WriteLine($"Game source navigation cache hit: game={gameVersion}");
            WriteNavigationItems(shared);
            WriteIdentity(Path.Combine(output, "GameSdkIdentity.g.cs"), table);
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
            SourceNavigationVerifier.RestoreSources(output);
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

    private static void WriteIdentity(string path, NameTable table) =>
        WriteGeneratedFile(path, System.Text.Encoding.UTF8.GetBytes(
            "// Generated from the shared names table.\n[assembly: System.Reflection.AssemblyMetadataAttribute(\"BD2.GameNames\", " + JsonSerializer.Serialize(Stamp(table)) + ")]\n"));

    internal static void Prepare(string assembly, string mapping, string versions, string output)
    {
        Directory.CreateDirectory(output);
        string tablePath = Path.Combine(output, "names.json");
        NameGenerator.Generate(assembly, mapping, versions, tablePath);
        PreparePackage(tablePath + ".gz", assembly, output);
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
        RuntimeInitialization.Inject(module, Stamp(table));
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
        RuntimeInitialization.Verify(module, Stamp(table));
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
