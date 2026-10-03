using System.Reflection;
using System.Text.Json;
using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp;
using Microsoft.CodeAnalysis.Host.Mef;
using Microsoft.CodeAnalysis.CSharp.Syntax;

if (args.Length == 0)
    throw new ArgumentException("Usage: GameSdk.NavigationTests <sdk-directory> [game-managed-directory] [--embedded-only]");

var hiddenSources = new List<(string Original, string Hidden)>();
try
{
    // Missing local source proves that navigation uses the embedded documents.
    if (args.Contains("--embedded-only"))
    {
        using var manifest = JsonDocument.Parse(File.ReadAllText(Path.Combine(args[0], "navigation.json")));
        string root = manifest.RootElement.GetProperty("SourceRoot").GetString()!;
        string fixtures = Path.GetFullPath(Path.Combine(Environment.CurrentDirectory, ".build", "game-sdk-tests")) + Path.DirectorySeparatorChar;
        if (!Path.GetFullPath(root).StartsWith(fixtures, StringComparison.OrdinalIgnoreCase))
            throw new InvalidOperationException("Embedded-only tests may only move generated synthetic fixture sources.");
        foreach (string source in Directory.GetFiles(root, "*.cs", SearchOption.AllDirectories))
        {
            string hidden = source + ".embedded-test";
            File.Move(source, hidden);
            hiddenSources.Add((source, hidden));
        }
    }

    // Reflection accesses Visual Studio's internal Roslyn service. A mock would
    // miss the reference-assembly lookup and embedded-document loader failures.
    var features = Assembly.Load("Microsoft.CodeAnalysis.Features");
    var host = MefHostServices.Create(MefHostServices.DefaultAssemblies
        .Add(features).Add(Assembly.Load("Microsoft.CodeAnalysis.CSharp.Features")));
    var exportProvider = host.GetType().GetInterfaces().Single(t => t.Name == "IMefHostExportProvider");
    var getExports = exportProvider.GetMethods().Single(m => m.Name == "GetExports" && m.GetGenericArguments().Length == 1);
    var contract = features.GetType("Microsoft.CodeAnalysis.MetadataAsSource.IMetadataAsSourceFileService")!;
    var exports = (System.Collections.IEnumerable)getExports.MakeGenericMethod(contract).Invoke(host, null)!;
    object export = exports.Cast<object>().First();
    object service = export.GetType().GetProperty("Value")!.GetValue(export)!;

    using var workspace = new AdhocWorkspace(host);
    string reference = Path.Combine(Path.GetFullPath(args[0]), "ref", "Assembly-CSharp.Readable.dll");
    IEnumerable<string> referencePaths = args.Length > 1 && !args[1].StartsWith("--")
        ? Directory.GetFiles(Path.GetFullPath(args[1]), "*.dll").Where(p => Path.GetFileName(p) != "Assembly-CSharp.dll")
        : ((string)AppContext.GetData("TRUSTED_PLATFORM_ASSEMBLIES")!).Split(Path.PathSeparator);
    var references = referencePaths.Select(p => MetadataReference.CreateFromFile(p))
        .Append(MetadataReference.CreateFromFile(reference));
    var project = workspace.AddProject("Consumer", LanguageNames.CSharp)
        .WithMetadataReferences(references)
        .WithCompilationOptions(new CSharpCompilationOptions(OutputKind.DynamicallyLinkedLibrary));
    if (!workspace.TryApplyChanges(project.Solution)) throw new InvalidOperationException("Cannot create navigation consumer project.");
    project = workspace.CurrentSolution.GetProject(project.Id)!;
    var compilation = (await project.GetCompilationAsync())!;
    bool fixture = compilation.GetTypeByMetadataName("Readable.Agent") is not null;
    var type = compilation.GetTypeByMetadataName(fixture ? "Readable.Agent" : "BDNetwork.NetworkManager")
        ?? throw new InvalidOperationException("Navigation test type missing.");

    var optionsType = features.GetType("Microsoft.CodeAnalysis.MetadataAsSource.MetadataAsSourceOptions")!;
    object options = optionsType.GetMethod("GetDefault", BindingFlags.Static | BindingFlags.Public | BindingFlags.NonPublic)!
        .Invoke(null, new object[] { project.Services })!;
    optionsType.GetProperty("NavigateToSourceLinkAndEmbeddedSources")!.SetValue(options, true);
    optionsType.GetProperty("NavigateToDecompiledSources")!.SetValue(options, false);

    var symbols = new List<ISymbol> { type };
    if (fixture)
    {
        var ping = type.GetMembers("Ping").OfType<IMethodSymbol>().Single(m => m.Parameters[0].Type.SpecialType == SpecialType.System_Int32);
        symbols.AddRange(new ISymbol[] {
            ping, type.GetMembers("Ping").OfType<IMethodSymbol>().Single(m => m.Parameters[0].Type.SpecialType == SpecialType.System_String),
            ping.Parameters[0], type.GetMembers("Echo").Single(), type.GetMembers("Value").Single(), type.GetMembers("Changed").Single()
        });
        var box = compilation.GetTypeByMetadataName("Readable.Box`1")!;
        symbols.AddRange(new ISymbol[] { box, box.GetMembers("Echo").Single(), box.GetTypeMembers("Nested").Single() });
    }
    else
    {
        symbols.AddRange(new ISymbol[] {
            type.GetMembers("Send").First(), type.GetMembers("Refresh").Single(), type.GetMembers("BatchCount").Single(),
            type.GetMembers("UpdateNowFunc").Single(), type.GetMembers("OnRequestComplete").Single(), type.GetTypeMembers("BatchObject").Single()
        });
    }

    foreach (var symbol in symbols)
    {
        var task = (Task)contract.GetMethod("GetGeneratedFileAsync")!
            .Invoke(service, new object[] { workspace, project, symbol, false, options, CancellationToken.None })!;
        await task;
        object result = task.GetType().GetProperty("Result")!.GetValue(task)!;
        string path = (string)result.GetType().GetProperty("FilePath")!.GetValue(result)!;
        string source = File.ReadAllText(path);
        if (!path.Contains("PdbSourceDocumentMetadataAsSourceFileProvider", StringComparison.Ordinal))
            throw new InvalidDataException("Navigation fell back from PDB source: " + path);
        if (!source.Contains(fixture ? "return value;" : "Path.Combine", StringComparison.Ordinal))
            throw new InvalidDataException("Navigation did not load complete method bodies: " + path);
        var location = (Location)result.GetType().GetProperty("IdentifierLocation")!.GetValue(result)!;
        if (!location.IsInSource) throw new InvalidDataException("Navigation has no source declaration.");
        string identifier = source.Substring(location.SourceSpan.Start, location.SourceSpan.Length);
        if (identifier != symbol.Name)
            throw new InvalidDataException($"Navigation selected '{identifier}' instead of '{symbol.Name}'.");
        if (fixture && symbol is IMethodSymbol { Name: "Ping" } method)
        {
            var syntax = CSharpSyntaxTree.ParseText(source).GetRoot().FindToken(location.SourceSpan.Start)
                .Parent!.AncestorsAndSelf().OfType<MethodDeclarationSyntax>().First();
            string expected = method.Parameters[0].Type.SpecialType == SpecialType.System_Int32 ? "int" : "string";
            if (syntax.ParameterList.Parameters[0].Type!.ToString() != expected)
                throw new InvalidDataException("Navigation selected the wrong Ping overload.");
        }
        Console.WriteLine($"{symbol.ToDisplayString()} => {path}");
    }
    Console.WriteLine($"Visual Studio Roslyn PDB-source navigation: {symbols.Count} declarations resolved to exact identifiers and complete source; embedded-only={hiddenSources.Count > 0}.");
}
finally
{
    foreach (var source in hiddenSources) File.Move(source.Hidden, source.Original);
}
