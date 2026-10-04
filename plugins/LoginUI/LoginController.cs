using System;
using System.Collections;
using System.IO;
using System.Reflection;
using System.Text;
using System.Threading;
using BD2.GameNames;
using Bd2Login;
using UnityEngine;
using UnityEngine.Networking;

using static BD2.GameNames.Game;
using static Bd2LoginUI.LoginPanel;
using static Bd2LoginUI.LoginRuntime;
using static Bd2LoginUI.ControlRequests;
using static Bd2LoginUI.SessionRecovery;

namespace Bd2LoginUI;

// Owns server authentication policy, browser/device login and refresh rotation state.
internal static class LoginController
{
    internal static void InitializeCredentials()
    {
        AccessTokens = new MemoryAccessTokenStore();
        RefreshCredentials = PlatformRefreshCredentialStore.Create();
    }

    private const string LocalAccessToken = "bd2-local-development-user";
    internal static ServerAuthentication Authentication;
    internal static string AuthenticationLoadingOrigin;
    internal static bool ContinueMaintenance;
    internal static bool LoginInProgress;
    internal static MemoryAccessTokenStore AccessTokens;
    internal static IRefreshCredentialStore RefreshCredentials;
    internal static void AccessTokenPostfix(ref string __result)
    {
        if (Authentication != null && Authentication.mode == "oauth")
        {
            __result = AccessTokens.Get();
            return;
        }
        __result = LocalAccessToken;
    }

    internal static void ClearPCLocalDataPostfix()
    {
        AccessTokens.Clear();
        EstablishedGameSession = false;
        RuntimeProbeFailures = 0;
        ServerInstanceID = null;
        DeleteCurrentRefresh();
    }

    internal static bool SendMaintenancePrefix(object __instance, bool __0)
    {
        if (!__0 || ContinueMaintenance)
        {
            return true;
        }
        try
        {
            if (!(__instance is Component))
            {
                throw new InvalidOperationException("IntroUI is not a Unity component");
            }
            Uri maintenance = CurrentMaintenanceUri();
            Uri currentRoot = new Uri(maintenance, "/");
            if (ServerRoot == null || !SameOrigin(ServerRoot, currentRoot))
            {
                AccessTokens.Clear();
                Authentication = null;
                LoginInProgress = false;
                EstablishedGameSession = false;
                RuntimeProbeFailures = 0;
                ServerInstanceID = null;
                DisposeGameRelay();
                ServerRoot = currentRoot;
                EnsureGameRelay();
            }
			if (Volatile.Read(ref SessionRecoveryInProgress) != 0 && Authentication != null &&
				Authentication.mode == "oauth" && AccessTokens.IsUsable(NormalizedServerOrigin()))
			{
				ContinueWithMaintenance(__instance, true);
				return false;
			}
            EnsureGameRelay();
            if (Authentication != null)
            {
                ApplyAuthenticationPolicy(__instance);
                return false;
            }
            string origin = NormalizedServerOrigin();
            if (AuthenticationLoadingOrigin == origin)
            {
                return false;
            }
            AuthenticationLoadingOrigin = origin;
            StartIntroCoroutine(__instance, LoadAuthenticationPolicy(__instance, currentRoot, origin));
            return false;
        }
        catch (Exception ex)
        {
            Log?.LogError("Could not request the server authentication policy: " + ex);
            return false;
        }
    }

    private static IEnumerator LoadAuthenticationPolicy(object introUI, Uri expectedRoot, string expectedOrigin)
    {
        Uri endpoint = new Uri(expectedRoot, "auth/config");
        int generation = Volatile.Read(ref RecoveryGeneration);
        for (int attempt = 0; attempt < 3; attempt++)
        {
            if (generation != Volatile.Read(ref RecoveryGeneration)) yield break;
            ControlProbeResult request = null;
            yield return AuthRequest(introUI, endpoint, UnityWebRequest.kHttpVerbGET, null, null, result => request = result);
            if (request == null || ServerRoot == null || !SameOrigin(ServerRoot, expectedRoot))
            {
                yield break;
            }
            if (!request.Success)
            {
                Log?.LogWarning("Authentication policy request failed: " + request.Error);
                bool transient = request.StatusCode == 0 || request.StatusCode == 429 || request.StatusCode >= 500;
                if (transient && attempt < 2)
                {
                    yield return new WaitForSecondsRealtime(attempt + 1);
                    continue;
                }
                if (AuthenticationLoadingOrigin == expectedOrigin) AuthenticationLoadingOrigin = null;
                if (Volatile.Read(ref SessionRecoveryInProgress) != 0)
                {
                    BeginSessionRecovery("authentication policy request failed");
                }
                yield break;
            }
            if (AuthenticationLoadingOrigin == expectedOrigin) AuthenticationLoadingOrigin = null;
            try
            {
                ServerAuthentication policy = JsonUtility.FromJson<ServerAuthentication>(request.Body);
                ValidateAuthentication(policy);
                Authentication = policy;
                Log?.LogInfo("Control HTTP /auth/config succeeded (native platform HTTP with certificate validation)");
                Log?.LogInfo("Server authentication mode: " + policy.mode);
                ApplyAuthenticationPolicy(introUI);
            }
            catch (Exception ex)
            {
                Log?.LogError("Server returned an invalid authentication policy: " + ex.GetType().Name);
                if (Volatile.Read(ref SessionRecoveryInProgress) != 0)
                {
                    BeginSessionRecovery("authentication policy response was invalid");
                }
            }
            finally { request.Body = null; }
            yield break;
        }
    }

