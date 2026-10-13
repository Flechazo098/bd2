using System.Diagnostics;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using Mono.Cecil;
using static BD2.GameSdk.Generation.GenerationInputs;

namespace BD2.GameSdk;

internal static class ReobfuscationCache
{
    private sealed record Entry(string Inputs, string Output);

    internal static void Run(string table, string input, string output, string originalAssembly, string dependencyDirectory)
    {
        var elapsed = Stopwatch.StartNew();
        output = Path.GetFullPath(output);
        if (string.Equals(Path.GetFullPath(input), output, StringComparison.OrdinalIgnoreCase))
            throw new ArgumentException("Reobfuscation output must differ from the compiler input");
        Directory.CreateDirectory(Path.GetDirectoryName(output));
        using FileStream outputLock = Lock(output + ".lock");
        string fingerprint = Inputs(table, input, output, originalAssembly, dependencyDirectory);
        string stamp = output + ".inputs.json";
        Entry cached = ReadEntry(stamp);
        bool hit = cached != null && cached.Inputs == fingerprint && File.Exists(output) && cached.Output == Hash(output);
        if (!hit)
        {
            Program.Reobfuscate(table, input, output, originalAssembly, dependencyDirectory);
            string temporary = stamp + "." + Guid.NewGuid().ToString("N") + ".tmp";
            try
            {
                File.WriteAllText(temporary, JsonSerializer.Serialize(new Entry(fingerprint, Hash(output))));
                File.Move(temporary, stamp, overwrite: true);
            }
            finally { File.Delete(temporary); }
        }
        Console.WriteLine($"[BD2 build] reobf plugin={Path.GetFileName(input)} cache={(hit ? "hit" : "miss")} elapsed_ms={elapsed.ElapsedMilliseconds}");
    }

    private static string Inputs(string table, string input, string output, string originalAssembly, string dependencyDirectory)
    {
        var files = new SortedSet<string>(StringComparer.Ordinal)
        {
            Path.GetFullPath(table), Path.GetFullPath(input), typeof(ReobfuscationCache).Assembly.Location,
            typeof(ModuleDefinition).Assembly.Location
        };
        var directories = new SortedSet<string>(StringComparer.Ordinal)
        {
            Path.GetDirectoryName(Path.GetFullPath(table)), Path.GetDirectoryName(Path.GetFullPath(input)),
            Path.GetDirectoryName(output)
        };
        if (originalAssembly != null)
        {
            files.Add(Path.GetFullPath(originalAssembly));
            directories.Add(Path.GetDirectoryName(Path.GetFullPath(originalAssembly)));
        }
        if (dependencyDirectory != null) directories.Add(Path.GetFullPath(dependencyDirectory));
        foreach (string directory in directories)
            if (Directory.Exists(directory))
                foreach (string file in Directory.EnumerateFiles(directory, "*.dll"))
                    if (!string.Equals(Path.GetFullPath(file), output, StringComparison.OrdinalIgnoreCase)) files.Add(Path.GetFullPath(file));
        using var hash = IncrementalHash.CreateHash(HashAlgorithmName.SHA256);
        foreach (string directory in directories) hash.AppendData(Encoding.UTF8.GetBytes(directory + "\0" + Directory.Exists(directory) + "\0"));
        foreach (string file in files) hash.AppendData(Encoding.UTF8.GetBytes(file + "\0" + Hash(file) + "\0"));
        return Convert.ToHexString(hash.GetHashAndReset()).ToLowerInvariant();
    }

    private static Entry ReadEntry(string path)
    {
        if (!File.Exists(path)) return null;
        try { return JsonSerializer.Deserialize<Entry>(File.ReadAllText(path)); }
        catch (JsonException) { return null; }
    }

    private static FileStream Lock(string path)
    {
        var elapsed = Stopwatch.StartNew();
        bool announced = false;
        while (true)
        {
            try { return new FileStream(path, FileMode.OpenOrCreate, FileAccess.ReadWrite, FileShare.None); }
            catch (IOException error) when (elapsed.Elapsed < TimeSpan.FromMinutes(30) &&
                error.HResult is unchecked((int)0x80070020) or unchecked((int)0x80070021))
            {
                if (!announced) { Console.WriteLine("Waiting for this plugin's reobfuscation cache to finish..."); announced = true; }
                Thread.Sleep(100);
            }
        }
    }
}
