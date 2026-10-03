using System.Collections.Concurrent;
using System.Collections.Immutable;
using System.Diagnostics;
using System.Reflection.Metadata;
using System.Reflection.Metadata.Ecma335;
using System.Reflection.PortableExecutable;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using ICSharpCode.Decompiler;
using ICSharpCode.Decompiler.CSharp;
using ICSharpCode.Decompiler.CSharp.OutputVisitor;
using ICSharpCode.Decompiler.CSharp.Syntax;
using ICSharpCode.Decompiler.Metadata;
using ICSharpCode.Decompiler.TypeSystem;
using Mono.Cecil;
using Mono.Cecil.Cil;
using EntityHandle = System.Reflection.Metadata.EntityHandle;
using ModuleDefinition = Mono.Cecil.ModuleDefinition;
using MethodDefinition = Mono.Cecil.MethodDefinition;
using MethodDebugInformation = Mono.Cecil.Cil.MethodDebugInformation;

namespace BD2.GameSdk;

/// <summary>Build local, offline external-source navigation from the readable implementation.</summary>
internal static class SourceNavigation
{
    internal static readonly Guid EmbeddedSource = new("0E8A571B-6926-466E-B4AD-8AB04611F5FE");
    internal static readonly Guid TypeDocuments = new("932E74BC-DBA9-4478-8D46-0F32A7BAB3D3");
    private static readonly Guid CompilationOptions = new("B5FEEC05-8CD0-4A83-96DA-466284BB4BD8");
    private static readonly Guid CompilationReferences = new("7E4D4708-096E-4C5C-AEDA-CB10BA6A740D");
    private static readonly Guid CSharpLanguage = new("3F5162F8-07C6-11D3-9053-00C04FA302A1");

    internal sealed record Declaration(int Token, string Name, string Kind, string File, int Line, int Column, int EndLine, int EndColumn, bool GeneratedView);
    internal sealed record Point(int Offset, int Line, int Column, int EndLine, int EndColumn);
    private sealed record Source(string Path, byte[] Content, Dictionary<int, Declaration> Declarations, Dictionary<int, List<Point>> Methods);
    internal sealed record Manifest(int SchemaVersion, string AssemblySha256, string PdbSha256, string DecompilerVersion, string SourceRoot,
        int Documents, int Declarations, int MethodsWithBodies, int MethodsWithSymbols, List<string> Diagnostics, List<Declaration> Symbols);

