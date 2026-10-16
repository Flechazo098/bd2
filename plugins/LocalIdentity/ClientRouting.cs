using System;
using System.Reflection;
using BD2.GameNames;
using HarmonyLib;
using System.IO;
using System.Net;
using System.Text;
using Bd2.Client;
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
            throw new MissingMethodException("2.36.7 server/resource routing methods were not found");
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


    private const string OfficialServerData = "https://bd2-cdn.akamaized.net/ServerData";
    private const string OfficialGameData = "https://bd2-cdn.akamaized.net/GameData";
    private const int MaximumResponseBytes = 16 * 1024;
    private static string BundleVersion => ClientSettings.Current.BundleVersion;
    private static string GameDataVersion => ClientSettings.Current.GameDataVersion;
    internal static bool IsActive => installedRouting != null;

    private readonly ManualLogSource log;
    private readonly ClientSettings config;
    internal string ServerOrigin => config.server_origin;
    internal Uri ServerRoot => new(config.server_origin + "/");
    private readonly ResourcePolicy resources;
    private readonly LocalResourceServer localResourceServer;

    private ClientRouting(
        ManualLogSource log,
        ClientSettings config,
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
        ClientSettings config = ClientSettings.Current;
        ResourcePolicy resources;
        LocalResourceServer localResourceServer = null;
        string effectiveMode = config.cdn_mode;
        try
        {
            if (effectiveMode == "official")
            {
                resources = new ResourcePolicy
                {
                    mode = "official",
                    server_data_url = OfficialServerData,
                    game_data_url = OfficialGameData,
                    bundle_version = BundleVersion,
                    game_data_version = GameDataVersion
                };
                log.LogInfo("Client resource mode official: direct official CDN with release-locked versions");
            }
            else if (effectiveMode == "local")
            {
                localResourceServer = LocalResourceServer.Start(
                    log,
                    config.local_resource_directory,
                    BundleVersion,
                    GameDataVersion);
                resources = new ResourcePolicy
                {
                    mode = "local",
                    server_data_url = localResourceServer.Origin + "/ServerData",
                    game_data_url = localResourceServer.Origin + "/GameData",
                    bundle_version = BundleVersion,
                    game_data_version = GameDataVersion
                };
                log.LogInfo("Client resource mode local: serving downloaded resources from " +
                    localResourceServer.RootDirectory);
            }
            else
            {
                resources = RequestResourcePolicy(log, config);
            }
            ValidatePolicy(effectiveMode, resources);
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
        var gameEndpoint = new Uri(new Uri(config.server_origin + "/"), "game/");
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

    private static ResourcePolicy RequestResourcePolicy(ManualLogSource log, ClientSettings config)
    {
        var endpoint = new Uri(new Uri(config.server_origin + "/"), "client/resources");
        byte[] body = Encoding.UTF8.GetBytes("{\"cdn_mode\":\"" + config.cdn_mode + "\"}");
        var request = (HttpWebRequest)WebRequest.Create(endpoint);
        request.Method = "PUT";
        request.ContentType = "application/json";
        request.Accept = "application/json";
        request.ContentLength = body.Length;
        request.AllowAutoRedirect = false;
        request.Timeout = 10_000;
        request.ReadWriteTimeout = 10_000;
        request.Proxy = endpoint.IsLoopback || string.IsNullOrEmpty(config.proxy_url)
            ? null : new WebProxy(config.proxy_url);

        log.LogInfo("Requesting client resource policy: origin=" + config.server_origin +
            " endpoint=" + endpoint.AbsoluteUri + " mode=" + config.cdn_mode + " method=PUT");
        try
        {
            using (Stream stream = request.GetRequestStream())
            {
                stream.Write(body, 0, body.Length);
            }
            using var response = (HttpWebResponse)request.GetResponse();
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

    private static void ValidatePolicy(string requestedMode, ResourcePolicy policy)
    {
        if (policy.mode != requestedMode)
        {
            throw new InvalidDataException("resource policy mode " + policy.mode + " does not match requested mode " + requestedMode);
        }
        ValidateResourceURL(policy.server_data_url, "server_data_url");
        ValidateResourceURL(policy.game_data_url, "game_data_url");
        if (policy.bundle_version != BundleVersion)
        {
            throw new InvalidDataException("resource policy bundle_version does not match this client release");
        }
        if (policy.game_data_version != GameDataVersion)
        {
            throw new InvalidDataException("resource policy game_data_version does not match this client release");
        }
        policy.server_data_url = policy.server_data_url.TrimEnd('/');
        policy.game_data_url = policy.game_data_url.TrimEnd('/');
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

    private static string ReadBoundedStream(Stream stream, int maximumBytes)
    {
        using var output = new MemoryStream();
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

    private sealed class ResourcePolicy
    {
        public string mode;
        public string server_data_url;
        public string game_data_url;
        public string bundle_version;
        public string game_data_version;
    }
}
