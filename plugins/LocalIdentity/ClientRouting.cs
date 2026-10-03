using System;
using System.Reflection;
using BD2.GameNames;
using static BD2.GameNames.Game;
using HarmonyLib;
using System.IO;
using System.Net;
using System.Text;
using BepInEx;
using BepInEx.Logging;
using Newtonsoft.Json;
using Newtonsoft.Json.Linq;

namespace Bd2LocalIdentity;

internal sealed class ClientRouting : IDisposable
{
    private static ClientRouting installedRouting;
    private static ManualLogSource routingLog;

    internal void Install(Harmony harmony)
    {
        installedRouting = this;
        routingLog = log;
        MethodInfo load = Game.Method(() => BDNetwork.ServerURLInfo.Load());
        MethodInfo sendMaintenance = Game.Method<IntroUI>(ui => ui.SendMaintenanceInfo(false));
        MethodInfo makeCDNInfo = Game.Method(() => BDNetwork.CommonPacket.MakeCDNInfo(null));
        if (load == null || sendMaintenance == null || makeCDNInfo == null)
        {
            throw new MissingMethodException("2.35.10 server/resource routing methods were not found");
        }
        harmony.Patch(load, postfix: new HarmonyMethod(typeof(ClientRouting), nameof(ApplyServerOriginHook)));
        harmony.Patch(sendMaintenance, prefix: new HarmonyMethod(typeof(ClientRouting), nameof(ApplyServerOriginHook)));
        harmony.Patch(makeCDNInfo, postfix: new HarmonyMethod(typeof(ClientRouting), nameof(ApplyResourcesHook)));
        routingLog?.LogInfo("Server origin and native CdnInfo routing patches installed");
    }

    private static void ApplyServerOriginHook()
    {
        ClientRouting routing = installedRouting;
        if (routing == null)
        {
            routingLog?.LogError("Client routing is unavailable while applying the server origin");
            return;
        }
        routing.ApplyServerOrigin();
    }

    private static void ApplyResourcesHook()
    {
        ClientRouting routing = installedRouting;
        if (routing == null)
        {
            routingLog?.LogError("Client routing is unavailable while applying resources");
            return;
        }
        routing.ApplyResources();
    }


    private const string ConfigFileName = "bd2.client.json";
    private const string OfficialServerData = "https://bd2-cdn.akamaized.net/ServerData";
    private const string OfficialGameData = "https://bd2-cdn.akamaized.net/GameData";
    private const int MaximumResponseBytes = 16 * 1024;

    private readonly ManualLogSource log;
    private readonly ClientConfig config;
    private readonly ResourcePolicy resources;
    private readonly LocalResourceServer localResourceServer;

    private ClientRouting(
        ManualLogSource log,
        ClientConfig config,
        ResourcePolicy resources,
        LocalResourceServer localResourceServer)
    {
        this.log = log;
        this.config = config;
        this.resources = resources;
        this.localResourceServer = localResourceServer;
    }

    internal static ClientRouting Load(ManualLogSource log)
    {
        string path = Path.Combine(Paths.ConfigPath, ConfigFileName);
        ClientConfig config = ParseConfig(ReadBoundedFile(path, MaximumResponseBytes));
        ValidateOrigin(config.server_origin, "server_origin");
        config.server_origin = config.server_origin.TrimEnd('/');

        ResourcePolicy resources;
        LocalResourceServer localResourceServer = null;
        try
        {
            if (config.cdn_mode == "official")
            {
                resources = new ResourcePolicy
                {
                    mode = "official",
                    server_data_url = OfficialServerData,
                    game_data_url = OfficialGameData,
                    bundle_version = Bd2Build.Versions.Bundle,
                    game_data_version = Bd2Build.Versions.GameData
                };
                log.LogInfo("Client resource mode official: direct official CDN with release-locked versions");
            }
            else if (config.cdn_mode == "local")
            {
                localResourceServer = LocalResourceServer.Start(
                    log,
                    config.local_resource_directory,
                    Bd2Build.Versions.Bundle,
                    Bd2Build.Versions.GameData);
                resources = new ResourcePolicy
                {
                    mode = "local",
                    server_data_url = localResourceServer.Origin + "/ServerData",
                    game_data_url = localResourceServer.Origin + "/GameData",
                    bundle_version = Bd2Build.Versions.Bundle,
                    game_data_version = Bd2Build.Versions.GameData
                };
                log.LogInfo("Client resource mode local: serving downloaded resources from " +
                    localResourceServer.RootDirectory);
            }
            else
            {
                resources = RequestResourcePolicy(log, config);
            }
            ValidatePolicy(config.cdn_mode, resources);
            return new ClientRouting(log, config, resources, localResourceServer);
        }
        catch
        {
            localResourceServer?.Dispose();
            throw;
        }
    }

