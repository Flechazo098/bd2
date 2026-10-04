using System;
using System.Collections;
using System.Collections.Generic;
using System.Reflection;
using System.Threading;
using BD2.GameNames;
using UnityEngine;
using UnityEngine.Networking;
using UnityEngine.UI;

using static BD2.GameNames.Game;
using static Bd2LoginUI.LoginPanel;
using static Bd2LoginUI.LoginRuntime;
using static Bd2LoginUI.ControlRequests;
using static Bd2LoginUI.LoginController;

namespace Bd2LoginUI;

// Owns recovery generations, monitor coroutines, game relay and recovery overlay.
internal static class SessionRecovery
{
    internal static int SessionRecoveryInProgress;
    private static RecoveryHost Owner;
    private static SecureGameRelay GameRelay;
    private static readonly List<WeakReference<UnityWebRequest>> GameRequests = new List<WeakReference<UnityWebRequest>>();
    internal static bool EstablishedGameSession;
    private static GameObject RecoveryOverlay;
    private static Text RecoveryText;
    internal static string ServerInstanceID;
    private static int RecoveryRestartScheduled;
    private static int RecoveryEnterScheduled;
    internal static int RecoveryGeneration;
    internal static int RuntimeProbeFailures;
    private static float LastGameSuccess;
    private static float LastGameTransportFailure;
    private static float RuntimeProbeFailureSince;
    private static bool RuntimeProbeDegraded;
    internal static CancellationTokenSource ControlProbeCancellation = new CancellationTokenSource();

    internal static void DisposeGameRelay()
    {
        SecureGameRelay relay = GameRelay;
        GameRelay = null;
        relay?.Dispose();
    }

    internal static void EnsureGameRelay()
    {
        if (GameRelay == null && ServerRoot != null && ServerRoot.Scheme == Uri.UriSchemeHttps && PlatformControlHttp.IsSupported)
        {
            GameRelay = SecureGameRelay.Start(ServerRoot, Log);
        }
    }

    private static bool TryGetOwnedRequestUri(string url, out Uri canonical)
    {
        canonical = null;
        if (!Uri.TryCreate(url, UriKind.Absolute, out Uri parsed)) return false;
        if (GameRelay != null && GameRelay.TryResolve(parsed, out Uri original)) parsed = original;
        if (ServerRoot == null || !SameOrigin(ServerRoot, parsed)) return false;
        canonical = parsed;
        return true;
    }

    private static void CancelControlProbes()
    {
        CancellationTokenSource previous = ControlProbeCancellation;
        ControlProbeCancellation = new CancellationTokenSource();
        previous.Cancel();
        previous.Dispose();
    }

    internal static void IntroAwakePostfix(object __instance)
    {
        try
        {
            EnsureRecoveryHost();
        }
        catch (Exception ex)
        {
            Log?.LogError("Could not restore login recovery host: " + ex);
        }
        ConfigureLoginPanel(__instance);
    }

    // The BepInEx plugin component can disappear while its static Harmony hooks remain.
    // Keep coroutines on a scene-independent host and recreate it if Unity destroys it.
    internal static void EnsureRecoveryHost()
    {
        if (Owner != null && Owner.gameObject.activeInHierarchy)
        {
            return;
        }
        if (Owner != null)
        {
            UnityEngine.Object.Destroy(Owner.gameObject);
        }
        GameObject host = new GameObject("BD2 Login Recovery Host");
        UnityEngine.Object.DontDestroyOnLoad(host);
        Owner = host.AddComponent<RecoveryHost>();
        Log?.LogInfo("Created persistent login recovery coroutine host");
        CancelControlProbes();
        Interlocked.Increment(ref RecoveryGeneration);
        Interlocked.Exchange(ref RecoveryRestartScheduled, 0);
        Interlocked.Exchange(ref RecoveryEnterScheduled, 0);
        Owner.StartCoroutine(RuntimeMonitor());
    }