    private static void ApplyAuthenticationPolicy(object introUI)
    {
        if (Authentication.mode == "local")
        {
            try
            {
                AccessTokens.Clear();
                ContinueMaintenance = true;
                SendMaintenance.Invoke(introUI, new object[] { true });
            }
            finally
            {
                ContinueMaintenance = false;
            }
            return;
        }
        ValidateOAuthTransport(ServerRoot);
        if (!RefreshCredentials.IsSupported)
        {
            ServerLoginPreferences.SetAutoLogin(NormalizedServerOrigin(), false);
            ServerLoginPreferences.SetUseAutoLoginPC(NormalizedServerOrigin(), false);
            Log?.LogWarning("Secure refresh credential storage is unavailable; automatic login is disabled on this platform");
        }
        if (LoginInProgress)
        {
            return;
        }
        if (Volatile.Read(ref SessionRecoveryInProgress) != 0)
        {
            if (AccessTokens.IsUsable(NormalizedServerOrigin()))
            {
                ContinueWithMaintenance(introUI, true);
                return;
            }
            if (RefreshCredentials.IsSupported)
            {
                LoginInProgress = true;
                StartIntroCoroutine(introUI, RefreshSession(introUI));
                return;
            }
            FinishRecovery(false, "no usable credential is available");
            ShowLoginPanel(introUI);
            return;
        }
        if (ServerLoginPreferences.IsAutoLogin(NormalizedServerOrigin()) &&
            ServerLoginPreferences.UseAutoLoginPC(NormalizedServerOrigin()) &&
            CanAttemptAutomaticLogin())
        {
            LoginInProgress = true;
            StartIntroCoroutine(introUI, RefreshSession(introUI));
        }
        else
        {
            ShowLoginPanel(introUI);
        }
    }

    private static void ShowLoginPanel(object introUI)
    {
        // The official manual-login branch cancels the 10-second startup watchdog.
        // Browser authentication waits for the user and must not retain that timer.
        CancelMaintenanceTimeout.Invoke(introUI, null);
        AccessTokens.Clear();
        LoginInProgress = false;
        EstablishedGameSession = false;
        ConfigureLoginPanel(introUI);
        Type stateType = SetIntroState.GetParameters()[0].ParameterType;
        SetIntroState.Invoke(introUI, new[] { Enum.ToObject(stateType, 1) });
        Log?.LogInfo("Waiting for server-authorized third-party authentication");
    }

    internal static void OpenLogin(object introUI, string provider)
    {
        try
        {
            if (LoginInProgress)
            {
                return;
            }
            PropertyInfo canInteraction = introUI?.GetType().GetGameProperty(
                "CanInteraction",
                BindingFlags.Instance | BindingFlags.Public);
            if (canInteraction != null && canInteraction.PropertyType == typeof(bool) &&
                !(bool)canInteraction.GetValue(introUI, null))
            {
                return;
            }

            if (!ProviderEnabled(provider))
            {
                throw new InvalidOperationException("Provider is not enabled by this server");
            }
            LoginInProgress = true;
            StartIntroCoroutine(introUI, DeviceLogin(introUI, provider));
        }
        catch (Exception ex)
        {
            Log?.LogError("Could not start " + provider + " authentication: " + ex.Message);
        }
    }

