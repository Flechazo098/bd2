using System.IO.Compression;
using System.Text.Json;
using BD2.GameNames.Internal;
using Mono.Cecil;
using static BD2.GameSdk.Generation.AssemblyNames;
using static BD2.GameSdk.Generation.GenerationInputs;
using static BD2.GameSdk.Generation.ShellGenerator;

namespace BD2.GameSdk.Generation;

internal static class NameGenerator
{
    internal static void Generate(string assembly, string mapping, string versions, string tablePath)
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
            generator_sha256 = GenerationInputs.NamesStamp()
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

}