    public sealed class RecoveryHost : MonoBehaviour
    {
        internal readonly CancellationTokenSource Lifetime = new CancellationTokenSource();
        private void OnDestroy()
        {
            Lifetime.Cancel();
            Lifetime.Dispose();
            Log?.LogWarning("Login recovery coroutine host was destroyed");
        }
    }

    internal static void SendWebRequestPrefix(UnityWebRequest __instance)
    {
        if (__instance == null || !TryGetOwnedRequestUri(__instance.url, out Uri canonical)) return;
        bool gameRequest = canonical.AbsolutePath.StartsWith("/game/", StringComparison.Ordinal);
        bool telemetryRequest = canonical.AbsolutePath.Equals("/logs", StringComparison.Ordinal);
        if (!gameRequest && !telemetryRequest) return;
        EnsureGameRelay();
        if (gameRequest)
        {
            GameRequests.RemoveAll(reference => !reference.TryGetTarget(out _));
            if (GameRequests.Count >= 128) GameRequests.RemoveAt(0);
            GameRequests.Add(new WeakReference<UnityWebRequest>(__instance));
        }
        if (GameRelay != null)
        {
            __instance.timeout = 30;
            __instance.url = GameRelay.Rewrite(canonical).AbsoluteUri;
        }
    }

    internal static void DisposeWebRequestPrefix(UnityWebRequest __instance)
    {
        if (__instance == null || !__instance.isDone)
        {
            return;
        }
        GameRequests.RemoveAll(reference => !reference.TryGetTarget(out UnityWebRequest request) || ReferenceEquals(request, __instance));
        InspectCompletedGameRequest(__instance);
    }

    private static void InspectCompletedGameRequest(UnityWebRequest request)
    {
        try
        {
            if (!IsCurrentGameRequest(request, out Uri requestUri))
            {
                return;
            }
            if (request.result == UnityWebRequest.Result.Success && request.responseCode >= 200 && request.responseCode < 300)
                LastGameSuccess = Time.realtimeSinceStartup;
            else if (IsGameTransportFailure(request))
                LastGameTransportFailure = Time.realtimeSinceStartup;
            if (requestUri.AbsolutePath.Equals("/game/LoginUser", StringComparison.Ordinal) &&
                request.responseCode >= 200 && request.responseCode < 300)
            {
                EnsureRecoveryHost();
                EstablishedGameSession = true;
                SetRecoveryMessage("正在同步玩家数据……\nSynchronizing player data…");
                return;
            }
            bool accessExpired = requestUri.AbsolutePath.Equals("/game/LoginUser", StringComparison.Ordinal) &&
                request.responseCode == 401 &&
                string.Equals(request.GetResponseHeader("X-BD2-Access-Expired"), "1", StringComparison.Ordinal);
            if (accessExpired)
            {
                AccessTokens.Clear();
                BeginSessionRecovery("expired game access credential");
                return;
            }
            bool sessionExpired = request.responseCode == 401 &&
                string.Equals(request.GetResponseHeader("X-BD2-Session-Expired"), "1", StringComparison.Ordinal);
            bool serverRestarting = request.responseCode == 503 &&
                string.Equals(request.GetResponseHeader("X-BD2-Reconnect"), "1", StringComparison.Ordinal);
            if (!sessionExpired && !serverRestarting)
            {
                return;
            }
            BeginSessionRecovery(serverRestarting ? "server restart" : "expired game session");
        }
        catch (Exception ex)
        {
            Log?.LogError("Could not inspect the completed game request: " + ex);
        }
    }

    private static bool IsCurrentGameRequest(UnityWebRequest request, out Uri requestUri)
    {
        requestUri = null;
        if (request == null || !TryGetOwnedRequestUri(request.url, out Uri canonical) ||
            !canonical.AbsolutePath.StartsWith("/game/", StringComparison.Ordinal)) return false;
        requestUri = canonical;
        return true;
    }