    private static IEnumerator DeviceLogin(object introUI, string provider)
    {
        int generation = Volatile.Read(ref RecoveryGeneration);
        Uri origin = ServerRoot;
        string endpoint = new Uri(origin, "auth/device").AbsoluteUri;
        byte[] body = Encoding.UTF8.GetBytes(JsonUtility.ToJson(new DeviceRequest { provider = provider }));
        {
            ControlProbeResult request = null;
            yield return AuthRequest(introUI, new Uri(endpoint), UnityWebRequest.kHttpVerbPOST, body, null, result => request = result);
            if (request == null) yield break;
            if (!IsCurrentLogin(generation, origin)) { request.Body = null; yield break; }
            if (!request.Success)
            {
                LoginInProgress = false;
                Log?.LogError("Could not create login transaction: " + request.Error);
                yield break;
            }
            DeviceStart start;
            try
            {
                start = JsonUtility.FromJson<DeviceStart>(request.Body);
                request.Body = null;
                if (start == null || string.IsNullOrEmpty(start.transaction_id) || string.IsNullOrEmpty(start.device_secret) || string.IsNullOrEmpty(start.start_url))
                {
                    throw new InvalidDataException("incomplete transaction response");
                }
                ValidateBrowserURL(start.start_url);
            }
            catch (Exception ex)
            {
                LoginInProgress = false;
                Log?.LogError("Invalid login transaction: " + ex.GetType().Name);
                yield break;
            }
            Application.OpenURL(start.start_url);
            yield return PollDevice(introUI, start, generation, origin);
        }
    }

    private static IEnumerator PollDevice(object introUI, DeviceStart start, int generation, Uri origin)
    {
        int delay = Math.Max(1, start.poll_interval);
        float deadline = Time.realtimeSinceStartup + Math.Max(30, start.expires_in);
        string endpoint = new Uri(origin, "auth/device/" + Uri.EscapeDataString(start.transaction_id) + "/poll").AbsoluteUri;
        while (Time.realtimeSinceStartup < deadline)
        {
            yield return new WaitForSecondsRealtime(delay);
            if (!IsCurrentLogin(generation, origin)) { start.device_secret = null; yield break; }
            {
                ControlProbeResult request = null;
                yield return AuthRequest(introUI, new Uri(endpoint), UnityWebRequest.kHttpVerbPOST, Array.Empty<byte>(), "Device " + start.device_secret, result => request = result);
                if (request == null) yield break;
                if (!IsCurrentLogin(generation, origin)) { request.Body = null; start.device_secret = null; yield break; }
                if (request.StatusCode == 202)
                {
                    continue;
                }
                if (!request.Success)
                {
                    LoginInProgress = false;
                    Log?.LogError("Login transaction failed: " + request.Error);
                    yield break;
                }
                TokenResult result;
                try
                {
                    result = JsonUtility.FromJson<TokenResult>(request.Body);
                    request.Body = null;
                }
                catch (Exception ex)
                {
                    LoginInProgress = false;
                    Log?.LogError("Login transaction returned invalid credentials: " + ex.GetType().Name);
                    yield break;
                }
                if (!ValidTokenResult(result) || !ProviderEnabled(result.provider))
                {
                    LoginInProgress = false;
                    Log?.LogError("Login transaction returned incomplete credentials");
                    yield break;
                }
                CompleteInteractiveLogin(introUI, result, generation, origin);
                yield break;
            }
        }
        LoginInProgress = false;
        Log?.LogError("Login transaction expired");
    }

    private static bool IsCurrentLogin(int generation, Uri origin) =>
        generation == Volatile.Read(ref RecoveryGeneration) && origin != null && ServerRoot != null && SameOrigin(origin, ServerRoot);

