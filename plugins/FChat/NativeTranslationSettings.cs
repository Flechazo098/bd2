using Bd2LoginUI;
using TextComponent = BD2.GameSdk.Component;
using System;
using System.IO;
using System.Security.Cryptography;
using System.Text;
using BepInEx;
using UnityEngine;

namespace Bd2FChat;

[Serializable]
internal sealed class TranslationConfig
{
    public bool Enabled;
    public string ApiUrl = string.Empty;
    public string Model = string.Empty;
    public string ApiKey = string.Empty;
}

internal static class NativeTranslationSettings
{
    private const string Prefix = "BD2NativeTranslationConfigV1_";
    internal static bool HasSavedKey => PlatformSecretStore.Contains(Scope());

    internal static TranslationConfig Load()
    {
        string json = PlayerPrefs.GetString(Prefix + Scope(), string.Empty);
        if (string.IsNullOrEmpty(json)) return new TranslationConfig();
        TranslationConfig config = JsonUtility.FromJson<TranslationConfig>(json) ?? new TranslationConfig();
        config.ApiKey = string.Empty;
        return config;
    }

    internal static void Save(TranslationConfig draft)
    {
        TranslationConfig previous = Load();
        TranslationConfig config = Validate(draft, draft?.Enabled == true);
        string scope = Scope();
        string key = config.ApiKey;
        if (key.Length == 0 && config.ApiUrl == previous.ApiUrl) key = PlatformSecretStore.Read(scope);
        if (config.Enabled && key.Length == 0) throw new ArgumentException(TextComponent.Translatable("fchat.error.key_required").GetString());
        if (key.Length != 0) PlatformSecretStore.Write(scope, key);
        else PlatformSecretStore.Delete(scope);
        config.ApiKey = string.Empty;
        PlayerPrefs.SetString(Prefix + scope, JsonUtility.ToJson(config));
        PlayerPrefs.Save();
        NativeChatTranslation.Reset();
    }

    internal static void Clear()
    {
        string scope = Scope();
        PlatformSecretStore.Delete(scope);
        PlayerPrefs.DeleteKey(Prefix + scope);
        PlayerPrefs.Save();
        NativeChatTranslation.Reset();
    }

    internal static TranslationConfig ForRequest(TranslationConfig draft = null)
    {
        TranslationConfig saved = Load();
        TranslationConfig config = Validate(draft ?? saved, true);
        if (config.ApiKey.Length == 0 && config.ApiUrl == saved.ApiUrl) config.ApiKey = PlatformSecretStore.Read(Scope());
        if (config.ApiKey.Length == 0) throw new ArgumentException(TextComponent.Translatable("fchat.error.key_required").GetString());
        return config;
    }

    private static TranslationConfig Validate(TranslationConfig input, bool complete)
    {
        if (input == null) throw new ArgumentException(TextComponent.Translatable("fchat.error.config_empty").GetString());
        var config = new TranslationConfig { Enabled = input.Enabled, ApiUrl = (input.ApiUrl ?? string.Empty).Trim(), Model = (input.Model ?? string.Empty).Trim(), ApiKey = (input.ApiKey ?? string.Empty).Trim() };
        if (complete || config.ApiUrl.Length != 0)
        {
            if (!Uri.TryCreate(config.ApiUrl, UriKind.Absolute, out Uri uri) ||
                (uri.Scheme != Uri.UriSchemeHttps && !(uri.Scheme == Uri.UriSchemeHttp && uri.IsLoopback)) ||
                uri.UserInfo.Length != 0 || uri.Query.Length != 0 || uri.Fragment.Length != 0 || string.IsNullOrEmpty(uri.Host))
                throw new ArgumentException(TextComponent.Translatable("fchat.error.endpoint_invalid").GetString());
            string path = uri.AbsolutePath.TrimEnd('/');
            if (!path.EndsWith("/chat/completions", StringComparison.Ordinal)) path += "/chat/completions";
            var builder = new UriBuilder(uri) { Path = path };
            config.ApiUrl = builder.Uri.AbsoluteUri;
        }
        if ((complete && config.Model.Length == 0) || config.Model.Length > 200 || HasControl(config.Model))
            throw new ArgumentException(TextComponent.Translatable("fchat.error.model_invalid").GetString());
        if (config.ApiKey.Length > 16384 || HasControl(config.ApiKey)) throw new ArgumentException(TextComponent.Translatable("fchat.error.key_invalid").GetString());
        return config;
    }

    private static bool HasControl(string value) { foreach (char c in value) if (char.IsControl(c)) return true; return false; }

    private static string NormalizeOrigin(Uri uri)
    {
        if (!uri.IsAbsoluteUri || string.IsNullOrEmpty(uri.Host) ||
            (uri.Scheme != Uri.UriSchemeHttp && uri.Scheme != Uri.UriSchemeHttps) || uri.UserInfo.Length != 0)
            throw new InvalidOperationException(TextComponent.Translatable("fchat.error.server_unconfigured").GetString());
        var builder = new UriBuilder(uri.Scheme.ToLowerInvariant(), uri.IdnHost.ToLowerInvariant(),
            uri.IsDefaultPort ? -1 : uri.Port);
        return builder.Uri.GetLeftPart(UriPartial.Authority).TrimEnd('/');
    }
    private static string Scope()
    {
        Uri server = ClientSession.ServerOrigin ?? throw new InvalidOperationException(TextComponent.Translatable("fchat.error.server_unconfigured").GetString());
        string root = Environment.GetEnvironmentVariable("BD2_CLIENT_INSTANCE_ROOT");
        root = Path.GetFullPath(string.IsNullOrEmpty(root) ? Paths.GameRootPath : root).TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar);
        if (Environment.OSVersion.Platform == PlatformID.Win32NT) root = root.ToUpperInvariant();
        string identity = NormalizeOrigin(server) + "\0" + root;
        using var hash = SHA256.Create();
        return BitConverter.ToString(hash.ComputeHash(Encoding.UTF8.GetBytes(identity))).Replace("-", string.Empty);
    }
}