    internal static bool BeginSessionRecovery(string reason)
    {
        try
        {
            if (ServerRoot == null)
            {
                Log?.LogWarning("Cannot recover game session without a configured server");
                return false;
            }
            EnsureRecoveryHost();
            if (Interlocked.CompareExchange(ref SessionRecoveryInProgress, 1, 0) == 0)
            {
                Log?.LogWarning("Starting automatic game-session recovery: " + reason);
                ShowRecoveryOverlay("正在重新连接服务器……\nReconnecting to server…");
            }
            else
            {
                Log?.LogWarning("Restarting automatic game-session recovery: " + reason);
            }
            ScheduleRecoveryRestart();
            return true;
        }
        catch (Exception ex)
        {
            FinishRecovery(false, "could not start recovery: " + ex);
            return false;
        }
    }

    internal static bool ExponentialBackoffPrefix(object __0)
    {
        try
        {
            try
            {
                const BindingFlags diagnosticFlags = BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic;
                object packet = __0?.GetType().GetGameProperty("PacketData", diagnosticFlags)?.GetValue(__0, null);
                string path = packet?.GetType().GetGameProperty("SendPath", diagnosticFlags)?.GetValue(packet, null) as string;
                if (path == "MaintenanceInfo")
                {
                    string server = packet?.GetType().GetGameField("RequestServerURL", diagnosticFlags)?.GetValue(packet) as string;
                    string endpoint = Uri.TryCreate(server, UriKind.Absolute, out Uri parsed) ? parsed.GetLeftPart(UriPartial.Authority) + parsed.AbsolutePath : "invalid";
                    string message = __0?.GetType().GetGameProperty("Message", diagnosticFlags)?.GetValue(__0, null) as string ?? string.Empty;
                    string category = message.IndexOf("tim", StringComparison.OrdinalIgnoreCase) >= 0 ? "Timeout" :
                        message.IndexOf("cert", StringComparison.OrdinalIgnoreCase) >= 0 || message.IndexOf("TLS", StringComparison.OrdinalIgnoreCase) >= 0 || message.IndexOf("SSL", StringComparison.OrdinalIgnoreCase) >= 0 ? "TLS" : "Other";
                    Log?.LogDebug("Maintenance backoff: endpoint=" + endpoint + ", category=" + category + ", observedRequests=" + GameRequests.Count);
                    foreach (WeakReference<UnityWebRequest> reference in GameRequests)
                    {
                        if (!reference.TryGetTarget(out UnityWebRequest request) || !request.isDone) continue;
                        if (!TryGetOwnedRequestUri(request.url, out Uri original) || original.AbsolutePath != "/game/MaintenanceInfo") continue;
                        string error = request.error ?? string.Empty;
                        string safeError = error.IndexOf("Insecure", StringComparison.OrdinalIgnoreCase) >= 0 ? "InsecureConnectionBlocked" :
                            error.IndexOf("connect", StringComparison.OrdinalIgnoreCase) >= 0 ? "ConnectionFailure" :
                            error.IndexOf("tim", StringComparison.OrdinalIgnoreCase) >= 0 ? "Timeout" :
                            error.IndexOf("HTTP", StringComparison.OrdinalIgnoreCase) >= 0 ? "HttpFailure" :
                            error.IndexOf("SSL", StringComparison.OrdinalIgnoreCase) >= 0 || error.IndexOf("cert", StringComparison.OrdinalIgnoreCase) >= 0 ? "TlsFailure" : "Unclassified";
                        Log?.LogWarning("Maintenance completed transport: result=" + request.result + ", status=" + request.responseCode +
                            ", error=" + safeError + ", errorLength=" + error.Length);
                    }
                }

            }
            catch (Exception ex)
            {
                Log?.LogDebug("Maintenance transport diagnostic unavailable: " + ex.GetType().Name);
            }
            if (ServerRoot == null || !IsConfiguredServerFailure(__0) || !IsTransportFailure(__0))
            {
                return true;
            }
            const BindingFlags flags = BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic;
            object failedPacket = __0.GetType().GetGameProperty("PacketData", flags)?.GetValue(__0, null);
            object retryValue = failedPacket?.GetType().GetGameProperty("RetryCount", flags)?.GetValue(failedPacket, null);
            // Preserve the game's same-packet backoff for two transient failures;
            // never replay a mutation independently in the native transport.
            if (retryValue is int retries && retries < 2) return true;
            if (Volatile.Read(ref SessionRecoveryInProgress) != 0)
            {
                return !BeginSessionRecovery("transport failure during recovery after client retries");
            }
            return !EstablishedGameSession || !BeginSessionRecovery("transport failure after client retries");
        }
        catch (Exception ex)
        {
            Log?.LogError("Could not classify game request failure: " + ex);
            return true;
        }
    }