    private static void CompleteInteractiveLogin(object introUI, TokenResult result, int generation, Uri origin)
    {
        if (!IsCurrentLogin(generation, origin))
        {
            result.access_token = null;
            result.refresh_token = null;
            return;
        }
        if (!RefreshCredentials.IsSupported)
        {
            AccessTokens.Set(result.access_token, NormalizedServerOrigin(), result.provider, result.access_expires_in);
            result.access_token = null;
            result.refresh_token = null;
            ServerLoginPreferences.SetAutoLogin(NormalizedServerOrigin(), false);
            ServerLoginPreferences.SetUseAutoLoginPC(NormalizedServerOrigin(), false);
            Log?.LogWarning("Login succeeded, but automatic login remains disabled because this platform has no supported secure credential store");
            ContinueWithMaintenance(introUI, false);
            return;
        }
        Action confirmed = delegate
        {
            if (!IsCurrentLogin(generation, origin))
            {
                result.access_token = null;
                result.refresh_token = null;
                return;
            }
            try
            {
                bool autoLogin = ServerLoginPreferences.UseAutoLoginPC(NormalizedServerOrigin());
                if (autoLogin)
                {
                    StoreRefresh(result);
                }
                else
                {
                    DeleteCurrentRefresh();
                    result.refresh_token = null;
                }
                AccessTokens.Set(result.access_token, NormalizedServerOrigin(), result.provider, result.access_expires_in);
                result.access_token = null;
                ServerLoginPreferences.SetAutoLogin(NormalizedServerOrigin(), autoLogin);
                ContinueWithMaintenance(introUI, false);
            }
            catch (Exception ex)
            {
                result.access_token = null;
                result.refresh_token = null;
                Log?.LogError("Could not finish interactive login: " + ex.Message);
                AccessTokens.Clear();
                LoginInProgress = false;
                ShowLoginPanel(introUI);
            }
        };
        try
        {
            OpenPCLoginPopup.Invoke(null, new object[] { confirmed });
        }
        catch
        {
            result.access_token = null;
            result.refresh_token = null;
            LoginInProgress = false;
            throw;
        }
    }

    private static IEnumerator RefreshSession(object introUI)
    {
        int generation = Volatile.Read(ref RecoveryGeneration);
        RefreshCredential saved = null;
        while (saved == null)
        {
            if (generation != Volatile.Read(ref RecoveryGeneration))
            {
                yield break;
            }
            InvalidDataException invalid = null;
            Exception transient = null;
            try
            {
                saved = PrepareRefreshAttempt();
            }
            catch (InvalidDataException ex)
            {
                invalid = ex;
            }
            catch (FileNotFoundException ex)
            {
                invalid = new InvalidDataException("saved automatic-login credential was not found", ex);
            }
            catch (Exception ex)
            {
                transient = ex;
            }
            if (invalid != null)
            {
                Log?.LogError("Saved automatic login is invalid: " + invalid.Message);
                ClearSavedLogin();
                if (Volatile.Read(ref SessionRecoveryInProgress) != 0)
                {
                    FinishRecovery(false, "saved automatic-login credential is invalid", generation);
                }
                ShowLoginPanel(introUI);
                yield break;
            }
            if (transient != null)
            {
                Log?.LogWarning("Secure automatic-login storage is temporarily unavailable: " + transient.Message);
                if (Volatile.Read(ref SessionRecoveryInProgress) == 0)
                {
                    LoginInProgress = false;
                    ShowLoginPanel(introUI);
                    yield break;
                }
                yield return new WaitForSecondsRealtime(2f);
            }
        }
        string refreshToken = saved.pending_refresh_token;
        string attemptID = saved.pending_attempt_id;
        saved.refresh_token = null;
        saved.pending_refresh_token = null;
        saved.pending_attempt_id = null;
        float retryDelay = 1f;
        while (true)
        {
            byte[] body = BuildRefreshRequest(refreshToken, attemptID);
            {
                ControlProbeResult request = null;
                yield return AuthRequest(introUI, new Uri(ServerRoot, "auth/session/refresh"), UnityWebRequest.kHttpVerbPOST, body, null, result => request = result);
                if (request == null) yield break;
                if (generation != Volatile.Read(ref RecoveryGeneration))
                {
                    yield break;
                }
                bool credentialRejected = (request.StatusCode == 401 || request.StatusCode == 409) && request.RefreshInvalid;
                if (credentialRejected)
                {
                    Log?.LogWarning("Automatic-login credential rejected: HTTP=" + request.StatusCode +
                        ", refreshInvalid=" + request.RefreshInvalid + ", classification=invalid-credential");
                    refreshToken = null;
                    attemptID = null;
                    ClearSavedLogin();
                    if (Volatile.Read(ref SessionRecoveryInProgress) != 0)
                    {
                        FinishRecovery(false, "saved automatic-login credential was rejected", generation);
                    }
                    ShowLoginPanel(introUI);
                    yield break;
                }
                if (!request.Success)
                {
                    Log?.LogWarning("Automatic login temporarily unavailable; the same refresh attempt will be retried: " + request.Error);
                    if (Volatile.Read(ref SessionRecoveryInProgress) == 0)
                    {
                        LoginInProgress = false;
                        ShowLoginPanel(introUI);
                        yield break;
                    }
                }
                else
                {
                    TokenResult result = null;
                    try
                    {
                        result = JsonUtility.FromJson<TokenResult>(request.Body);
                        request.Body = null;
                    }
                    catch (Exception ex)
                    {
                        Log?.LogWarning("Automatic login returned an unreadable response; the same refresh attempt will be retried: " + ex.GetType().Name);
                    }
                    if (ValidRefreshResult(result) && ProviderEnabled(result.provider))
                    {
                        try
                        {
                            StoreRefresh(result);
                            if (result.access_expires_in <= 30)
                            {
                                result.access_token = null;
                                saved = PrepareRefreshAttempt();
                                refreshToken = saved.pending_refresh_token;
                                attemptID = saved.pending_attempt_id;
                                saved.refresh_token = null;
                                saved.pending_refresh_token = null;
                                saved.pending_attempt_id = null;
                                retryDelay = 1f;
                                continue;
                            }
                            AccessTokens.Set(result.access_token, NormalizedServerOrigin(), result.provider, result.access_expires_in);
                            result.access_token = null;
                            refreshToken = null;
                            attemptID = null;
                        }
                        catch (Exception ex)
                        {
                            result.access_token = null;
                            result.refresh_token = null;
                            Log?.LogWarning("Could not persist the rotated automatic-login credential; the committed attempt will be retrieved again: " + ex.Message);
                            if (Volatile.Read(ref SessionRecoveryInProgress) == 0)
                            {
                                LoginInProgress = false;
                                ShowLoginPanel(introUI);
                                yield break;
                            }
                        }
                        if (AccessTokens.IsUsable(NormalizedServerOrigin()))
                        {
                            ContinueWithMaintenance(introUI, true);
                            yield break;
                        }
                    }
                    Log?.LogWarning("Automatic login returned incomplete credentials; the same refresh attempt will be retried");
                    if (Volatile.Read(ref SessionRecoveryInProgress) == 0)
                    {
                        LoginInProgress = false;
                        ShowLoginPanel(introUI);
                        yield break;
                    }
                }
            }
            yield return new WaitForSecondsRealtime(retryDelay);
            retryDelay = Math.Min(retryDelay * 2f, 5f);
        }
    }

