using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using BepInEx;
using Newtonsoft.Json;
using Newtonsoft.Json.Linq;

namespace Bd2.Client;

internal sealed class ClientSettings
{
    private static readonly Lazy<ClientSettings> Loaded = new(() => ParseConfig(ReadFile(Path.Combine(Paths.ConfigPath, "bd2.client.json"))));
    private static readonly Lazy<ClientInstance> Instance = new(FindInstance);
    private static readonly char[] InvalidOriginCharacters = ['?', '#', '@', '%'];
    internal static ClientSettings Current => Loaded.Value;
    internal static ClientInstance CurrentInstance => Instance.Value;
    internal int schema_version;
    internal string server_origin;
    internal string cdn_mode;
    internal string local_resource_directory;
    internal string proxy_url;
    internal ClientRuntime runtime;
    internal string BundleVersion => string.IsNullOrEmpty(runtime.bundle_version) ? Bd2Build.Versions.Bundle : runtime.bundle_version;
    internal string GameDataVersion => string.IsNullOrEmpty(runtime.game_data_version) ? Bd2Build.Versions.GameData : runtime.game_data_version;
    internal string ClientVersion => string.IsNullOrEmpty(runtime.client_version) ? Bd2Build.Versions.ClientRelease : runtime.client_version;

    private static ClientRuntime ParseRuntime(JToken token)
    {
        if (token == null || token.Type == JTokenType.Null) return new ClientRuntime();
        if (token is not JObject value) throw new InvalidDataException("runtime must be an object");
        RequireOnly(value, "client_version", "update_fingerprint", "resource_fingerprint", "game_version", "bundle_version", "game_data_version", "plugin_files");
        var runtime = new ClientRuntime
        {
            client_version = OptionalString(value, "client_version"),
            update_fingerprint = OptionalString(value, "update_fingerprint"),
            resource_fingerprint = OptionalString(value, "resource_fingerprint"),
            game_version = OptionalString(value, "game_version"),
            bundle_version = OptionalString(value, "bundle_version"),
            game_data_version = OptionalString(value, "game_data_version")
        };
        if (value["plugin_files"] is JToken files && files.Type != JTokenType.Null)
        {
            if (files is not JObject entries) throw new InvalidDataException("runtime.plugin_files must be an object");
            foreach (JProperty entry in entries.Properties())
            {
                string file = RequiredString(entries, entry.Name);
                if (string.IsNullOrWhiteSpace(entry.Name) || Path.GetFileName(file) != file ||
                    file.IndexOfAny(['/', '\\', ':']) >= 0 || !file.EndsWith(".dll", StringComparison.OrdinalIgnoreCase))
                    throw new InvalidDataException("runtime.plugin_files must map plugin IDs to DLL file names");
                runtime.plugin_files.Add(entry.Name, file);
            }
        }
        ValidateFingerprint(runtime.update_fingerprint, "update_fingerprint");
        ValidateFingerprint(runtime.resource_fingerprint, "resource_fingerprint");
        ValidateResourceVersion(runtime.bundle_version, "bundle_version");
        ValidateResourceVersion(runtime.game_data_version, "game_data_version");
        return runtime;
    }

    private static void ValidateFingerprint(string value, string name)
    {
        if (value.Length == 0) return;
        if (value.Length != 64) throw new InvalidDataException("runtime." + name + " must be a SHA-256 fingerprint");
        foreach (char digit in value)
            if (!(digit >= '0' && digit <= '9') && !(digit >= 'a' && digit <= 'f'))
                throw new InvalidDataException("runtime." + name + " must be a lowercase SHA-256 fingerprint");
    }

    private static void ValidateResourceVersion(string value, string name)
    {
        if (value.Length == 0) return;
        if (value.Length != 14)
            throw new InvalidDataException("runtime." + name + " must be a 14-digit resource version");
        foreach (char digit in value)
            if (digit < '0' || digit > '9') throw new InvalidDataException("runtime." + name + " must contain only digits");
    }

