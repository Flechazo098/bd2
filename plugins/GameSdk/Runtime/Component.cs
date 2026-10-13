using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Runtime.Serialization.Json;
using System.Text;
using Proto.Net;

namespace BD2.GameSdk;

internal sealed class Component
{
    private static readonly Lazy<Dictionary<string, Dictionary<string, string>>> Translations = new(Load);
    private readonly string content;
    private readonly object[] arguments;

    private Component(string content, object[] arguments)
    {
        this.content = content;
        this.arguments = arguments;
    }

    internal static Component Literal(string text)
    {
        if (text == null) throw new ArgumentNullException(nameof(text));
        return new Component(text, null);
    }

    internal static Component Translatable(string key, params object[] arguments)
    {
        if (key == null) throw new ArgumentNullException(nameof(key));
        if (arguments == null) throw new ArgumentNullException(nameof(arguments));
        return new Component(key, (object[])arguments.Clone());
    }

    internal string GetString()
    {
        if (arguments == null) return content;
        string locale = UserSettingInfo.LanguageGame switch
        {
            Define_LanguageType.Korea => "ko_kr",
            Define_LanguageType.English => "en_us",
            Define_LanguageType.Japan => "ja_jp",
            Define_LanguageType.Taiwan => "zh_tw",
            Define_LanguageType.China => "zh_cn",
            _ => throw new InvalidOperationException("The game selected an unsupported text language.")
        };
        Dictionary<string, Dictionary<string, string>> translations = Translations.Value;
        if (!(translations.TryGetValue(locale, out Dictionary<string, string> selected) && selected.TryGetValue(content, out string text)) &&
            !(translations.TryGetValue("en_us", out Dictionary<string, string> fallback) && fallback.TryGetValue(content, out text)))
            text = content;
        try { return Format(text, arguments); }
        catch (FormatException) { return text; }
    }

    public override string ToString() => GetString();

    private static Dictionary<string, Dictionary<string, string>> Load()
    {
        var result = new Dictionary<string, Dictionary<string, string>>(StringComparer.Ordinal);
        var serializer = new DataContractJsonSerializer(typeof(Dictionary<string, string>),
            new DataContractJsonSerializerSettings { UseSimpleDictionaryFormat = true });
        foreach (string locale in new[] { "en_us", "ko_kr", "ja_jp", "zh_tw", "zh_cn" })
        {
            using Stream stream = typeof(Component).Assembly.GetManifestResourceStream("BD2.GameSdk.Lang." + locale + ".json");
            if (stream != null)
            {
                using var reader = new StreamReader(stream, Encoding.UTF8, true);
                using var content = new MemoryStream(Encoding.UTF8.GetBytes(reader.ReadToEnd()));
                var entries = (Dictionary<string, string>)serializer.ReadObject(content);
                result.Add(locale, new Dictionary<string, string>(entries, StringComparer.Ordinal));
            }
        }
        return result;
    }

    private static string Format(string text, object[] arguments) =>
        string.Format(CultureInfo.CurrentCulture, text, arguments);
}
