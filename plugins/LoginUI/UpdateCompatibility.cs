using System;
using Bd2.Client;
using System.Collections;
using System.Collections.Generic;
using System.IO;
using System.Runtime.Serialization;
using System.Runtime.Serialization.Json;
using System.Text;
using System.Threading;
using BepInEx.Bootstrap;
using TextComponent = BD2.GameSdk.Component;
using UnityEngine;
using UnityEngine.Networking;

using static Bd2LoginUI.ControlRequests;
using static Bd2LoginUI.LoginRuntime;
using static Bd2LoginUI.SessionRecovery;

namespace Bd2LoginUI;

internal static class UpdateCompatibility
{
    private static readonly string Fingerprint = ClientSettings.Current.runtime.update_fingerprint;
    private static readonly string ResourceFingerprint = ClientSettings.Current.runtime.resource_fingerprint;
    private static readonly string ClientVersion = ClientSettings.Current.ClientVersion;
    private static readonly string BundleVersion = ClientSettings.Current.BundleVersion;
    private static readonly string GameDataVersion = ClientSettings.Current.GameDataVersion;
    private static IReadOnlyDictionary<string, string> headers;
    private static Dictionary<string, string> plugins;
    private static int blocked;
    private static int popupShown;
    private static bool verificationFailed;
    internal static bool ContinuingLogin;
    internal static bool LoginCheckInProgress;
    internal static bool IsBlocked => Volatile.Read(ref blocked) != 0;