    private static bool IsTransportFailure(object packetException)
    {
        const BindingFlags flags = BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic;
        object packet = packetException.GetType().GetGameProperty("PacketData", flags)?.GetValue(packetException, null);
        string server = packet?.GetType().GetGameField("RequestServerURL", flags)?.GetValue(packet) as string;
        string path = packet?.GetType().GetGameProperty("SendPath", flags)?.GetValue(packet, null) as string;
        if (!Uri.TryCreate(server + path, UriKind.Absolute, out Uri packetUri))
        {
            return false;
        }
        UnityWebRequest match = null;
        foreach (WeakReference<UnityWebRequest> reference in GameRequests)
        {
            if (!reference.TryGetTarget(out UnityWebRequest request) || !request.isDone ||
                !TryGetOwnedRequestUri(request.url, out Uri requestUri) || requestUri != packetUri)
            {
                continue;
            }
            if (match != null)
            {
                return false; // Ambiguous concurrent requests must retain client error handling.
            }
            match = request;
        }
        if (match == null || !IsGameTransportFailure(match)) return false;
        LastGameTransportFailure = Time.realtimeSinceStartup;
        return true;
    }

    private static bool IsGameTransportFailure(UnityWebRequest request) =>
        request.result == UnityWebRequest.Result.ConnectionError ||
        (request.result == UnityWebRequest.Result.ProtocolError && request.responseCode == 502 &&
            GameRelay != null && GameRelay.TryResolve(new Uri(request.url), out _) &&
            string.Equals(request.GetResponseHeader("X-BD2-Transport-Failure"), "1", StringComparison.Ordinal));

    private static bool IsConfiguredServerFailure(object packetException)
    {
        if (packetException == null || ServerRoot == null)
        {
            return false;
        }
        const BindingFlags flags = BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic;
        Type exceptionType = packetException.GetType();
        string url = exceptionType.GetGameProperty("Url", flags)?.GetValue(packetException, null) as string;
        object packet = exceptionType.GetGameProperty("PacketData", flags)?.GetValue(packetException, null);
        string requestServer = packet?.GetType().GetGameField("RequestServerURL", flags)?.GetValue(packet) as string;
        foreach (string candidate in new[] { requestServer, url })
        {
            if (string.IsNullOrWhiteSpace(candidate))
            {
                continue;
            }
            if (!Uri.TryCreate(candidate, UriKind.Absolute, out Uri parsed))
            {
                parsed = new Uri(ServerRoot, candidate.TrimStart('/'));
            }
            if (SameOrigin(ServerRoot, parsed))
            {
                return true;
            }
        }
        return false;
    }

    private static void ScheduleRecoveryRestart()
    {
        CancelControlProbes();
        Interlocked.Increment(ref RecoveryGeneration);
        AuthenticationLoadingOrigin = null;
        LoginInProgress = false;
        ContinueMaintenance = false;
        EstablishedGameSession = false;
        RuntimeProbeFailures = 0;
        ServerInstanceID = null;
        Interlocked.Exchange(ref RecoveryEnterScheduled, 0);
        if (Interlocked.CompareExchange(ref RecoveryRestartScheduled, 1, 0) == 0)
        {
            Owner.StartCoroutine(StartLatestRecoveryGeneration());
        }
    }