    private static void ContinueWithMaintenance(object introUI, bool automatic)
    {
        try
        {
            ContinueMaintenance = true;
            Log?.LogInfo("Authenticated login is requesting maintenance information: automatic=" + automatic);
            SendMaintenance.Invoke(introUI, new object[] { automatic });
        }
        finally
        {
            ContinueMaintenance = false;
            LoginInProgress = false;
        }
    }

    private static void StartIntroCoroutine(object introUI, IEnumerator routine)
    {
        if (!(introUI is MonoBehaviour owner) || owner == null)
        {
            throw new InvalidOperationException("IntroUI coroutine owner is unavailable");
        }
        owner.StartCoroutine(routine ?? throw new ArgumentNullException(nameof(routine)));
    }

    private static bool CanAttemptAutomaticLogin()
    {
        if (!RefreshCredentials.IsSupported)
        {
            return false;
        }
        try
        {
            return RefreshCredentials.Contains(NormalizedServerOrigin());
        }
        catch (Exception ex)
        {
            Log?.LogWarning("Could not inspect the secure automatic-login credential: " + ex.Message);
            return false;
        }
    }

    private static void StoreRefresh(TokenResult result)
    {
        string origin = NormalizedServerOrigin();
        RefreshCredential credential = new RefreshCredential
        {
            version = 2,
            origin = origin,
            provider = result.provider,
            refresh_token = result.refresh_token,
            expires_at = DateTimeOffset.UtcNow.ToUnixTimeSeconds() + result.refresh_expires_in
        };
        RefreshCredentials.Save(origin, credential);
        credential.refresh_token = null;
        result.refresh_token = null;
    }

    private static RefreshCredential LoadRefresh()
    {
        string origin = NormalizedServerOrigin();
        RefreshCredential credential = RefreshCredentials.Load(origin);
        if (credential == null || credential.version < 1 || credential.version > 2 || credential.origin != origin ||
            !ProviderEnabled(credential.provider) || string.IsNullOrEmpty(credential.refresh_token) ||
            credential.expires_at <= DateTimeOffset.UtcNow.ToUnixTimeSeconds())
        {
            if (credential != null)
            {
                credential.refresh_token = null;
            }
            throw new InvalidDataException("saved automatic-login credential is invalid, expired, or belongs to another server");
        }
        return credential;
    }