    private static void ValidateOrigin(string raw)
    {
        if (!Uri.TryCreate(raw, UriKind.Absolute, out Uri uri) || string.IsNullOrEmpty(uri.Host) ||
            !string.IsNullOrEmpty(uri.UserInfo) || uri.AbsolutePath != "/" ||
            !string.IsNullOrEmpty(uri.Query) || !string.IsNullOrEmpty(uri.Fragment) ||
            (uri.Scheme != Uri.UriSchemeHttps && !(uri.Scheme == Uri.UriSchemeHttp && uri.IsLoopback)))
            throw new InvalidDataException("server_origin must be an HTTPS origin, except that loopback may use HTTP");
    }

    private static ClientInstance FindInstance()
    {
        DirectoryInfo game = new(Path.GetFullPath(Paths.GameRootPath));
        DirectoryInfo container = game.Name.EndsWith(".app", StringComparison.OrdinalIgnoreCase) ? game.Parent : game;
        DirectoryInfo root = container?.Parent;
        if (root == null || !File.Exists(Path.Combine(root.FullName, "instance.json"))) return null;
        if (container.Name != "game") throw new InvalidDataException("Client instance game directory must be named game");
        JObject manifest = ParseObject(ReadFile(Path.Combine(root.FullName, "instance.json")), "client instance manifest");
        RequireOnly(manifest, "schema_version", "id", "source", "game_version");
        if (RequiredInteger(manifest, "schema_version") != 1) throw new InvalidDataException("Unsupported client instance manifest schema_version");
        string id = RequiredString(manifest, "id");
        if (id.Length > 32 || id == "default" || root.Name != id) throw new InvalidDataException("Invalid client instance name");
        foreach (char character in id)
            if (!(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '-' && character != '_')
                throw new InvalidDataException("Invalid client instance name");
        return new ClientInstance(id, root.FullName);
    }

    private static string ReadFile(string path)
    {
        using var stream = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read);
        const int maximumBytes = 16 * 1024;
        if (stream.Length > maximumBytes) throw new InvalidDataException("Client JSON document exceeds 16384 bytes");
        using var reader = new StreamReader(stream, new UTF8Encoding(false, true));
        return reader.ReadToEnd();
    }

    private static ClientSettings ParseConfig(string json)
    {
        JObject value = ParseObject(json, "client configuration");
        RequireOnly(value, "schema_version", "server_origin", "cdn_mode", "local_resource_directory", "proxy_url", "runtime");
        var config = new ClientSettings
        {
            schema_version = RequiredInteger(value, "schema_version"),
            server_origin = RequiredString(value, "server_origin"),
            cdn_mode = RequiredString(value, "cdn_mode"),
            local_resource_directory = OptionalString(value, "local_resource_directory"),
            proxy_url = NormalizeClientProxy(OptionalString(value, "proxy_url")),
            runtime = ParseRuntime(value["runtime"])
        };
        if (config.schema_version != 2)
        {
            throw new InvalidDataException("bd2.client.json schema_version must be 2");
        }
        if (config.cdn_mode != "official" && config.cdn_mode != "local" && config.cdn_mode != "server")
        {
            throw new InvalidDataException("bd2.client.json cdn_mode must be official, local, or server");
        }
        if (config.cdn_mode == "local" && string.IsNullOrWhiteSpace(config.local_resource_directory))
        {
            throw new InvalidDataException("local_resource_directory is required when cdn_mode is local");
        }
        if (config.cdn_mode != "local" && !string.IsNullOrWhiteSpace(config.local_resource_directory))
        {
            throw new InvalidDataException("local_resource_directory is only valid when cdn_mode is local");
        }
        ValidateOrigin(config.server_origin);
        config.server_origin = config.server_origin.TrimEnd('/');
        return config;
    }