    private static IEnumerator StartLatestRecoveryGeneration()
    {
        int generation;
        do
        {
            generation = Volatile.Read(ref RecoveryGeneration);
            yield return new WaitForSecondsRealtime(0.25f);
        }
        while (generation != Volatile.Read(ref RecoveryGeneration));
        Interlocked.Exchange(ref RecoveryRestartScheduled, 0);
        if (Volatile.Read(ref SessionRecoveryInProgress) != 0)
        {
            Owner.StartCoroutine(WaitForServerAndRestart(generation));
        }
    }

    private static IEnumerator WaitForServerAndRestart(int generation)
    {
        float delay = 0.5f;
        string lastError = null;
        CancellationToken lifetime = Owner.Lifetime.Token;
        while (Volatile.Read(ref SessionRecoveryInProgress) != 0 && generation == Volatile.Read(ref RecoveryGeneration))
        {
            SetRecoveryMessage("等待服务器启动……\nWaiting for server…");
            ControlProbeResult result = null;
            yield return RequestControlEndpoint(new Uri(ServerRoot, "readyz"), UnityWebRequest.kHttpVerbGET, null, null, 6,
                lifetime, ControlProbeCancellation.Token, response => result = response);
            if (result == null || generation != Volatile.Read(ref RecoveryGeneration) || lifetime.IsCancellationRequested) yield break;
            if (result.Success && result.StatusCode == 200) break;
            if (result.Error != lastError)
            {
                Log?.LogWarning("Server readiness probe failed: " + result.Error);
                lastError = result.Error;
            }
            yield return new WaitForSecondsRealtime(delay);
            delay = Math.Min(delay * 2f, 5f);
        }
        if (Volatile.Read(ref SessionRecoveryInProgress) == 0 || generation != Volatile.Read(ref RecoveryGeneration))
        {
            yield break;
        }
        Log?.LogInfo("Server became ready; restarting client to restore session");
        SetRecoveryMessage("正在恢复登录会话……\nRestoring session…");
        if (!RestartClientForRecovery())
        {
            FinishRecovery(false, "client restart methods are unavailable", generation);
        }
    }

    private static bool RestartClientForRecovery()
    {
        BDNetwork.NetworkManager network = FindUnitySingleton<BDNetwork.NetworkManager>();
        AppManager app = FindUnitySingleton<AppManager>();
        MethodInfo refresh = network?.GetType().GetGameMethod(nameof(BDNetwork.NetworkManager.Refresh), BindingFlags.Instance | BindingFlags.Public, null, Type.EmptyTypes, null);
        MethodInfo restart = app?.GetType().GetGameMethod(nameof(AppManager.AppReStart), BindingFlags.Instance | BindingFlags.Public, null, Type.EmptyTypes, null);
        if (refresh == null || restart == null)
        {
            return false;
        }
        AuthenticationLoadingOrigin = null;
        LoginInProgress = false;
        ContinueMaintenance = false;
        Interlocked.Exchange(ref RecoveryEnterScheduled, 0);
        ServerInstanceID = null;
        refresh.Invoke(network, null);
        restart.Invoke(app, null);
        return true;
    }

