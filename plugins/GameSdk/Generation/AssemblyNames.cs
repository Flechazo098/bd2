using BD2.GameNames.Internal;
using Mono.Cecil;
using Mono.Cecil.Cil;
using static BD2.GameSdk.Generation.ShellGenerator;

namespace BD2.GameSdk.Generation;

internal static class AssemblyNames
{
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

}