    internal static void Generate(string shellPath, string dependencyDirectory)
    {
        string directory = Path.GetDirectoryName(Path.GetFullPath(shellPath));
        string sourceRoot = Path.Combine(directory, "sources");
        Directory.CreateDirectory(sourceRoot);
        using var module = Program.ReadModule(shellPath, dependencyDirectory);
        using var pe = new PEFile(shellPath, new MemoryStream(File.ReadAllBytes(shellPath)), PEStreamOptions.PrefetchEntireImage);
        var reader = pe.Metadata;
        var types = reader.TypeDefinitions.Where(h => reader.GetTypeDefinition(h).GetDeclaringType().IsNil && reader.GetString(reader.GetTypeDefinition(h).Name) != "<Module>").ToArray();
        var sources = new ConcurrentBag<Source>();
        var timer = Stopwatch.StartNew();
        int completed = 0;
        var expected = module.GetTypes().ToDictionary(t => t.MetadataToken.ToInt32(), t =>
            t.Methods.Cast<IMemberDefinition>().Concat(t.Fields).Concat(t.Properties).Concat(t.Events).Select(m => m.MetadataToken.ToInt32()).Append(t.MetadataToken.ToInt32()).ToHashSet());
        var definitionsByToken = module.GetTypes().ToDictionary(t => t.MetadataToken.ToInt32());
        // Each worker owns its decompiler and resolver. PE metadata is immutable;
        // mutable decompiler state is never shared between workers.
        int workers = Math.Clamp(Environment.ProcessorCount / 2, 1, 4);
        Parallel.ForEach(types, new ParallelOptions { MaxDegreeOfParallelism = workers },
            () => new Worker(shellPath, dependencyDirectory), (handle, _, worker) =>
            {
                int token = MetadataTokens.GetToken(handle);
                string relative = PathFor(reader, handle);
                var primary = worker.Decompile(handle, Path.Combine(sourceRoot, relative), raw: false);
                ApplyImplicitDeclarations(primary.Declarations, definitionsByToken[MetadataTokens.GetToken(handle)]);
                sources.Add(primary);
                var typeTokens = Descendants(reader, handle).Select(h => MetadataTokens.GetToken(h)).ToArray();
                var wanted = typeTokens.SelectMany(t => expected[t]).ToHashSet();
                var covered = primary.Declarations.Keys.ToHashSet();
                if (wanted.Except(covered).Any())
                {
                    // Keep idiomatic async/iterator/lambda code in the primary view, and
                    // expose the compiler-generated implementation in a companion view.
                    // Roslyn materializes embedded documents by basename, so primary
                    // and generated views must have distinct filenames as well as paths.
                    string generatedPath = Path.Combine(Path.GetDirectoryName(relative), Path.GetFileNameWithoutExtension(relative) + ".generated.cs");
                    var raw = worker.Decompile(handle, Path.Combine(sourceRoot, "generated", generatedPath), raw: true);
                    ApplyImplicitDeclarations(raw.Declarations, definitionsByToken[MetadataTokens.GetToken(handle)]);
                    sources.Add(raw);
                    covered.UnionWith(raw.Declarations.Keys);
                }
                foreach (int missing in wanted.Except(covered))
                {
                    // ILSpy hides some runtime-only definitions even with transformations
                    // disabled. Decompile them explicitly, retaining exact token identity.
                    var supplemental = worker.DecompileMember(MetadataTokens.EntityHandle(missing),
                        Path.Combine(sourceRoot, "metadata", missing.ToString("X8") + ".cs"));
                    sources.Add(supplemental);
                    if (!supplemental.Declarations.ContainsKey(missing))
                        throw new InvalidDataException($"Decompiler omitted declaration 0x{missing:X8}; complete navigation cannot be published");
                }
                int done = Interlocked.Increment(ref completed);
                if (done % 500 == 0 || done == types.Length)
                    Console.WriteLine($"Game source navigation: {done}/{types.Length} top-level types ({timer.Elapsed.TotalSeconds:F0}s)");
                return worker;
            }, worker => worker.Dispose());

        var ordered = sources.OrderBy(s => s.Path, StringComparer.Ordinal).ToArray();
        var sourceByPath = ordered.ToDictionary(s => s.Path, StringComparer.Ordinal);
        var declarations = new Dictionary<int, Declaration>();
        var methodPoints = new Dictionary<int, (Source Source, List<Point> Points)>();
        foreach (var source in ordered.OrderBy(s => s.Declarations.Values.FirstOrDefault()?.GeneratedView == true))
        {
            foreach (var declaration in source.Declarations) declarations.TryAdd(declaration.Key, declaration.Value);
            foreach (var method in source.Methods) methodPoints.TryAdd(method.Key, (source, method.Value));
        }
        // Accessors may be folded into properties/events. Their declaration navigation
        // maps to the exact accessor when present, otherwise its owning declaration.
        foreach (var type in module.GetTypes())
        {
            foreach (var property in type.Properties)
                Alias(property, property.GetMethod, property.SetMethod);
            foreach (var @event in type.Events)
                Alias(@event, @event.AddMethod, @event.RemoveMethod, @event.InvokeMethod);
        }
        void Alias(IMemberDefinition owner, params MethodDefinition[] accessors)
        {
            if (!declarations.TryGetValue(owner.MetadataToken.ToInt32(), out var location)) return;
            foreach (var method in accessors.Where(m => m != null))
                declarations.TryAdd(method.MetadataToken.ToInt32(), location with { Token = method.MetadataToken.ToInt32(), Name = method.FullName, Kind = "Method" });
        }
        foreach (var method in module.GetTypes().SelectMany(t => t.Methods).Where(m => m.HasBody))
        {
            int token = method.MetadataToken.ToInt32();
            if (!methodPoints.ContainsKey(token) && declarations.TryGetValue(token, out var declaration))
            {
                var source = sourceByPath[declaration.File];
                methodPoints[token] = (source, new List<Point> { new(0, declaration.Line, declaration.Column, declaration.EndLine, declaration.EndColumn) });
            }
        }
        int bodies = module.GetTypes().Sum(t => t.Methods.Count(m => m.HasBody));
        if (methodPoints.Count < bodies) throw new InvalidDataException($"Navigation coverage incomplete: {methodPoints.Count} symbols for {bodies} method bodies");
        string pdbPath = Path.ChangeExtension(shellPath, ".pdb");
        var pdb = BuildPdb(pe, module, ordered, declarations, methodPoints, dependencyDirectory);
        File.WriteAllBytes(pdbPath, pdb.Bytes);
        // Attach exactly the PDB we generated to this PE. No implementation is executed.
        module.Write(shellPath, new WriterParameters { WriteSymbols = true, SymbolWriterProvider = new NavigationSymbolWriterProvider(pdb.Id, pdbPath, pdb.Bytes) });
        // Store the source root once instead of repeating a long machine-specific
        // cache path for hundreds of thousands of declarations.
        var manifest = new Manifest(2, Program.Hash(shellPath), Program.Hash(pdbPath), typeof(CSharpDecompiler).Assembly.GetName().Version.ToString(), sourceRoot,
            ordered.Length, declarations.Count, bodies, methodPoints.Count, new List<string>(),
            declarations.Values.OrderBy(d => d.Token).Select(d => d with { File = Path.GetRelativePath(sourceRoot, d.File) }).ToList());
        File.WriteAllText(Path.Combine(directory, "navigation.json"), JsonSerializer.Serialize(manifest));
        WriteXmlDocumentation(shellPath, pe, declarations);
        Console.WriteLine($"Game source navigation ready: {ordered.Length} source files, {declarations.Count} declarations, {methodPoints.Count} method symbols");
    }