    private static IEnumerator RuntimeMonitor()
    {
        WaitForSecondsRealtime interval = new WaitForSecondsRealtime(5f);
        while (true)
        {
            if (ServerRoot == null || !EstablishedGameSession || Volatile.Read(ref SessionRecoveryInProgress) != 0)
            {
                yield return interval;
                continue;
            }
            int generation = Volatile.Read(ref RecoveryGeneration);
            CancellationToken lifetime = Owner.Lifetime.Token;
            {
                ControlProbeResult result = null;
                yield return RequestControlEndpoint(new Uri(ServerRoot, "client/runtime"), UnityWebRequest.kHttpVerbGET, null, null, 6,
                    lifetime, ControlProbeCancellation.Token, response => result = response);
                if (lifetime.IsCancellationRequested) yield break;
                if (result == null || generation != Volatile.Read(ref RecoveryGeneration) || Volatile.Read(ref SessionRecoveryInProgress) != 0) continue;
                RuntimeStatus status = null;
                if (result.Success && result.StatusCode == 200)
                {
                    try { status = JsonUtility.FromJson<RuntimeStatus>(result.Body); }
                    catch (Exception) { }
                }
                bool valid = status != null && !string.IsNullOrWhiteSpace(status.instance_id) &&
                    (status.status == "ready" || status.status == "draining");
                if (!valid)
                {
                    if (!IsUnavailableProbe(result))
                    {
                        // Malformed/unsupported control responses degrade monitoring;
                        // they do not prove the game session disappeared.
                        RuntimeProbeFailures = 0;
                        RuntimeProbeFailureSince = 0;
                        if (!RuntimeProbeDegraded)
                            Log?.LogWarning("Server runtime monitoring degraded: " + (result.Error ?? "InvalidRuntimeStatus"));
                        RuntimeProbeDegraded = true;
                        yield return interval;
                        continue;
                    }
                    float now = Time.realtimeSinceStartup;
                    if (RuntimeProbeFailures == 0) RuntimeProbeFailureSince = now;
                    RuntimeProbeFailures++;
                    Log?.LogWarning("Server runtime probe failed: " + (result.Error ?? "InvalidRuntimeStatus"));
                    // Control-path hiccups alone must not interrupt active gameplay.
                    // A quiet session still recovers after a sustained outage, so a
                    // dead server cannot leave the client idle forever.
                    bool recentSuccess = LastGameSuccess > 0 && now - LastGameSuccess < 30f;
                    bool recentFailure = LastGameTransportFailure > LastGameSuccess && now - LastGameTransportFailure < 60f;
                    if (RuntimeProbeFailures >= 3 && !recentSuccess)
                    {
                        if (recentFailure)
                            BeginSessionRecovery("runtime probes and game transport unavailable");
                        else if (now - RuntimeProbeFailureSince >= 90f && now - LastGameSuccess >= 90f)
                        {
                            ControlProbeResult readiness = null;
                            yield return RequestControlEndpoint(new Uri(ServerRoot, "readyz"), UnityWebRequest.kHttpVerbGET,
                                null, null, 6, lifetime, ControlProbeCancellation.Token, response => readiness = response);
                            if (lifetime.IsCancellationRequested) yield break;
                            if (generation != Volatile.Read(ref RecoveryGeneration)) continue;
                            if (readiness != null && IsUnavailableProbe(readiness) && Time.realtimeSinceStartup - LastGameSuccess >= 90f)
                                BeginSessionRecovery("runtime and readiness unavailable during idle session");
                        }
                    }
                }
                else
                {
                    RuntimeProbeFailures = 0;
                    RuntimeProbeFailureSince = 0;
                    RuntimeProbeDegraded = false;
                    bool changed = ServerInstanceID != null && ServerInstanceID != status.instance_id;
                    ServerInstanceID = status.instance_id;
                    if (changed || status.status == "draining")
                        BeginSessionRecovery(status.status == "draining" ? "server draining" : "server instance changed");
                }
            }
            yield return interval;
        }
    }

    private static bool IsUnavailableProbe(ControlProbeResult result) =>
        result.StatusCode == 0 || result.StatusCode == 502 || result.StatusCode == 503 || result.StatusCode == 504;

    internal static bool SuppressNetworkErrorDuringRecovery(object __0, object __1)
    {
        Log?.LogWarning("Client network error: type=" + __0 + ", code=" + __1 +
            ", recovering=" + (Volatile.Read(ref SessionRecoveryInProgress) != 0));
        return Volatile.Read(ref SessionRecoveryInProgress) == 0;
    }