    internal static IReadOnlyDictionary<string, string> Headers
    {
        get
        {
            if (headers != null) return headers;
            plugins = new Dictionary<string, string>(StringComparer.Ordinal);
            foreach (KeyValuePair<string, string> entry in ClientSettings.Current.runtime.plugin_files)
            {
                string expected = Path.GetFullPath(Path.Combine(BepInEx.Paths.PluginPath, entry.Value));
                foreach (BepInEx.PluginInfo info in Chainloader.PluginInfos.Values)
                {
                    if (info.Instance == null || !string.Equals(info.Instance.GetType().Assembly.Location, expected,
                        Path.DirectorySeparatorChar == '\\' ? StringComparison.OrdinalIgnoreCase : StringComparison.Ordinal)) continue;
                    if (plugins.ContainsKey(entry.Key))
                        throw new InvalidDataException("Multiple loaded plugins use the managed DLL " + entry.Value);
                    plugins.Add(entry.Key, info.Metadata.Version.ToString());
                }
            }
            using var stream = new MemoryStream();
            Serializer(typeof(Dictionary<string, string>)).WriteObject(stream, plugins);
            headers = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase)
            {
                ["X-BD2-Client-Version"] = ClientVersion,
                ["X-BD2-Plugins"] = Encoding.UTF8.GetString(stream.ToArray()),
                ["X-BD2-Game-Version"] = Application.version,
                ["X-BD2-Bundle-Version"] = BundleVersion,
                ["X-BD2-Game-Data-Version"] = GameDataVersion,
                ["X-BD2-Update-Fingerprint"] = Fingerprint,
                ["X-BD2-Resource-Fingerprint"] = ResourceFingerprint
            };
            return headers;
        }
    }

    internal static IEnumerator CheckLogin(object introUI, bool automatic)
    {
        LoginCheckInProgress = true;
        try
        {
            bool compatible = false;
            var intro = (MonoBehaviour)introUI;
            AuthenticationRequestLifetime owner = intro.GetComponent<AuthenticationRequestLifetime>() ??
                intro.gameObject.AddComponent<AuthenticationRequestLifetime>();
            yield return Check(allowed => compatible = allowed, owner.Lifetime.Token);
            if (!compatible || IsBlocked || intro == null) yield break;
            ContinuingLogin = true;
            try { SendMaintenance.Invoke(introUI, [automatic]); }
            finally { ContinuingLogin = false; }
        }
        finally { LoginCheckInProgress = false; }
    }

    internal static IEnumerator Check(Action<bool> completed, CancellationToken lifetime)
    {
        if (IsBlocked) { completed(false); yield break; }
        int generation = Volatile.Read(ref RecoveryGeneration);
        Uri origin = ServerRoot;
        for (int attempt = 0; attempt < 3; attempt++)
        {
            ControlProbeResult result = null;
            yield return RequestControlEndpoint(new Uri(origin, "client/compatibility"), UnityWebRequest.kHttpVerbGET,
                null, null, 6, response => result = response, lifetime, ControlProbeCancellation.Token);
            if (lifetime.IsCancellationRequested || generation != Volatile.Read(ref RecoveryGeneration) ||
                ServerRoot == null || !SameOrigin(origin, ServerRoot) || IsBlocked) yield break;
            if (result == null) yield break;
            if (result.UpdateRequired)
            {
                Log?.LogWarning($"Client compatibility check rejected by server: request_id={result.RequestId} status={result.StatusCode}");
                ReportRequired(); completed(false); yield break;
            }
            if (result.Success)
            {
                bool? compatible = null;
                try
                {
                    using var stream = new MemoryStream(Encoding.UTF8.GetBytes(result.Body));
                    Compatibility policy = (Compatibility)Serializer(typeof(Compatibility)).ReadObject(stream) ??
                        throw new InvalidDataException("Missing compatibility policy");
                    if (string.IsNullOrWhiteSpace(policy.GameVersion)) throw new InvalidDataException("Missing game_version");
                    if (policy.MinimumPlugins == null) throw new InvalidDataException("Missing minimum_plugins");
                    compatible = Matches(policy);
                }
                catch (Exception ex) when (ex is SerializationException || ex is InvalidDataException || ex is ArgumentException)
                {
                    string detail = ex is InvalidDataException ? DiagnosticValue(ex.Message) : ex.GetType().Name;
                    Log?.LogWarning($"Client compatibility check failed: phase=decode attempt={attempt + 1}/3 request_id={result.RequestId} status={result.StatusCode} response_bytes={result.ResponseBytes} reason={detail}");
                }
                finally { result.Body = null; }
                if (compatible == null) break;
                if (!compatible.Value) ReportRequired();
                completed(compatible.Value);
                yield break;
            }
            bool transient = result.StatusCode == 0 || result.StatusCode == 429 || result.StatusCode >= 500;
            Log?.LogWarning($"Client compatibility check failed: phase=request attempt={attempt + 1}/3 request_id={result.RequestId} status={result.StatusCode} failure={result.FailureKind ?? "HttpStatus"} native_error={result.NativeErrorCode} elapsed_ms={result.ElapsedMilliseconds} response_bytes={result.ResponseBytes} error={DiagnosticValue(result.Error)} retry={transient && attempt < 2}");
            if (!transient || attempt == 2) break;
            yield return new WaitForSecondsRealtime(attempt + 1);
        }
        verificationFailed = true;
        ReportRequired();
        completed(false);
    }

    private static bool Matches(Compatibility policy)
    {
        _ = Headers;
        FingerprintPolicy update = policy.Fingerprint;
        if (update == null)
            return MatchesValue("game_version", headers["X-BD2-Game-Version"], policy.GameVersion) &
                MatchesResource("bundle_version", policy.BundleVersion, BundleVersion) & MatchesResource("game_data_version", policy.GameDataVersion, GameDataVersion) &
                MatchesPlugins(policy.MinimumPlugins);
        if (string.IsNullOrWhiteSpace(update.ID)) throw new InvalidDataException("Missing update fingerprint identity");
        bool compatible = true;
        if (update.RequireClientUpdate)
        {
            if (!AtLeast(ClientVersion, update.ClientVersion, "+client."))
            {
                Log?.LogWarning($"Client update required: launcher={DiagnosticValue(ClientVersion)}, minimum={DiagnosticValue(update.ClientVersion)}");
                compatible = false;
            }
            compatible &= MatchesPlugins(update.MinimumPlugins);
        }
        if (update.RequireGameUpdate)
        {
            if (update.Game == null || string.IsNullOrEmpty(update.Game.GameVersion) ||
                string.IsNullOrEmpty(update.Game.BundleVersion) || string.IsNullOrEmpty(update.Game.GameDataVersion) ||
                string.IsNullOrEmpty(update.GameFingerprint))
                throw new InvalidDataException("Missing required game resource versions");
            compatible &= MatchesValue("resource_fingerprint", ResourceFingerprint, update.GameFingerprint);
            compatible &= MatchesValue("game_version", headers["X-BD2-Game-Version"], update.Game.GameVersion);
            compatible &= MatchesValue("bundle_version", BundleVersion, update.Game.BundleVersion);
            compatible &= MatchesValue("game_data_version", GameDataVersion, update.Game.GameDataVersion);
        }
        return compatible;
    }

    private static bool MatchesResource(string name, string required, string actual) =>
        string.IsNullOrEmpty(required) || MatchesValue(name, actual, required);

    private static bool MatchesValue(string name, string actual, string required)
    {
        if (actual == required) return true;
        Log?.LogWarning($"Client update required: field={name} actual={DiagnosticValue(actual)} required={DiagnosticValue(required)}");
        return false;
    }

    private static string DiagnosticValue(string value) => string.IsNullOrEmpty(value) ? "missing" :
        value[..Math.Min(value.Length, 96)].Replace('\r', ' ').Replace('\n', ' ');

    private static bool MatchesPlugins(Dictionary<string, string> required)
    {
        if (required == null) return true;
        bool compatible = true;
        foreach (KeyValuePair<string, string> item in required)
        {
            // CaptureEnvironment is an official-client evidence tool, never a player plugin.
            if (item.Key == "capture_environment") continue;
            if (!plugins.TryGetValue(item.Key, out string current) || !AtLeast(current, item.Value, null))
            {
                Log?.LogWarning($"Client update required: plugin={DiagnosticValue(item.Key)}, loaded={DiagnosticValue(current)}, minimum={DiagnosticValue(item.Value)}");
                compatible = false;
            }
        }
        return compatible;
    }

    private static bool AtLeast(string current, string required, string component)
    {
        if (string.IsNullOrEmpty(current) || string.IsNullOrEmpty(required)) return false;
        if (component != null)
        {
            int a = current.IndexOf(component, StringComparison.Ordinal), b = required.IndexOf(component, StringComparison.Ordinal);
            if (a <= 0 || b <= 0 || !Version.TryParse(current[..a], out Version game) ||
                !Version.TryParse(required[..b], out Version minimumGame)) return false;
            int comparison = game.CompareTo(minimumGame);
            if (comparison != 0) return comparison > 0;
            current = current[(a + component.Length)..];
            required = required[(b + component.Length)..];
        }
        return Version.TryParse(current, out Version value) && Version.TryParse(required, out Version minimum) && value >= minimum;
    }

    internal static void Observe(UnityWebRequest request)
    {
        if (request.isDone && request.GetResponseHeader("X-BD2-Update-Required") == "1") ReportRequired();
    }

    internal static bool Observe(PlatformControlHttp.Response response)
    {
        bool required = response.Headers != null && response.Headers.TryGetValue("X-BD2-Update-Required", out string value) && value == "1";
        if (required) ReportRequired();
        return required;
    }

    internal static void ReportRequired() => Interlocked.Exchange(ref blocked, 1);

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0031", Justification = "UnityEngine.Object null checks also detect destroyed native objects.")]
    internal static void ShowPendingPopup()
    {
        if (!IsBlocked || ApplicationQuitting || UIManager.IsShowingNetworkErrorPopup ||
            Interlocked.CompareExchange(ref popupShown, 1, 0) != 0) return;
        FinishRecovery(false, verificationFailed ? "client compatibility could not be verified" : "server requires a client update");
        LoginController.LoginInProgress = false;
        LoginController.AccessTokens.Clear();
        EstablishedGameSession = false;
        IntroUI intro = FindUnitySingleton<IntroUI>();
        if (intro != null) CancelMaintenanceTimeout.Invoke(intro, null);
        BDNetwork.NetworkManager network = FindUnitySingleton<BDNetwork.NetworkManager>();
        if (network != null) network.Refresh();
        DisposeGameRelay();
        string key = verificationFailed ? "loginui.update.check_failed" : "loginui.update.required";
        UIManager.OpenErrorMessagePopupUI(TextComponent.Translatable(key).GetString(), LocalTextInfo.GetIntroText(-9998),
            TextComponent.Translatable("loginui.update.exit").GetString(), null, true, Application.Quit);
        UIManager.IsShowingNetworkErrorPopup = true;
    }

    private static DataContractJsonSerializer Serializer(Type type) =>
        new(type, new DataContractJsonSerializerSettings { UseSimpleDictionaryFormat = true });

    [DataContract]
    private sealed class Compatibility
    {
        [DataMember(Name = "game_version")] public string GameVersion { get; set; }
        [DataMember(Name = "bundle_version")] public string BundleVersion { get; set; }
        [DataMember(Name = "game_data_version")] public string GameDataVersion { get; set; }
        [DataMember(Name = "minimum_plugins")] public Dictionary<string, string> MinimumPlugins { get; set; }
        [DataMember(Name = "fingerprint")] public FingerprintPolicy Fingerprint { get; set; }
    }

    [DataContract]
    private sealed class FingerprintPolicy
    {
        [DataMember(Name = "id")] public string ID { get; set; }
        [DataMember(Name = "game_fingerprint")] public string GameFingerprint { get; set; }
        [DataMember(Name = "client_version")] public string ClientVersion { get; set; }
        [DataMember(Name = "require_client_update")] public bool RequireClientUpdate { get; set; }
        [DataMember(Name = "require_game_update")] public bool RequireGameUpdate { get; set; }
        [DataMember(Name = "minimum_plugins")] public Dictionary<string, string> MinimumPlugins { get; set; }
        [DataMember(Name = "game")] public GamePolicy Game { get; set; }
    }

    [DataContract]
    private sealed class GamePolicy
    {
        [DataMember(Name = "game_version")] public string GameVersion { get; set; }
        [DataMember(Name = "bundle_version")] public string BundleVersion { get; set; }
        [DataMember(Name = "game_data_version")] public string GameDataVersion { get; set; }
    }
}