    internal static void Verify(string sdkDirectory)
    {
        string library = Path.Combine(sdkDirectory, "lib", Program.ShellName + ".dll"), pdbPath = Path.ChangeExtension(library, ".pdb");
        var manifest = JsonSerializer.Deserialize<Manifest>(File.ReadAllText(Path.Combine(sdkDirectory, "navigation.json")));
        if (Program.Hash(library) != manifest.AssemblySha256 || Program.Hash(pdbPath) != manifest.PdbSha256)
            throw new InvalidDataException("Source navigation manifest, DLL and PDB do not match");
        using var peStream = File.OpenRead(library); using var pe = new PEReader(peStream);
        using var pdbStream = File.OpenRead(pdbPath); using var provider = MetadataReaderProvider.FromPortablePdbStream(pdbStream);
        var pdb = provider.GetMetadataReader(); var dll = pe.GetMetadataReader();
        var id = new BlobContentId(pdb.DebugMetadataHeader.Id);
        var entry = pe.ReadDebugDirectory().Single(d => d.Type == DebugDirectoryEntryType.CodeView);
        var codeview = pe.ReadCodeViewDebugDirectoryData(entry);
        if (codeview.Guid != id.Guid || entry.Stamp != id.Stamp || codeview.Age != 1) throw new InvalidDataException("PDB identity does not match navigation assembly");
        var checksum = pe.ReadDebugDirectory().Single(d => d.Type == DebugDirectoryEntryType.PdbChecksum);
        if (!pe.ReadPdbChecksumDebugDirectoryData(checksum).Checksum.SequenceEqual(SHA256.HashData(File.ReadAllBytes(pdbPath))))
            throw new InvalidDataException("PE/PDB checksum mismatch");
        int documents = 0, methods = 0;
        foreach (var documentHandle in pdb.Documents)
        {
            var document = pdb.GetDocument(documentHandle);
            if (pdb.GetGuid(document.Language) != CSharpLanguage) throw new InvalidDataException("Navigation document language is not C#");
            var embedded = pdb.GetCustomDebugInformation(documentHandle).Select(h => pdb.GetCustomDebugInformation(h)).Single(c => pdb.GetGuid(c.Kind) == EmbeddedSource);
            byte[] content = DecodeEmbedded(pdb.GetBlobBytes(embedded.Value));
            if (!SHA256.HashData(content).SequenceEqual(pdb.GetBlobBytes(document.Hash))) throw new InvalidDataException("Embedded-source checksum mismatch");
            string path = pdb.GetString(document.Name);
            if (!File.Exists(path) || !File.ReadAllBytes(path).SequenceEqual(content)) throw new InvalidDataException("Local source does not match embedded source: " + path);
            documents++;
        }
        foreach (var method in dll.MethodDefinitions)
        {
            var definition = dll.GetMethodDefinition(method);
            if (definition.RelativeVirtualAddress == 0) continue;
            var debug = pdb.GetMethodDebugInformation(method);
            var points = debug.GetSequencePoints().ToArray();
            if (debug.Document.IsNil || points.Length == 0) throw new InvalidDataException("Method lacks navigation symbols: 0x" + MetadataTokens.GetToken(method).ToString("X8"));
            int size = pe.GetMethodBody(definition.RelativeVirtualAddress).GetILContent().Length;
            if (points.Any(p => !p.IsHidden && (p.Offset >= size || p.StartLine <= 0 || p.EndLine < p.StartLine))) throw new InvalidDataException("Invalid navigation sequence point");
            methods++;
        }
        foreach (var type in dll.TypeDefinitions)
        {
            if (dll.GetString(dll.GetTypeDefinition(type).Name) == "<Module>") continue;
            var custom = pdb.GetCustomDebugInformation(type).Select(h => pdb.GetCustomDebugInformation(h)).SingleOrDefault(c => pdb.GetGuid(c.Kind) == TypeDocuments);
            if (custom.Value.IsNil) throw new InvalidDataException("Type lacks source documents: 0x" + MetadataTokens.GetToken(type).ToString("X8"));
            var blob = pdb.GetBlobReader(custom.Value);
            while (blob.RemainingBytes > 0) _ = pdb.GetDocument(MetadataTokens.DocumentHandle(blob.ReadCompressedInteger()));
        }
        if (documents != manifest.Documents || methods != manifest.MethodsWithBodies || manifest.Symbols.Select(s => s.Token).Distinct().Count() != manifest.Declarations)
            throw new InvalidDataException("Navigation coverage counts do not match manifest");
        Console.WriteLine($"Verified source navigation: {documents} embedded documents, {manifest.Declarations} declarations, {methods} method bodies; PE/PDB identity, source checksums and type documents match");
    }

