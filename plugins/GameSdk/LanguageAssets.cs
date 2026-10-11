using System.Text.Json;

namespace BD2.GameSdk;

internal static class LanguageAssets
{
    private static readonly HashSet<string> FileNames = new(StringComparer.Ordinal)
    {
        "en_us.json", "ko_kr.json", "ja_jp.json", "zh_tw.json", "zh_cn.json"
    };

    internal static void Validate(string directory)
    {
        foreach (string path in Directory.EnumerateFiles(directory, "*", SearchOption.AllDirectories))
        {
            string name = Path.GetRelativePath(directory, path);
            if (!FileNames.Contains(name))
                throw new InvalidDataException($"Unsupported language asset '{name}'. Brown Dust II text languages require en_us.json, ko_kr.json, ja_jp.json, zh_tw.json or zh_cn.json directly in Assets/Lang.");
            using var json = JsonDocument.Parse(File.ReadAllText(path));
            if (json.RootElement.ValueKind != JsonValueKind.Object)
                throw new InvalidDataException($"Language asset '{name}' must be a flat JSON object of translation keys and string values.");
            var keys = new HashSet<string>(StringComparer.Ordinal);
            foreach (JsonProperty property in json.RootElement.EnumerateObject())
            {
                if (property.Value.ValueKind != JsonValueKind.String)
                    throw new InvalidDataException($"Language asset '{name}' contains a non-string value at '{property.Name}'.");
                if (!keys.Add(property.Name))
                    throw new InvalidDataException($"Language asset '{name}' repeats translation key '{property.Name}'.");
            }
        }
    }
}
