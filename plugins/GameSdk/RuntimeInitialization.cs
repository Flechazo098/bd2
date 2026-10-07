using Mono.Cecil;
using Mono.Cecil.Cil;

namespace BD2.GameSdk;

internal static class RuntimeInitialization
{
    private const string RuntimeAssembly = "BD2.GameNames";
    private const string RuntimeType = "BD2.GameNames.RuntimeCompatibility";
    private const string RuntimeMethod = "InitializePlugin";

    internal static void Inject(ModuleDefinition module, string stamp)
    {
        TypeDefinition moduleType = module.Types.Single(t => t.Name == "<Module>");
        MethodDefinition initializer = moduleType.Methods.SingleOrDefault(m => m.Name == ".cctor");
        if (initializer == null)
        {
            initializer = new MethodDefinition(".cctor", MethodAttributes.Private | MethodAttributes.Static |
                MethodAttributes.HideBySig | MethodAttributes.SpecialName | MethodAttributes.RTSpecialName, module.TypeSystem.Void);
            moduleType.Methods.Add(initializer);
            initializer.Body.Instructions.Add(Instruction.Create(OpCodes.Ret));
        }
        if (initializer.HasBody && initializer.Body.Instructions.Count >= 2 && IsRuntimeCall(initializer.Body.Instructions[1]))
        {
            Verify(module, stamp);
            return;
        }
        AssemblyNameReference runtime = module.AssemblyReferences.SingleOrDefault(a => a.Name == RuntimeAssembly);
        if (runtime == null)
        {
            runtime = new AssemblyNameReference(RuntimeAssembly, new Version(1, 0, 0, 0));
            module.AssemblyReferences.Add(runtime);
        }
        var type = new TypeReference("BD2.GameNames", "RuntimeCompatibility", module, runtime);
        var method = new MethodReference(RuntimeMethod, module.TypeSystem.Void, type);
        method.Parameters.Add(new ParameterDefinition(module.TypeSystem.String));
        initializer.Body.Instructions.Insert(0, Instruction.Create(OpCodes.Ldstr, stamp));
        initializer.Body.Instructions.Insert(1, Instruction.Create(OpCodes.Call, method));
    }

    internal static void Verify(ModuleDefinition module, string stamp)
    {
        MethodDefinition initializer = module.Types.Single(t => t.Name == "<Module>").Methods.SingleOrDefault(m => m.Name == ".cctor");
        if (initializer?.HasBody != true || initializer.Body.Instructions.Count < 3 ||
            initializer.Body.Instructions[0].OpCode != OpCodes.Ldstr || initializer.Body.Instructions[0].Operand as string != stamp ||
            !IsRuntimeCall(initializer.Body.Instructions[1]))
            throw new InvalidDataException("Missing/mismatched SDK runtime initializer: rebuild this plugin with BD2.GameSdk");
    }

    private static bool IsRuntimeCall(Instruction instruction) => instruction.OpCode == OpCodes.Call &&
        instruction.Operand is MethodReference method && method.Name == RuntimeMethod && !method.HasThis &&
        method.DeclaringType.FullName == RuntimeType && method.DeclaringType.Scope is AssemblyNameReference assembly &&
        assembly.Name == RuntimeAssembly && method.ReturnType.MetadataType == MetadataType.Void &&
        method.Parameters.Count == 1 && method.Parameters[0].ParameterType.MetadataType == MetadataType.String;
}
