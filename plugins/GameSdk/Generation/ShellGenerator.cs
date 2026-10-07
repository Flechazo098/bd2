using System.Text.Json;
using BD2.GameNames.Internal;
using Mono.Cecil;
using static BD2.GameSdk.Generation.AssemblyNames;
using static BD2.GameSdk.Generation.GenerationInputs;

namespace BD2.GameSdk.Generation;

internal static class ShellGenerator
{
    internal const string ShellName = "Assembly-CSharp.Readable";
    internal static readonly JsonSerializerOptions Json = new() { IncludeFields = true };
    internal static NameTable ReadTable(string path) => JsonSerializer.Deserialize<NameTable>(File.ReadAllText(path), Json);

    internal static void Generate(string tablePath, string assembly, string shellPath)
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

}