    internal static void RestoreSources(string sdkDirectory)
    {
        using var stream = File.OpenRead(Path.Combine(sdkDirectory, "lib", Program.ShellName + ".pdb"));
        using var provider = MetadataReaderProvider.FromPortablePdbStream(stream);
        var reader = provider.GetMetadataReader();
        foreach (var handle in reader.Documents)
        {
            string path = reader.GetString(reader.GetDocument(handle).Name);
            if (File.Exists(path)) continue;
            var info = reader.GetCustomDebugInformation(handle).Select(h => reader.GetCustomDebugInformation(h)).Single(c => reader.GetGuid(c.Kind) == EmbeddedSource);
            Directory.CreateDirectory(Path.GetDirectoryName(path));
            File.WriteAllBytes(path, DecodeEmbedded(reader.GetBlobBytes(info.Value)));
        }
    }

    private static byte[] DecodeEmbedded(byte[] blob)
    {
        int length = BitConverter.ToInt32(blob, 0);
        if (length == 0) return blob[4..];
        using var input = new MemoryStream(blob, 4, blob.Length - 4);
        using var gzip = new System.IO.Compression.DeflateStream(input, System.IO.Compression.CompressionMode.Decompress);
        using var output = new MemoryStream(); gzip.CopyTo(output);
        if (output.Length != length) throw new InvalidDataException("Embedded-source length mismatch");
        return output.ToArray();
    }