    internal static void SetIntroStatePostfix(object __instance, object __0)
    {
        if (Volatile.Read(ref SessionRecoveryInProgress) == 0 || __0 == null || Convert.ToInt32(__0) != 9 || Owner == null)
        {
            return;
        }
        if (Interlocked.CompareExchange(ref RecoveryEnterScheduled, 1, 0) != 0)
        {
            return;
        }
        Owner.StartCoroutine(EnterAfterAuthoritativeLoad(__instance, Volatile.Read(ref RecoveryGeneration)));
    }

    private static IEnumerator EnterAfterAuthoritativeLoad(object introUI, int generation)
    {
        SetRecoveryMessage("正在返回安全场景……\nReturning to a safe scene…");
        yield return null;
        if (generation != Volatile.Read(ref RecoveryGeneration))
        {
            yield break;
        }
        try
        {
            EnterGame.Invoke(introUI, null);
        }
        catch (Exception ex)
        {
            BeginSessionRecovery("could not enter safe scene: " + ex.Message);
            yield break;
        }
        float deadline = Time.realtimeSinceStartup + 30f;
        bool safeSceneLoaded = false;
        string safeScene = null;
        while (Time.realtimeSinceStartup < deadline && generation == Volatile.Read(ref RecoveryGeneration))
        {
            bool ready;
            try
            {
                ready = TryGetRecoveredSafeScene(out safeScene);
            }
            catch (Exception ex)
            {
                FinishRecovery(false, "could not inspect recovered scene: " + ex.Message, generation);
                yield break;
            }
            if (ready)
            {
                safeSceneLoaded = true;
                break;
            }
            yield return new WaitForSecondsRealtime(0.25f);
        }
        if (!safeSceneLoaded)
        {
            if (generation == Volatile.Read(ref RecoveryGeneration))
            {
                // A local scene deadline is not evidence of an expired session.
                // Retain normal client error handling instead of relogging forever.
                FinishRecovery(false, "safe scene load timed out after authoritative player sync; restart the client manually", generation);
            }
            yield break;
        }
        Log?.LogInfo("Recovered synchronized player data in safe scene: " + safeScene);
        FinishRecovery(true, null, generation);
    }

    private static bool TryGetRecoveredSafeScene(out string scene)
    {
        scene = null;
        if (!EstablishedGameSession || IsRecoveryUIActive("LoadingUI") || IsRecoveryUIActive("IntroUI")) return false;
        const BindingFlags flags = BindingFlags.Instance | BindingFlags.Public;
        PackManager pack = FindUnitySingleton<PackManager>();
        PropertyInfo packLoading = pack?.GetType().GetGameProperty(nameof(PackManager.IsPackLoading), flags);
        if (!(packLoading?.GetValue(pack) is bool loading) || loading) return false;
        GameFieldManager field = FindUnitySingleton<GameFieldManager>();
        PropertyInfo battle = field?.GetType().GetGameProperty(nameof(GameFieldManager.IsBattlePlay), flags);
        if (!(battle?.GetValue(field) is bool inBattle) || inBattle) return false;

        // OpenMenuUICoroutine deliberately clears IsLoadedField and loads an
        // empty scene. EntryFlow plus the completed, active MenuUI is its ready
        // state; waiting for a loaded field would restart a healthy home menu.
        object flow = typeof(UIManager).GetGameProperty("EntryFlow", BindingFlags.Static | BindingFlags.Public)?.GetValue(null, null);
        PropertyInfo menuState = flow?.GetType().GetGameProperty("IsInMenuUI", flags);
        if (menuState?.GetValue(flow) is bool inMenu && inMenu && IsRecoveryUIReady("MenuUI"))
        {
            scene = "home menu";
            return true;
        }
        PropertyInfo loaded = field.GetType().GetGameProperty(nameof(GameFieldManager.IsLoadedField), flags);
        if (loaded?.GetValue(field) is bool ready && ready)
        {
            scene = "field";
            return true;
        }
        if (IsRecoveryUIReady("PackCollectionUI"))
        {
            scene = "pack collection";
            return true;
        }
        return false;
    }