    public void Dispose()
    {
        if (ReferenceEquals(installedRouting, this)) installedRouting = null;
        localResourceServer?.Dispose();
    }

    internal void ApplyServerOrigin()
    {
        Uri gameEndpoint = new Uri(new Uri(config.server_origin + "/"), "game/");
        BDNetwork.ServerURLInfo.SetUriDirectly(gameEndpoint);
        log.LogInfo("Client server origin applied: " + config.server_origin);
    }

    internal void ApplyResources()
    {
        BDNetwork.CdnInfo.Info = resources.server_data_url;
        BDNetwork.CdnInfo.Version = resources.bundle_version;
        BDNetwork.CdnInfo.SoundVersion = resources.bundle_version;
        BDNetwork.CdnInfo.GameDataInfo = resources.game_data_url;
        BDNetwork.CdnInfo.GameDataVersion = resources.game_data_version;
        log.LogInfo("Client resources applied: mode=" + resources.mode +
            " server_data=" + resources.server_data_url +
            " game_data=" + resources.game_data_url +
            " bundle=" + resources.bundle_version +
            " game_data_version=" + resources.game_data_version);
    }

    private static ResourcePolicy RequestResourcePolicy(ManualLogSource log, ClientConfig config)
    {
        Uri endpoint = new Uri(new Uri(config.server_origin + "/"), "client/resources");
        byte[] body = Encoding.UTF8.GetBytes("{\"cdn_mode\":\"" + config.cdn_mode + "\"}");
        HttpWebRequest request = (HttpWebRequest)WebRequest.Create(endpoint);
        request.Method = "PUT";
        request.ContentType = "application/json";
        request.Accept = "application/json";
        request.ContentLength = body.Length;
        request.AllowAutoRedirect = false;
        request.Timeout = 10_000;
        request.ReadWriteTimeout = 10_000;
        if (endpoint.IsLoopback)
        {
            // UnityWebRequest can inherit a system proxy even for loopback.
            // The configured private server must be contacted directly.
            request.Proxy = null;
        }

        log.LogInfo("Requesting client resource policy: origin=" + config.server_origin +
            " endpoint=" + endpoint.AbsoluteUri + " mode=" + config.cdn_mode + " method=PUT");
        try
        {
            using (Stream stream = request.GetRequestStream())
            {
                stream.Write(body, 0, body.Length);
            }
            using HttpWebResponse response = (HttpWebResponse)request.GetResponse();
            log.LogInfo("Client resource policy response: origin=" + config.server_origin +
                " status=" + (int)response.StatusCode);
            if (response.StatusCode != HttpStatusCode.OK)
            {
                throw new InvalidDataException("resource policy returned HTTP " + (int)response.StatusCode);
            }
            using Stream responseStream = response.GetResponseStream();
            return ParsePolicy(ReadBoundedStream(responseStream, MaximumResponseBytes));
        }
        catch (WebException ex)
        {
            int status = ex.Response is HttpWebResponse response ? (int)response.StatusCode : 0;
            ex.Response?.Close();
            log.LogError("Client resource policy failed: origin=" + config.server_origin +
                " status=" + status + " network_status=" + ex.Status);
            throw new InvalidOperationException(
                "Could not load " + config.cdn_mode + " resource policy from " + endpoint.AbsoluteUri,
                ex);
        }
    }