    private static void ApplyImplicitDeclarations(Dictionary<int, Declaration> declarations, Mono.Cecil.TypeDefinition type)
    {
        foreach (var property in type.Properties)
            if (declarations.TryGetValue(property.MetadataToken.ToInt32(), out var location))
                foreach (var accessor in new[] { property.GetMethod, property.SetMethod }.Where(m => m != null))
                    declarations.TryAdd(accessor.MetadataToken.ToInt32(), location with { Token = accessor.MetadataToken.ToInt32(), Name = accessor.FullName, Kind = "Method" });
        foreach (var @event in type.Events)
            if (declarations.TryGetValue(@event.MetadataToken.ToInt32(), out var location))
                foreach (var accessor in new[] { @event.AddMethod, @event.RemoveMethod, @event.InvokeMethod }.Where(m => m != null))
                    declarations.TryAdd(accessor.MetadataToken.ToInt32(), location with { Token = accessor.MetadataToken.ToInt32(), Name = accessor.FullName, Kind = "Method" });
        if (declarations.TryGetValue(type.MetadataToken.ToInt32(), out var declaration))
        {
            foreach (var constructor in type.Methods.Where(m => m.IsConstructor && !declarations.ContainsKey(m.MetadataToken.ToInt32())))
                declarations.TryAdd(constructor.MetadataToken.ToInt32(), declaration with { Token = constructor.MetadataToken.ToInt32(), Name = constructor.FullName, Kind = "Method" });
            foreach (var field in type.Fields.Where(f => f.IsRuntimeSpecialName))
                declarations.TryAdd(field.MetadataToken.ToInt32(), declaration with { Token = field.MetadataToken.ToInt32(), Name = field.FullName, Kind = "Field" });
        }
        foreach (var nested in type.NestedTypes) ApplyImplicitDeclarations(declarations, nested);
    }

    private static IEnumerable<TypeDefinitionHandle> Descendants(MetadataReader reader, TypeDefinitionHandle handle)
    {
        yield return handle;
        foreach (var child in reader.GetTypeDefinition(handle).GetNestedTypes())
            foreach (var nested in Descendants(reader, child)) yield return nested;
    }
    private static string PathFor(MetadataReader reader, TypeDefinitionHandle handle)
    {
        var type = reader.GetTypeDefinition(handle);
        static string Safe(string name) => string.Concat(name.Select(c => char.IsLetterOrDigit(c) || c is '_' or '-' or '.' ? c : '_'));
        string ns = reader.GetString(type.Namespace), name = reader.GetString(type.Name);
        // Token suffix prevents collisions from generic arity, case-insensitive filesystems,
        // and names whose invalid filename characters normalize to the same spelling.
        return Path.Combine(Safe(ns), Safe(name) + "." + MetadataTokens.GetToken(handle).ToString("X8") + ".cs");
    }