    private static Component GetRecoveryUI(string name)
    {
        Type uiManager = typeof(UIManager);
        MethodInfo getUI = uiManager?.GetGameMethod(
            "GetUI",
            BindingFlags.Static | BindingFlags.Public,
            null,
            new[] { typeof(string) },
            null);
        return getUI?.Invoke(null, new object[] { name }) as Component;
    }

    private static bool IsRecoveryUIActive(string name)
    {
        Component ui = GetRecoveryUI(name);
        return ui != null && ui.gameObject != null && ui.gameObject.activeInHierarchy;
    }

    private static bool IsRecoveryUIReady(string name)
    {
        Component ui = GetRecoveryUI(name);
        if (ui == null || ui.gameObject == null || !ui.gameObject.activeInHierarchy) return false;
        PropertyInfo loaded = ui.GetType().GetGameProperty("IsLoadedUI", BindingFlags.Instance | BindingFlags.Public);
        return loaded?.GetValue(ui) is bool ready && ready;
    }

    private static void ShowRecoveryOverlay(string message)
    {
        if (RecoveryOverlay == null)
        {
            RecoveryOverlay = new GameObject(
                "BD2 Recovery Overlay",
                typeof(RectTransform), typeof(Canvas), typeof(CanvasScaler), typeof(GraphicRaycaster), typeof(Image));
            UnityEngine.Object.DontDestroyOnLoad(RecoveryOverlay);
            Canvas canvas = RecoveryOverlay.GetComponent<Canvas>();
            canvas.renderMode = RenderMode.ScreenSpaceOverlay;
            canvas.sortingOrder = short.MaxValue;
            Image background = RecoveryOverlay.GetComponent<Image>();
            background.color = new Color(0.025f, 0.035f, 0.055f, 0.94f);
            RectTransform root = RecoveryOverlay.GetComponent<RectTransform>();
            root.anchorMin = Vector2.zero;
            root.anchorMax = Vector2.one;
            root.offsetMin = root.offsetMax = Vector2.zero;
            GameObject label = new GameObject("Status", typeof(RectTransform), typeof(Text));
            label.transform.SetParent(RecoveryOverlay.transform, false);
            RecoveryText = label.GetComponent<Text>();
            RecoveryText.font = Resources.GetBuiltinResource<Font>("Arial.ttf");
            RecoveryText.fontSize = 28;
            RecoveryText.alignment = TextAnchor.MiddleCenter;
            RecoveryText.color = Color.white;
            RectTransform rect = label.GetComponent<RectTransform>();
            rect.anchorMin = new Vector2(0.15f, 0.35f);
            rect.anchorMax = new Vector2(0.85f, 0.65f);
            rect.offsetMin = rect.offsetMax = Vector2.zero;
        }
        RecoveryOverlay.SetActive(true);
        SetRecoveryMessage(message);
    }

    private static void SetRecoveryMessage(string message)
    {
        if (RecoveryText != null)
        {
            RecoveryText.text = message;
        }
    }

    internal static void FinishRecovery(bool success, string error, int expectedGeneration = 0)
    {
        if (expectedGeneration != 0 && expectedGeneration != Volatile.Read(ref RecoveryGeneration))
        {
            return;
        }
        if (!success)
        {
            Log?.LogError("Automatic session recovery failed: " + error);
        }
        if (RecoveryOverlay != null)
        {
            UnityEngine.Object.Destroy(RecoveryOverlay);
            RecoveryOverlay = null;
            RecoveryText = null;
        }
        CancelControlProbes();
        Interlocked.Increment(ref RecoveryGeneration);
        Interlocked.Exchange(ref SessionRecoveryInProgress, 0);
        Interlocked.Exchange(ref RecoveryRestartScheduled, 0);
        Interlocked.Exchange(ref RecoveryEnterScheduled, 0);
        Log?.LogInfo(success ? "Automatic session recovery completed" : "Automatic session recovery stopped");
    }

    [Serializable]
    private sealed class RuntimeStatus
    {
        public string status = null;
        public string instance_id = null;
        public int retry_after_ms = 0;
    }
}