    private static RefreshCredential PrepareRefreshAttempt()
    {
        string origin = NormalizedServerOrigin();
        RefreshCredential credential = LoadRefresh();
        bool missingAttempt = string.IsNullOrEmpty(credential.pending_attempt_id) ||
                              string.IsNullOrEmpty(credential.pending_refresh_token);
        if (missingAttempt)
        {
            credential.version = 2;
            credential.pending_attempt_id = System.Guid.NewGuid().ToString("N");
            credential.pending_refresh_token = credential.refresh_token;
            RefreshCredentials.Save(origin, credential);
        }
        return credential;
    }

    private static byte[] BuildRefreshRequest(string token, string attemptID)
    {
        if (string.IsNullOrEmpty(token) || string.IsNullOrEmpty(attemptID))
        {
            throw new InvalidDataException("refresh token or attempt ID is empty");
        }
        foreach (char item in token + attemptID)
        {
            bool safe = item >= 'a' && item <= 'z' || item >= 'A' && item <= 'Z' ||
                        item >= '0' && item <= '9' || item == '-' || item == '_';
            if (!safe)
            {
                throw new InvalidDataException("refresh token contains an unexpected character");
            }
        }
        return Encoding.UTF8.GetBytes("{\"refresh_token\":\"" + token + "\",\"attempt_id\":\"" + attemptID + "\"}");
    }

    private static void ClearSavedLogin()
    {
        AccessTokens.Clear();
        ServerLoginPreferences.SetAutoLogin(NormalizedServerOrigin(), false);
        ServerLoginPreferences.SetUseAutoLoginPC(NormalizedServerOrigin(), false);
        DeleteCurrentRefresh();
    }

    private static void DeleteCurrentRefresh()
    {
        if (ServerRoot == null || RefreshCredentials == null || !RefreshCredentials.IsSupported)
        {
            return;
        }
        try
        {
            RefreshCredentials.Delete(NormalizedServerOrigin());
        }
        catch (Exception ex)
        {
            Log?.LogWarning("Could not delete the secure automatic-login credential: " + ex.Message);
        }
    }

    private static bool ValidTokenResult(TokenResult result)
    {
        return result != null && !string.IsNullOrEmpty(result.provider) &&
               !string.IsNullOrEmpty(result.access_token) && result.access_expires_in > 0 &&
               !string.IsNullOrEmpty(result.refresh_token) && result.refresh_expires_in > 0;
    }

    private static bool ValidRefreshResult(TokenResult result)
    {
        return result != null && !string.IsNullOrEmpty(result.provider) &&
               !string.IsNullOrEmpty(result.access_token) && result.access_expires_in >= 0 &&
               !string.IsNullOrEmpty(result.refresh_token) && result.refresh_expires_in > 0;
    }

    internal static bool ProviderEnabled(string provider)
    {
        if (Authentication?.providers == null)
        {
            return false;
        }
        foreach (string enabled in Authentication.providers)
        {
            if (enabled == provider)
            {
                return true;
            }
        }
        return false;
    }

    private static void ValidateAuthentication(ServerAuthentication policy)
    {
        if (policy == null || (policy.mode != "local" && policy.mode != "oauth") || policy.providers == null)
        {
            throw new InvalidDataException("missing or unknown authentication mode");
        }
        if (policy.mode == "local" && policy.providers.Length != 0)
        {
            throw new InvalidDataException("local mode enabled providers");
        }
        if (policy.mode == "oauth" && policy.providers.Length == 0)
        {
            throw new InvalidDataException("oauth mode omitted providers");
        }
        for (int i = 0; i < policy.providers.Length; i++)
        {
            string provider = policy.providers[i];
            if (provider != "discord" && provider != "google")
            {
                throw new InvalidDataException("unsupported provider " + provider);
            }
            for (int j = 0; j < i; j++)
            {
                if (policy.providers[j] == provider)
                {
                    throw new InvalidDataException("duplicate provider " + provider);
                }
            }
        }
    }

    [Serializable]
    private sealed class DeviceRequest
    {
        public string provider;
    }

    [Serializable]
    private sealed class DeviceStart
    {
        public string transaction_id = null;
        public string device_secret = null;
        public string start_url = null;
        public int expires_in = 0;
        public int poll_interval = 0;
    }

    [Serializable]
    private sealed class TokenResult
    {
        public string provider = null;
        public string access_token = null;
        public long access_expires_in = 0;
        public string refresh_token = null;
        public long refresh_expires_in = 0;
    }

    [Serializable]
    internal sealed class ServerAuthentication
    {
        public string mode = null;
        public string[] providers = null;
    }

}