    private static ClientConfig ParseConfig(string json)
    {
        JObject value = ParseObject(json, "client configuration");
        RequireOnly(value, "schema_version", "server_origin", "cdn_mode", "local_resource_directory");
        ClientConfig config = new ClientConfig
        {
            schema_version = RequiredInteger(value, "schema_version"),
            server_origin = RequiredString(value, "server_origin"),
            cdn_mode = RequiredString(value, "cdn_mode"),
            local_resource_directory = OptionalString(value, "local_resource_directory")
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
        return config;
    }

    private static ResourcePolicy ParsePolicy(string json)
    {
        JObject value = ParseObject(json, "resource policy");
        RequireOnly(value, "mode", "server_data_url", "game_data_url", "bundle_version", "game_data_version");
        return new ResourcePolicy
        {
            mode = RequiredString(value, "mode"),
            server_data_url = RequiredString(value, "server_data_url"),
            game_data_url = RequiredString(value, "game_data_url"),
            bundle_version = RequiredString(value, "bundle_version"),
            game_data_version = RequiredString(value, "game_data_version")
        };
    }

    private static JObject ParseObject(string json, string description)
    {
        using StringReader input = new StringReader(json);
        using JsonTextReader reader = new JsonTextReader(input)
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

    private static void ValidatePolicy(string requestedMode, ResourcePolicy policy)
    {
        if (policy.mode != requestedMode)
        {
            throw new InvalidDataException("resource policy mode " + policy.mode + " does not match requested mode " + requestedMode);
        }
        ValidateResourceURL(policy.server_data_url, "server_data_url");
        ValidateResourceURL(policy.game_data_url, "game_data_url");
        if (policy.bundle_version != Bd2Build.Versions.Bundle)
        {
            throw new InvalidDataException("resource policy bundle_version does not match this client release");
        }
        if (policy.game_data_version != Bd2Build.Versions.GameData)
        {
            throw new InvalidDataException("resource policy game_data_version does not match this client release");
        }
        policy.server_data_url = policy.server_data_url.TrimEnd('/');
        policy.game_data_url = policy.game_data_url.TrimEnd('/');
    }

    private static void ValidateOrigin(string raw, string name)
    {
        if (!Uri.TryCreate(raw, UriKind.Absolute, out Uri uri) ||
            !string.IsNullOrEmpty(uri.UserInfo) || uri.AbsolutePath != "/" ||
            !string.IsNullOrEmpty(uri.Query) || !string.IsNullOrEmpty(uri.Fragment) ||
            (uri.Scheme != Uri.UriSchemeHttps && !(uri.Scheme == Uri.UriSchemeHttp && uri.IsLoopback)))
        {
            throw new InvalidDataException(name + " must be an HTTPS origin, except that loopback may use HTTP");
        }
    }

    private static void ValidateResourceURL(string raw, string name)
    {
        if (!Uri.TryCreate(raw, UriKind.Absolute, out Uri uri) ||
            !string.IsNullOrEmpty(uri.UserInfo) || !string.IsNullOrEmpty(uri.Query) || !string.IsNullOrEmpty(uri.Fragment) ||
            (uri.Scheme != Uri.UriSchemeHttps && !(uri.Scheme == Uri.UriSchemeHttp && uri.IsLoopback)))
        {
            throw new InvalidDataException(name + " must be HTTPS, except that loopback may use HTTP");
        }
    }

    private static string ReadBoundedFile(string path, int maximumBytes)
    {
        if (!File.Exists(path))
        {
            throw new FileNotFoundException("Client routing configuration is missing", path);
        }
        using FileStream stream = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read);
        return ReadBoundedStream(stream, maximumBytes);
    }

    private static string ReadBoundedStream(Stream stream, int maximumBytes)
    {
        using MemoryStream output = new MemoryStream();
        byte[] buffer = new byte[4096];
        while (true)
        {
            int read = stream.Read(buffer, 0, Math.Min(buffer.Length, maximumBytes + 1 - (int)output.Length));
            if (read == 0)
            {
                break;
            }
            output.Write(buffer, 0, read);
            if (output.Length > maximumBytes)
            {
                throw new InvalidDataException("JSON document exceeds " + maximumBytes + " bytes");
            }
        }
        return new UTF8Encoding(false, true).GetString(output.ToArray());
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

    private sealed class ClientConfig
    {
        public int schema_version;
        public string server_origin;
        public string cdn_mode;
        public string local_resource_directory;
    }

    private sealed class ResourcePolicy
    {
        public string mode;
        public string server_data_url;
        public string game_data_url;
        public string bundle_version;
        public string game_data_version;
    }
}
