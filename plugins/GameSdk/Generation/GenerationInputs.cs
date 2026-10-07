using System.Security.Cryptography;
using System.Text;
using ICSharpCode.Decompiler.CSharp;
using Mono.Cecil;

namespace BD2.GameSdk.Generation;

internal static class GenerationInputs
{
    internal static string Hash(string path)
    {
        using FileStream stream = File.OpenRead(path);
        return Convert.ToHexString(SHA256.HashData(stream)).ToLowerInvariant();
    }

    internal static string Stamp() => SourceStamp("BD2.GameSdk.Generation.") + "|" +
        Hash(typeof(CSharpDecompiler).Assembly.Location) + "|" + Hash(typeof(ModuleDefinition).Assembly.Location);

    internal static string NamesStamp() => Convert.ToHexString(SHA256.HashData(
        Encoding.UTF8.GetBytes(Stamp() + "|" + SourceStamp("BD2.GameSdk.Names.")))).ToLowerInvariant();

    private static string SourceStamp(string prefix)
    {
        System.Reflection.Assembly assembly = typeof(GenerationInputs).Assembly;
        string[] resources = [.. assembly.GetManifestResourceNames()
            .Where(name => name.StartsWith(prefix, StringComparison.Ordinal))
            .OrderBy(name => name, StringComparer.Ordinal)];
        if (resources.Length == 0) throw new InvalidDataException("SDK generation inputs are missing; rebuild the SDK");
        using var hash = IncrementalHash.CreateHash(HashAlgorithmName.SHA256);
        foreach (string name in resources)
        {
            hash.AppendData(Encoding.UTF8.GetBytes(name + "\0"));
            using Stream stream = assembly.GetManifestResourceStream(name);
            hash.AppendData(SHA256.HashData(stream));
        }
        return Convert.ToHexString(hash.GetHashAndReset()).ToLowerInvariant();
    }
}
