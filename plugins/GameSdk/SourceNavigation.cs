using System.Globalization;
using System.Reflection.Metadata;
using System.Reflection.Metadata.Ecma335;
using System.Reflection.PortableExecutable;
using System.Security.Cryptography;
using System.Text.Json;

using BD2.GameSdk.Generation;
using static BD2.GameSdk.Generation.SourceNavigation;

namespace BD2.GameSdk;

internal static class SourceNavigationVerifier
{
    internal static void Verify(string sdkDirectory)
    {
        sdkDirectory = Program.ResolveSdkDirectory(sdkDirectory);
        string library = Path.Combine(sdkDirectory, "lib", ShellGenerator.ShellName + ".dll"), pdbPath = Path.ChangeExtension(library, ".pdb");
        Manifest manifest = JsonSerializer.Deserialize<Manifest>(File.ReadAllText(Path.Combine(sdkDirectory, "navigation.json")));
        if (GenerationInputs.Hash(library) != manifest.AssemblySha256 || GenerationInputs.Hash(pdbPath) != manifest.PdbSha256)
            throw new InvalidDataException("Source navigation manifest, DLL and PDB do not match");
        using FileStream peStream = File.OpenRead(library); using var pe = new PEReader(peStream);
        using FileStream pdbStream = File.OpenRead(pdbPath); using var provider = MetadataReaderProvider.FromPortablePdbStream(pdbStream);
        MetadataReader pdb = provider.GetMetadataReader(); MetadataReader dll = pe.GetMetadataReader();
        var id = new BlobContentId(pdb.DebugMetadataHeader.Id);
        DebugDirectoryEntry entry = pe.ReadDebugDirectory().Single(d => d.Type == DebugDirectoryEntryType.CodeView);
        CodeViewDebugDirectoryData codeview = pe.ReadCodeViewDebugDirectoryData(entry);
        if (codeview.Guid != id.Guid || entry.Stamp != id.Stamp || codeview.Age != 1) throw new InvalidDataException("PDB identity does not match navigation assembly");
        DebugDirectoryEntry checksum = pe.ReadDebugDirectory().Single(d => d.Type == DebugDirectoryEntryType.PdbChecksum);
        if (!pe.ReadPdbChecksumDebugDirectoryData(checksum).Checksum.SequenceEqual(SHA256.HashData(File.ReadAllBytes(pdbPath))))
            throw new InvalidDataException("PE/PDB checksum mismatch");
        int documents = 0, methods = 0;
        foreach (DocumentHandle documentHandle in pdb.Documents)
        {
            System.Reflection.Metadata.Document document = pdb.GetDocument(documentHandle);
            if (pdb.GetGuid(document.Language) != CSharpLanguage) throw new InvalidDataException("Navigation document language is not C#");
            System.Reflection.Metadata.CustomDebugInformation embedded = pdb.GetCustomDebugInformation(documentHandle).Select(h => pdb.GetCustomDebugInformation(h)).Single(c => pdb.GetGuid(c.Kind) == EmbeddedSource);
            byte[] content = DecodeEmbedded(pdb.GetBlobBytes(embedded.Value));
            if (!SHA256.HashData(content).SequenceEqual(pdb.GetBlobBytes(document.Hash))) throw new InvalidDataException("Embedded-source checksum mismatch");
            string path = pdb.GetString(document.Name);
            if (!File.Exists(path) || !File.ReadAllBytes(path).SequenceEqual(content)) throw new InvalidDataException("Local source does not match embedded source: " + path);
            documents++;
        }
        foreach (MethodDefinitionHandle method in dll.MethodDefinitions)
        {
            System.Reflection.Metadata.MethodDefinition definition = dll.GetMethodDefinition(method);
            if (definition.RelativeVirtualAddress == 0) continue;
            System.Reflection.Metadata.MethodDebugInformation debug = pdb.GetMethodDebugInformation(method);
            System.Reflection.Metadata.SequencePoint[] points = [.. debug.GetSequencePoints()];
            if (debug.Document.IsNil || points.Length == 0) throw new InvalidDataException("Method lacks navigation symbols: 0x" + MetadataTokens.GetToken(method).ToString("X8", CultureInfo.InvariantCulture));
            int size = pe.GetMethodBody(definition.RelativeVirtualAddress).GetILContent().Length;
            if (points.Any(p => !p.IsHidden && (p.Offset >= size || p.StartLine <= 0 || p.EndLine < p.StartLine))) throw new InvalidDataException("Invalid navigation sequence point");
            methods++;
        }
        foreach (TypeDefinitionHandle type in dll.TypeDefinitions)
        {
            if (dll.GetString(dll.GetTypeDefinition(type).Name) == "<Module>") continue;
            System.Reflection.Metadata.CustomDebugInformation custom = pdb.GetCustomDebugInformation(type).Select(h => pdb.GetCustomDebugInformation(h)).SingleOrDefault(c => pdb.GetGuid(c.Kind) == TypeDocuments);
            if (custom.Value.IsNil) throw new InvalidDataException("Type lacks source documents: 0x" + MetadataTokens.GetToken(type).ToString("X8", CultureInfo.InvariantCulture));
            BlobReader blob = pdb.GetBlobReader(custom.Value);
            while (blob.RemainingBytes > 0) _ = pdb.GetDocument(MetadataTokens.DocumentHandle(blob.ReadCompressedInteger()));
        }
        if (documents != manifest.Documents || methods != manifest.MethodsWithBodies || manifest.Symbols.Select(s => s.Token).Distinct().Count() != manifest.Declarations)
            throw new InvalidDataException("Navigation coverage counts do not match manifest");
        Console.WriteLine($"Verified source navigation: {documents} embedded documents, {manifest.Declarations} declarations, {methods} method bodies; PE/PDB identity, source checksums and type documents match");
    }

    internal static void RestoreSources(string sdkDirectory)
    {
        using FileStream stream = File.OpenRead(Path.Combine(sdkDirectory, "lib", ShellGenerator.ShellName + ".pdb"));
        using var provider = MetadataReaderProvider.FromPortablePdbStream(stream);
        MetadataReader reader = provider.GetMetadataReader();
        foreach (DocumentHandle handle in reader.Documents)
        {
            string path = reader.GetString(reader.GetDocument(handle).Name);
            if (File.Exists(path)) continue;
            System.Reflection.Metadata.CustomDebugInformation info = reader.GetCustomDebugInformation(handle).Select(h => reader.GetCustomDebugInformation(h)).Single(c => reader.GetGuid(c.Kind) == EmbeddedSource);
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

}