    private sealed class Worker : IDisposable
    {
        private readonly PEFile file;
        private readonly UniversalAssemblyResolver resolver;
        private readonly CSharpDecompiler primary;
        private readonly CSharpDecompiler raw;
        private readonly DecompilerSettings primarySettings;
        private readonly DecompilerSettings rawSettings;
        internal Worker(string path, string dependencies)
        {
            file = new PEFile(path, new MemoryStream(File.ReadAllBytes(path)), PEStreamOptions.PrefetchEntireImage);
            resolver = new UniversalAssemblyResolver(path, true, file.DetectTargetFrameworkId());
            resolver.AddSearchDirectory(dependencies);
            primarySettings = Settings(false); rawSettings = Settings(true);
            primary = new CSharpDecompiler(file, resolver, primarySettings);
            raw = new CSharpDecompiler((DecompilerTypeSystem)primary.TypeSystem, rawSettings);
        }
        private static DecompilerSettings Settings(bool raw) => new()
        {
            ThrowOnAssemblyResolveErrors = true, UseDebugSymbols = false, ShowXmlDocumentation = false,
            UseNestedDirectoriesForNamespaces = true, AnonymousMethods = !raw, AnonymousTypes = !raw,
            AsyncAwait = !raw, YieldReturn = !raw, AutomaticProperties = !raw, AutomaticEvents = !raw,
            GetterOnlyAutomaticProperties = !raw, UseExpressionBodyForCalculatedGetterOnlyProperties = !raw
        };
        internal Source Decompile(TypeDefinitionHandle type, string path, bool raw)
        {
            var decompiler = raw ? this.raw : primary;
            return Render(decompiler, decompiler.DecompileTypes(new[] { type }), path, raw);
        }
        internal Source DecompileMember(EntityHandle member, string path)
        {
            var source = Render(raw, raw.Decompile(member), path, true);
            int token = MetadataTokens.GetToken(member);
            if (!source.Declarations.ContainsKey(token))
            {
                var text = Encoding.UTF8.GetString(source.Content);
                source.Declarations[token] = new Declaration(token, "Metadata declaration 0x" + token.ToString("X8"), member.Kind.ToString(), path, 2, 1, text.Count(c => c == '\n') + 1, 1, true);
            }
            return source;
        }
        private Source Render(CSharpDecompiler decompiler, SyntaxTree tree, string path, bool generated)
        {
            // CLR compiler-generated names such as <Run>d__0 are not legal C#.
            // Keep token identity in the index/PDB while spelling those identifiers
            // legally in the generated view, so one state machine cannot break the
            // semantic parser and navigation for all ordinary members in its file.
            foreach (var identifier in tree.DescendantsAndSelf.OfType<Identifier>())
            {
                string name = identifier.Name;
                if (name.Any(c => !(char.IsLetterOrDigit(c) || c == '_')))
                    identifier.Name = "__generated_" + string.Concat(name.Select(c => char.IsLetterOrDigit(c) || c == '_' ? c.ToString() : "u" + ((int)c).ToString("X4")));
            }
            tree.InsertChildAfter(null, new Comment(" Decompiled from the matching game DLL using the shared BD2 names table. For navigation; not compiled or executed."), Roles.Comment);
            using var text = new StringWriter(System.Globalization.CultureInfo.InvariantCulture);
            var writer = TokenWriter.WrapInWriterThatSetsLocationsInAST(new TextWriterTokenWriter(text));
            tree.AcceptVisitor(new CSharpOutputVisitor(writer, (generated ? rawSettings : primarySettings).CSharpFormattingOptions));
            byte[] content = new UTF8Encoding(false).GetBytes(text.ToString());
            Directory.CreateDirectory(Path.GetDirectoryName(path));
            File.WriteAllBytes(path, content);
            var declarations = new Dictionary<int, Declaration>();
            foreach (var node in tree.DescendantsAndSelf)
            {
                if (node is not EntityDeclaration && node is not VariableInitializer && node is not Accessor) continue;
                if (node.GetSymbol() is not IEntity entity || entity.MetadataToken.IsNil) continue;
                int token = MetadataTokens.GetToken(entity.MetadataToken);
                var start = node is EntityDeclaration declaration && !declaration.NameToken.IsNull ? declaration.NameToken.StartLocation : node.StartLocation;
                var end = node.EndLocation;
                if (start.Line <= 0 || end.Line < start.Line) continue;
                declarations.TryAdd(token, new Declaration(token, entity.ReflectionName, entity.SymbolKind.ToString(), path, start.Line, start.Column, end.Line, end.Column, generated));
            }
            var points = new Dictionary<int, List<Point>>();
            foreach (var function in decompiler.CreateSequencePoints(tree))
            {
                var method = function.Key.MoveNextMethod ?? function.Key.Method;
                if (method == null || method.MetadataToken.IsNil) continue;
                int token = MetadataTokens.GetToken(method.MetadataToken);
                var sequence = function.Value.Where(p => !p.IsHidden).Select(p => new Point(p.Offset, p.StartLine, p.StartColumn, p.EndLine, p.EndColumn)).ToList();
                if (sequence.Count > 0) points.TryAdd(token, sequence);
            }
            return new Source(path, content, declarations, points);
        }
        public void Dispose() => file.Dispose();
    }