    private static string NormalizeClientProxy(string raw)
    {
        raw = raw.Trim();
        if (raw.Length == 0) return string.Empty;
        const string error = "proxy_url must be an HTTP proxy address with an explicit port and no credentials or path";
        if (raw.IndexOfAny(InvalidOriginCharacters) >= 0) throw new InvalidDataException(error);
        foreach (char character in raw)
            if (char.IsWhiteSpace(character) || char.IsControl(character)) throw new InvalidDataException(error);
        if (!Uri.TryCreate(raw, UriKind.Absolute, out Uri proxy) || proxy.Scheme != "http" ||
            string.IsNullOrEmpty(proxy.Host) || !string.IsNullOrEmpty(proxy.UserInfo) ||
            !string.IsNullOrEmpty(proxy.Query) || !string.IsNullOrEmpty(proxy.Fragment) || proxy.AbsolutePath != "/")
            throw new InvalidDataException(error);
        string authority = raw[(raw.IndexOf("://", StringComparison.Ordinal) + 3)..].TrimEnd('/');
        int separator = authority.LastIndexOf(':');
        if (separator <= 0) throw new InvalidDataException(error);
        string portText = authority[(separator + 1)..];
        foreach (char character in portText) if (character < '0' || character > '9') throw new InvalidDataException(error);
        if (!int.TryParse(portText, System.Globalization.NumberStyles.None, System.Globalization.CultureInfo.InvariantCulture,
            out int port) || port < 1 || port > 65535) throw new InvalidDataException(error);
        string host = proxy.IdnHost.Trim('[', ']');
        if (proxy.HostNameType == UriHostNameType.IPv6) host = "[" + host + "]";
        return "http://" + host + ":" + port.ToString(System.Globalization.CultureInfo.InvariantCulture);
    }

    private static JObject ParseObject(string json, string description)
    {
        using var input = new StringReader(json);
        using var reader = new JsonTextReader(input)
        {
            DateParseHandling = DateParseHandling.None
        };
        JObject result;
        try
        {
            result = JObject.Load(reader, new JsonLoadSettings
            {
                DuplicatePropertyNameHandling = DuplicatePropertyNameHandling.Error
            });
        }
        catch (JsonException ex)
        {
            throw new InvalidDataException("Invalid " + description + " JSON", ex);
        }
        if (reader.Read())
        {
            throw new InvalidDataException(description + " contains trailing JSON");
        }
        return result;
    }

    private static void RequireOnly(JObject value, params string[] names)
    {
        foreach (JProperty property in value.Properties())
        {
            if (Array.IndexOf(names, property.Name) < 0)
            {
                throw new InvalidDataException("unknown JSON field " + property.Name);
            }
        }
    }

    private static string RequiredString(JObject value, string name)
    {
        JToken token = value[name];
        if (token == null || token.Type != JTokenType.String || string.IsNullOrEmpty((string)token))
        {
            throw new InvalidDataException(name + " must be a non-empty string");
        }
        return (string)token;
    }

    private static int RequiredInteger(JObject value, string name)
    {
        JToken token = value[name];
        if (token == null || token.Type != JTokenType.Integer)
        {
            throw new InvalidDataException(name + " must be an integer");
        }
        return (int)token;
    }

    private static string OptionalString(JObject value, string name)
    {
        JToken token = value[name];
        if (token == null || token.Type == JTokenType.Null)
        {
            return string.Empty;
        }
        if (token.Type != JTokenType.String)
        {
            throw new InvalidDataException(name + " must be a string when present");
        }
        return (string)token;
    }

}

internal sealed class ClientRuntime
{
    internal Dictionary<string, string> plugin_files = new(StringComparer.Ordinal);
    internal string client_version = string.Empty;
    internal string update_fingerprint = string.Empty;
    internal string resource_fingerprint = string.Empty;
    internal string game_version = string.Empty;
    internal string bundle_version = string.Empty;
    internal string game_data_version = string.Empty;
}

internal sealed class ClientInstance(string id, string root)
{
    internal string ID { get; } = id;
    internal string Root { get; } = root;
}