    private static (byte[] Bytes, BlobContentId Id) BuildPdb(PEFile pe, ModuleDefinition module, Source[] sources,
        Dictionary<int, Declaration> declarations, Dictionary<int, (Source Source, List<Point> Points)> methods, string dependencies)
    {
        var metadata = new MetadataBuilder();
        var documents = new Dictionary<string, DocumentHandle>(StringComparer.Ordinal);
        var custom = new List<(EntityHandle Parent, Guid Kind, byte[] Bytes)>();
        var csharp = metadata.GetOrAddGuid(CSharpLanguage);
        var sha256 = metadata.GetOrAddGuid(new Guid("8829D00F-11B8-4213-878B-770E8597AC16"));
        foreach (var source in sources)
        {
            var document = metadata.AddDocument(metadata.GetOrAddDocumentName(source.Path), sha256, metadata.GetOrAddBlob(SHA256.HashData(source.Content)), csharp);
            documents.Add(source.Path, document);
            using var stream = new MemoryStream();
            using (var writer = new BinaryWriter(stream, Encoding.UTF8, leaveOpen: true)) writer.Write(source.Content.Length);
            using (var deflate = new System.IO.Compression.DeflateStream(stream, System.IO.Compression.CompressionLevel.Fastest, leaveOpen: true)) deflate.Write(source.Content);
            custom.Add((document, EmbeddedSource, stream.ToArray()));
        }
        var methodDefs = module.GetTypes().SelectMany(t => t.Methods).ToDictionary(m => m.MetadataToken.ToInt32());
        foreach (var handle in pe.Metadata.MethodDefinitions)
        {
            int token = MetadataTokens.GetToken(handle);
            if (!methods.TryGetValue(token, out var mapped)) { metadata.AddMethodDebugInformation(default, default); continue; }
            var points = mapped.Points.Where(p => p.Offset >= 0 && p.Offset < methodDefs[token].Body.CodeSize && p.Line > 0 && p.EndLine >= p.Line)
                .GroupBy(p => p.Offset).Select(g => g.First()).OrderBy(p => p.Offset).ToArray();
            var blob = new BlobBuilder(); blob.WriteCompressedInteger(0); // no local signature is needed for navigation
            int previousOffset = 0, previousLine = 0, previousColumn = 0;
            for (int i = 0; i < points.Length; i++)
            {
                var p = points[i]; blob.WriteCompressedInteger(i == 0 ? p.Offset : p.Offset - previousOffset);
                int lines = p.EndLine - p.Line; blob.WriteCompressedInteger(lines);
                int columns = p.EndColumn - p.Column;
                if (lines == 0) blob.WriteCompressedInteger(Math.Max(1, columns)); else blob.WriteCompressedSignedInteger(columns);
                if (i == 0) { blob.WriteCompressedInteger(p.Line); blob.WriteCompressedInteger(p.Column); }
                else { blob.WriteCompressedSignedInteger(p.Line - previousLine); blob.WriteCompressedSignedInteger(p.Column - previousColumn); }
                previousOffset = p.Offset; previousLine = p.Line; previousColumn = p.Column;
            }
            metadata.AddMethodDebugInformation(documents[mapped.Source.Path], metadata.GetOrAddBlob(blob));
        }
        foreach (var type in module.GetTypes())
        {
            if (!declarations.TryGetValue(type.MetadataToken.ToInt32(), out var d)) continue;
            var docs = new BlobBuilder(); docs.WriteCompressedInteger(MetadataTokens.GetRowNumber(documents[d.File]));
            custom.Add((MetadataTokens.EntityHandle(type.MetadataToken.ToInt32()), TypeDocuments, docs.ToArray()));
        }
        var options = Encoding.UTF8.GetBytes("language\0C#\0language-version\0" + "12.0\0compiler-version\0BD2.GameSdk\0output-kind\0DynamicallyLinkedLibrary\0optimization\0debug\0");
        custom.Add((MetadataTokens.EntityHandle(1), CompilationOptions, options));
        var references = new BlobBuilder();
        foreach (var reference in module.AssemblyReferences)
        {
            string path = Path.Combine(dependencies, reference.Name + ".dll");
            if (!File.Exists(path)) continue;
            using var stream = File.OpenRead(path); using var dependency = new PEReader(stream);
            var reader = dependency.GetMetadataReader();
            references.WriteBytes(Encoding.UTF8.GetBytes(Path.GetFileName(path))); references.WriteByte(0);
            references.WriteByte(0); // global alias
            references.WriteByte(1); // combined embedInteropTypes=false, image kind=assembly
            references.WriteUInt32((uint)dependency.PEHeaders.CoffHeader.TimeDateStamp);
            references.WriteUInt32((uint)dependency.PEHeaders.PEHeader.SizeOfImage);
            references.WriteBytes(reader.GetGuid(reader.GetModuleDefinition().Mvid).ToByteArray());
        }
        custom.Add((MetadataTokens.EntityHandle(1), CompilationReferences, references.ToArray()));
        foreach (var info in custom.OrderBy(c => CodedIndex.HasCustomDebugInformation(c.Parent)))
            metadata.AddCustomDebugInformation(info.Parent, metadata.GetOrAddGuid(info.Kind), metadata.GetOrAddBlob(info.Bytes));
        var counts = Enumerable.Range(0, 64).Select(i => pe.Metadata.GetTableRowCount((TableIndex)i)).ToImmutableArray();
        var builder = new PortablePdbBuilder(metadata, counts, default, blobs =>
        {
            using var hash = IncrementalHash.CreateHash(HashAlgorithmName.SHA256);
            foreach (var blob in blobs) hash.AppendData(blob.GetBytes());
            return BlobContentId.FromHash(hash.GetHashAndReset());
        });
        var output = new BlobBuilder(); var id = builder.Serialize(output);
        return (output.ToArray(), id);
    }

    private static void WriteXmlDocumentation(string assembly, PEFile pe, Dictionary<int, Declaration> declarations)
    {
        var settings = new DecompilerSettings();
        var resolver = new UniversalAssemblyResolver(assembly, false, pe.DetectTargetFrameworkId());
        var decompiler = new CSharpDecompiler(pe, resolver, settings);
        using var writer = System.Xml.XmlWriter.Create(Path.ChangeExtension(assembly, ".xml"), new System.Xml.XmlWriterSettings { Indent = true, Encoding = new UTF8Encoding(false) });
        writer.WriteStartElement("doc"); writer.WriteStartElement("assembly"); writer.WriteElementString("name", Program.ShellName); writer.WriteEndElement(); writer.WriteStartElement("members");
        // Method signatures already provide IntelliSense, and PDB supplies exact
        // navigation. Repeating the same boilerplate for every member inflated
        // each XML file to hundreds of MB without adding API documentation.
        foreach (IEntity entity in decompiler.TypeSystem.MainModule.TypeDefinitions)
            {
                if (entity.MetadataToken.IsNil || !declarations.TryGetValue(MetadataTokens.GetToken(entity.MetadataToken), out var location)) continue;
                string id = ICSharpCode.Decompiler.Documentation.IdStringProvider.GetIdString(entity);
                writer.WriteStartElement("member"); writer.WriteAttributeString("name", id);
                writer.WriteElementString("summary", "Readable game type. Decompiled source: " + Path.GetRelativePath(Path.Combine(Path.GetDirectoryName(assembly), "sources"), location.File) + ":" + location.Line);
                writer.WriteElementString("remarks", "Navigation-only reference; the runtime implementation is in the matching Assembly-CSharp.");
                writer.WriteEndElement();
            }
        writer.WriteEndElement(); writer.WriteEndElement();
    }

    private sealed class NavigationSymbolWriterProvider(BlobContentId id, string pdbPath, byte[] pdb) : ISymbolWriterProvider
    {
        public ISymbolWriter GetSymbolWriter(ModuleDefinition module, string fileName) => new Writer(id, pdbPath, pdb);
        public ISymbolWriter GetSymbolWriter(ModuleDefinition module, Stream symbolStream) => throw new NotSupportedException();
        private sealed class Writer(BlobContentId id, string path, byte[] pdb) : ISymbolWriter
        {
            public ISymbolReaderProvider GetReaderProvider() => new PortablePdbReaderProvider();
            public void Write(MethodDebugInformation info) { }
            public void Write(ICustomDebugInformationProvider provider) { }
            public void Write() { }
            public void Dispose() { }
            public ImageDebugHeader GetDebugHeader()
            {
                using var stream = new MemoryStream(); using var writer = new BinaryWriter(stream);
                writer.Write(0x53445352); writer.Write(id.Guid.ToByteArray()); writer.Write(1); writer.Write(Encoding.UTF8.GetBytes(path)); writer.Write((byte)0);
                byte[] codeview = stream.ToArray();
                return new ImageDebugHeader(new[] {
                    new ImageDebugHeaderEntry(new ImageDebugDirectory { Type = ImageDebugType.CodeView, MajorVersion = 0x100, MinorVersion = 0x504d, TimeDateStamp = (int)id.Stamp, SizeOfData = codeview.Length }, codeview),
                    new ImageDebugHeaderEntry(new ImageDebugDirectory { Type = ImageDebugType.PdbChecksum, SizeOfData = 39 }, Encoding.UTF8.GetBytes("SHA256\0").Concat(SHA256.HashData(pdb)).ToArray())
                });
            }
        }
    }
}
