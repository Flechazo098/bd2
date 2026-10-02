using System;
using System.Collections;
using System.IO;
using System.Reflection;
using System.Text;
using System.Threading;
using BepInEx;
using BepInEx.Logging;
using HarmonyLib;
using UnityEngine;
using UnityEngine.Events;
using UnityEngine.Networking;
using UnityEngine.UI;

namespace Bd2LoginUI;

[BepInPlugin(Guid, Name, Version)]
[BepInDependency("bd2.localidentity", BepInDependency.DependencyFlags.HardDependency)]
public sealed class Plugin : BaseUnityPlugin
{
    public const string Guid = "bd2.login.ui";
    public const string Name = "BD2 Login UI";
    public const string Version = Bd2Build.Versions.Plugin;

    private const string SymbolResource = "Bd2LoginUI.Assets.Discord-Symbol.png";
    private const string WordmarkResource = "Bd2LoginUI.Assets.Discord-Wordmark.png";
    private const string LocalAccessToken = "bd2-local-development-user";
    private static readonly Color DiscordBlurple = new Color32(88, 101, 242, 255);

    private static ManualLogSource Log;
    private static Sprite DiscordSymbol;
    private static Sprite DiscordWordmark;
    private static MethodInfo SetIntroState;
    private static MethodInfo SendMaintenance;
    private static ServerAuthentication Authentication;
    private static Uri ServerRoot;
    private static string AuthenticationLoadingOrigin;
    private static bool ContinueMaintenance;
    private static bool LoginInProgress;
    private static MethodInfo OpenPCLoginPopup;
    private static MemoryAccessTokenStore AccessTokens;
    private static IRefreshCredentialStore RefreshCredentials;
    private static int SessionRecoveryInProgress;

    private void Awake()
    {
        try
        {
            Log = Logger;
            AccessTokens = new MemoryAccessTokenStore();
            RefreshCredentials = PlatformRefreshCredentialStore.Create();
            DiscordSymbol = LoadSprite(SymbolResource, "BD2 Discord Symbol");
            DiscordWordmark = LoadSprite(WordmarkResource, "BD2 Discord Wordmark");

            Type introUI = FindType("IntroUI");
            MethodInfo awake = introUI?.GetMethod(
                "Awake",
                BindingFlags.Instance | BindingFlags.NonPublic,
                null,
                Type.EmptyTypes,
                null);
            if (awake == null || awake.ReturnType != typeof(void))
            {
                throw new MissingMethodException("IntroUI.Awake() was not found (client version mismatch)");
            }
            SendMaintenance = introUI.GetMethod(
                "SendMaintenanceInfo",
                BindingFlags.Instance | BindingFlags.Public,
                null,
                new[] { typeof(bool) },
                null);
            SetIntroState = FindSetIntroState(introUI);
            OpenPCLoginPopup = FindOpenPCLoginPopup();
            MethodInfo accessTokenGetter = FindAccessTokenGetter();
            MethodInfo clearPCLocalData = FindClearPCLocalData();
            MethodInfo sendWebRequest = typeof(UnityWebRequest).GetMethod(
                nameof(UnityWebRequest.SendWebRequest),
                BindingFlags.Instance | BindingFlags.Public,
                null,
                Type.EmptyTypes,
                null);
            Type networkManager = FindType("BDNetwork.NetworkManager");
            MethodInfo clientNetworkError = networkManager?.GetMethod(
                "ClientNetworkError",
                BindingFlags.Instance | BindingFlags.Public);
            MethodInfo exponentialBackOff = networkManager?.GetMethod(
                "ὧὥὡὠὮὦὯὥὭὣὩ",
                BindingFlags.Instance | BindingFlags.NonPublic) ?? networkManager?.GetMethod(
                "ExponetialBackOff",
                BindingFlags.Instance | BindingFlags.NonPublic);
            if (SendMaintenance == null || SetIntroState == null || OpenPCLoginPopup == null ||
                accessTokenGetter == null || clearPCLocalData == null || sendWebRequest == null ||
                clientNetworkError == null || exponentialBackOff == null)
            {
                throw new MissingMethodException("IntroUI authentication transition methods were not found (client version mismatch)");
            }

            Harmony harmony = new Harmony(Guid);
            harmony.Patch(awake, postfix: new HarmonyMethod(typeof(Plugin), nameof(IntroAwakePostfix)));
            HarmonyMethod maintenancePrefix = new HarmonyMethod(typeof(Plugin), nameof(SendMaintenancePrefix));
            maintenancePrefix.after = new[] { "bd2.localidentity" };
            harmony.Patch(SendMaintenance, prefix: maintenancePrefix);
            harmony.Patch(
                accessTokenGetter,
                prefix: new HarmonyMethod(typeof(Plugin), nameof(AccessTokenPrefix)));
            harmony.Patch(
                clearPCLocalData,
                postfix: new HarmonyMethod(typeof(Plugin), nameof(ClearPCLocalDataPostfix)));
            harmony.Patch(
                sendWebRequest,
                postfix: new HarmonyMethod(typeof(Plugin), nameof(SendWebRequestPostfix)));
            harmony.Patch(
                clientNetworkError,
                prefix: new HarmonyMethod(typeof(Plugin), nameof(SuppressNetworkErrorDuringRecovery)));
            harmony.Patch(
                exponentialBackOff,
                prefix: new HarmonyMethod(typeof(Plugin), nameof(SuppressNetworkErrorDuringRecovery)));
            Logger.LogInfo("Server-authoritative Discord and Google login UI patch installed");
        }
        catch (Exception ex)
        {
            Logger.LogError("Login UI patch failed: " + ex);
        }
    }

    private static void IntroAwakePostfix(object __instance)
    {
        ConfigureLoginPanel(__instance);
    }

    private static void SendWebRequestPostfix(
        UnityWebRequest __instance,
        UnityWebRequestAsyncOperation __result)
    {
        if (__instance == null || __result == null)
        {
            return;
        }
        __result.completed += delegate
        {
            InspectCompletedGameRequest(__instance);
        };
    }

    private static void InspectCompletedGameRequest(UnityWebRequest request)
    {
        try
        {
            if (!IsCurrentGameRequest(request, out Uri requestUri))
            {
                return;
            }
            if (requestUri.AbsolutePath.Equals("/game/LoginUser", StringComparison.Ordinal) &&
                request.responseCode >= 200 && request.responseCode < 300)
            {
                Interlocked.Exchange(ref SessionRecoveryInProgress, 0);
                return;
            }
            if (request.responseCode != 401 ||
                !string.Equals(request.GetResponseHeader("X-BD2-Session-Expired"), "1", StringComparison.Ordinal))
            {
                return;
            }
            RecoverExpiredGameSession();
        }
        catch (Exception ex)
        {
            Log?.LogError("Could not inspect the completed game request: " + ex);
        }
    }

    private static bool IsCurrentGameRequest(UnityWebRequest request, out Uri requestUri)
    {
        requestUri = null;
        if (ServerRoot == null || request == null ||
            !Uri.TryCreate(request.url, UriKind.Absolute, out Uri parsed) ||
            !SameOrigin(ServerRoot, parsed) ||
            !parsed.AbsolutePath.StartsWith("/game/", StringComparison.Ordinal))
        {
            return false;
        }
        requestUri = parsed;
        return true;
    }

    private static void RecoverExpiredGameSession()
    {
        if (Interlocked.CompareExchange(ref SessionRecoveryInProgress, 1, 0) != 0)
        {
            return;
        }
        try
        {
            object network = FindSGSingleton("Net");
            object app = FindSGSingleton("App");
            MethodInfo refresh = network?.GetType().GetMethod(
                "Refresh",
                BindingFlags.Instance | BindingFlags.Public,
                null,
                Type.EmptyTypes,
                null);
            MethodInfo restart = app?.GetType().GetMethod(
                "AppReStart",
                BindingFlags.Instance | BindingFlags.Public,
                null,
                Type.EmptyTypes,
                null);
            if (refresh == null || restart == null)
            {
                throw new MissingMethodException("client game-session recovery methods were not found");
            }
            Log?.LogWarning("Game session expired; returning to login and creating a new session");
            refresh.Invoke(network, null);
            restart.Invoke(app, null);
        }
        catch
        {
            Interlocked.Exchange(ref SessionRecoveryInProgress, 0);
            throw;
        }
    }

    private static bool SuppressNetworkErrorDuringRecovery()
    {
        return Volatile.Read(ref SessionRecoveryInProgress) == 0;
    }

    private static bool AccessTokenPrefix(ref string __result)
    {
        if (Authentication != null && Authentication.mode == "oauth")
        {
            __result = AccessTokens.Get();
            return false;
        }
        __result = LocalAccessToken;
        return false;
    }

    private static void ClearPCLocalDataPostfix()
    {
        AccessTokens.Clear();
        PlayerPrefs.DeleteKey("AccessToken");
        DeleteCurrentRefresh();
        PlayerPrefs.Save();
    }

    private static bool SendMaintenancePrefix(object __instance, bool __0)
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
                ServerRoot = currentRoot;
                PlayerPrefs.DeleteKey("AccessToken");
                PlayerPrefs.Save();
            }
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
        using (UnityWebRequest request = UnityWebRequest.Get(endpoint))
        {
            request.timeout = 10;
            yield return request.SendWebRequest();
            if (AuthenticationLoadingOrigin == expectedOrigin)
            {
                AuthenticationLoadingOrigin = null;
            }
            if (ServerRoot == null || !SameOrigin(ServerRoot, expectedRoot))
            {
                yield break;
            }
            if (request.result != UnityWebRequest.Result.Success)
            {
                Log?.LogError("Authentication policy request failed: " + request.error);
                yield break;
            }
            try
            {
                ServerAuthentication policy = JsonUtility.FromJson<ServerAuthentication>(request.downloadHandler.text);
                ValidateAuthentication(policy);
                Authentication = policy;
                Log?.LogInfo("Server authentication mode: " + policy.mode);
                ApplyAuthenticationPolicy(introUI);
            }
            catch (Exception ex)
            {
                Log?.LogError("Server returned an invalid authentication policy: " + ex.Message);
            }
        }
    }

    private static void ApplyAuthenticationPolicy(object introUI)
    {
        if (Authentication.mode == "local")
        {
            try
            {
                AccessTokens.Clear();
                PlayerPrefs.DeleteKey("AccessToken");
                PlayerPrefs.Save();
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
        // Earlier development builds stored both local identifiers and OAuth
        // access credentials in this key. OAuth credentials now live only in
        // process memory, so remove any legacy plaintext before proceeding.
        PlayerPrefs.DeleteKey("AccessToken");
        if (!RefreshCredentials.IsSupported)
        {
            PlayerPrefs.SetInt("IsAutoLogin", 0);
            PlayerPrefs.SetInt("StandaloneAutoLogin", 0);
            Log?.LogWarning("Secure refresh credential storage is unavailable; automatic login is disabled on this platform");
        }
        PlayerPrefs.Save();
        if (LoginInProgress)
        {
            return;
        }
        if (PlayerPrefs.GetInt("IsAutoLogin", 0) != 0 &&
            PlayerPrefs.GetInt("StandaloneAutoLogin", 0) != 0 &&
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
        AccessTokens.Clear();
        LoginInProgress = false;
        ConfigureLoginPanel(introUI);
        Type stateType = SetIntroState.GetParameters()[0].ParameterType;
        SetIntroState.Invoke(introUI, new[] { Enum.ToObject(stateType, 1) });
        Log?.LogInfo("Waiting for server-authorized third-party authentication");
    }

    private static void ConfigureLoginPanel(object introUI)
    {
        if (Authentication == null || Authentication.mode != "oauth")
        {
            return;
        }
        try
        {
            Component component = introUI as Component;
            Transform panel = component == null ? null : FindDescendant(component.transform, "SignInWithAccount");
            if (panel == null)
            {
                throw new MissingMemberException("IntroUI/SignInWithAccount was not found");
            }

            Button google = FindButton(panel, "Button - Google");
            Button discord = FindButton(panel, "Button - Facebook");
            if (discord == null)
            {
                discord = FindButton(panel, "Button - Discord");
            }
            if (google == null || discord == null)
            {
                throw new MissingMemberException("Google or Facebook/Discord login button was not found");
            }

            Image box = FindImage(discord.transform, "Image - Box");
            Image logo = FindImage(discord.transform, "Image - Logo");
            Image title = FindImage(discord.transform, "Image - Title");
            if (box == null || logo == null || title == null)
            {
                throw new MissingMemberException("Discord button images were not found");
            }

            SetActive(panel, "Button - Apple", false);
            SetActive(panel, "Button - Email", false);
            SetActive(panel, "Button - Mail", false);
            SetActive(panel, "Button - Guest", false);

            google.gameObject.SetActive(ProviderEnabled("google"));
            discord.gameObject.SetActive(ProviderEnabled("discord"));
            int providerCount = (google.gameObject.activeSelf ? 1 : 0) +
                (discord.gameObject.activeSelf ? 1 : 0);
            discord.transform.SetSiblingIndex(0);
            google.transform.SetSiblingIndex(1);
            discord.gameObject.name = "Button - Discord";

            ReplaceClick(google, introUI, "google");
            ReplaceClick(discord, introUI, "discord");
            ApplyDiscordBrand(box, logo, title);
            ConfigureProviderGrid(panel, providerCount);

            Canvas.ForceUpdateCanvases();
            if (panel is RectTransform panelRect)
            {
                LayoutRebuilder.ForceRebuildLayoutImmediate(panelRect);
            }
        }
        catch (Exception ex)
        {
            Log?.LogError("Could not configure login panel: " + ex);
        }
    }

    private static void ConfigureProviderGrid(Transform panel, int providerCount)
    {
        GridLayoutGroup grid = panel.GetComponentInChildren<GridLayoutGroup>(true);
        if (grid == null)
        {
            throw new MissingMemberException("Login provider grid was not found");
        }
        int columns = Math.Max(1, providerCount);
        grid.constraint = GridLayoutGroup.Constraint.FixedColumnCount;
        grid.constraintCount = columns;
        if (grid.transform is RectTransform gridRect)
        {
            float width = grid.padding.horizontal + grid.cellSize.x * columns +
                grid.spacing.x * Math.Max(0, columns - 1);
            UpdateBetterGridProfiles(grid, columns);
            UpdateBetterLocatorProfiles(grid, width);
            gridRect.SetSizeWithCurrentAnchors(RectTransform.Axis.Horizontal, width);
            LayoutRebuilder.ForceRebuildLayoutImmediate(gridRect);
        }
    }

    private static void UpdateBetterGridProfiles(GridLayoutGroup grid, int columns)
    {
        Type type = grid.GetType();
        if (type.FullName != "TheraBytes.BetterUi.BetterGridLayoutGroup")
        {
            return;
        }
        const BindingFlags flags = BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic;
        UpdateBetterGridSettings(type.GetField("settingsFallback", flags)?.GetValue(grid), columns);
        object collection = type.GetField("customSettings", flags)?.GetValue(grid);
        IEnumerable items = collection?.GetType().GetProperty("Items", flags)?.GetValue(collection, null) as IEnumerable;
        if (items == null)
        {
            return;
        }
        foreach (object settings in items)
        {
            UpdateBetterGridSettings(settings, columns);
        }
    }

    private static void UpdateBetterGridSettings(object settings, int columns)
    {
        if (settings == null)
        {
            return;
        }
        Type type = settings.GetType();
        FieldInfo constraint = type.GetField("Constraint", BindingFlags.Instance | BindingFlags.Public);
        FieldInfo count = type.GetField("ConstraintCount", BindingFlags.Instance | BindingFlags.Public);
        if (constraint != null)
        {
            constraint.SetValue(settings, Enum.ToObject(constraint.FieldType, (int)GridLayoutGroup.Constraint.FixedColumnCount));
        }
        count?.SetValue(settings, columns);
    }

    private static void UpdateBetterLocatorProfiles(GridLayoutGroup grid, float width)
    {
        const BindingFlags flags = BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic;
        foreach (Component component in grid.GetComponents<Component>())
        {
            Type type = component?.GetType();
            if (type?.FullName != "TheraBytes.BetterUi.BetterLocator")
            {
                continue;
            }
            UpdateBetterRectTransformData(type.GetField("transformFallback", flags)?.GetValue(component), width);
            object collection = type.GetField("transformConfigs", flags)?.GetValue(component);
            IEnumerable items = collection?.GetType().GetProperty("Items", flags)?.GetValue(collection, null) as IEnumerable;
            if (items == null)
            {
                continue;
            }
            foreach (object data in items)
            {
                UpdateBetterRectTransformData(data, width);
            }
        }
    }

    private static void UpdateBetterRectTransformData(object data, float width)
    {
        FieldInfo sizeField = data?.GetType().GetField("SizeDelta", BindingFlags.Instance | BindingFlags.Public);
        if (sizeField == null || sizeField.FieldType != typeof(Vector2))
        {
            return;
        }
        Vector2 size = (Vector2)sizeField.GetValue(data);
        size.x = width;
        sizeField.SetValue(data, size);
    }

    private static void ReplaceClick(Button button, object introUI, string provider)
    {
        // Assigning a fresh event removes both serialized persistent calls and
        // the listeners that IntroUI.Awake adds at runtime.
        button.onClick = new Button.ButtonClickedEvent();
        button.onClick.AddListener(new UnityAction(delegate { OpenLogin(introUI, provider); }));
        button.interactable = true;
    }

    private static void OpenLogin(object introUI, string provider)
    {
        try
        {
            if (LoginInProgress)
            {
                return;
            }
            PropertyInfo canInteraction = introUI?.GetType().GetProperty(
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
        string endpoint = new Uri(ServerRoot, "auth/device").AbsoluteUri;
        byte[] body = Encoding.UTF8.GetBytes(JsonUtility.ToJson(new DeviceRequest { provider = provider }));
        using (UnityWebRequest request = JsonPost(endpoint, body))
        {
            yield return request.SendWebRequest();
            Array.Clear(body, 0, body.Length);
            if (request.result != UnityWebRequest.Result.Success)
            {
                LoginInProgress = false;
                Log?.LogError("Could not create login transaction: " + request.error);
                yield break;
            }
            DeviceStart start;
            try
            {
                start = JsonUtility.FromJson<DeviceStart>(request.downloadHandler.text);
                if (start == null || string.IsNullOrEmpty(start.transaction_id) || string.IsNullOrEmpty(start.device_secret) || string.IsNullOrEmpty(start.start_url))
                {
                    throw new InvalidDataException("incomplete transaction response");
                }
                ValidateBrowserURL(start.start_url);
            }
            catch (Exception ex)
            {
                LoginInProgress = false;
                Log?.LogError("Invalid login transaction: " + ex.Message);
                yield break;
            }
            Application.OpenURL(start.start_url);
            yield return PollDevice(introUI, start);
        }
    }

    private static IEnumerator PollDevice(object introUI, DeviceStart start)
    {
        int delay = Math.Max(1, start.poll_interval);
        float deadline = Time.realtimeSinceStartup + Math.Max(30, start.expires_in);
        string endpoint = new Uri(ServerRoot, "auth/device/" + Uri.EscapeDataString(start.transaction_id) + "/poll").AbsoluteUri;
        while (Time.realtimeSinceStartup < deadline)
        {
            yield return new WaitForSecondsRealtime(delay);
            using (UnityWebRequest request = JsonPost(endpoint, Array.Empty<byte>()))
            {
                request.SetRequestHeader("Authorization", "Device " + start.device_secret);
                yield return request.SendWebRequest();
                if (request.responseCode == 202)
                {
                    continue;
                }
                if (request.result != UnityWebRequest.Result.Success)
                {
                    LoginInProgress = false;
                    Log?.LogError("Login transaction failed: HTTP " + request.responseCode);
                    yield break;
                }
                TokenResult result;
                try
                {
                    result = JsonUtility.FromJson<TokenResult>(request.downloadHandler.text);
                }
                catch (Exception ex)
                {
                    LoginInProgress = false;
                    Log?.LogError("Login transaction returned invalid credentials: " + ex.Message);
                    yield break;
                }
                if (!ValidTokenResult(result) || !ProviderEnabled(result.provider))
                {
                    LoginInProgress = false;
                    Log?.LogError("Login transaction returned incomplete credentials");
                    yield break;
                }
                CompleteInteractiveLogin(introUI, result);
                yield break;
            }
        }
        LoginInProgress = false;
        Log?.LogError("Login transaction expired");
    }

    private static void CompleteInteractiveLogin(object introUI, TokenResult result)
    {
        if (!RefreshCredentials.IsSupported)
        {
            AccessTokens.Set(result.access_token);
            result.access_token = null;
            result.refresh_token = null;
            PlayerPrefs.SetInt("IsAutoLogin", 0);
            PlayerPrefs.SetInt("StandaloneAutoLogin", 0);
            PlayerPrefs.DeleteKey("AccessToken");
            PlayerPrefs.Save();
            Log?.LogWarning("Login succeeded, but automatic login remains disabled because this platform has no supported secure credential store");
            ContinueWithMaintenance(introUI, false);
            return;
        }
        Action confirmed = delegate
        {
            try
            {
                bool autoLogin = PlayerPrefs.GetInt("StandaloneAutoLogin", 0) != 0;
                if (autoLogin)
                {
                    StoreRefresh(result);
                }
                else
                {
                    DeleteCurrentRefresh();
                    result.refresh_token = null;
                }
                AccessTokens.Set(result.access_token);
                result.access_token = null;
                PlayerPrefs.SetInt("IsAutoLogin", autoLogin ? 1 : 0);
                PlayerPrefs.DeleteKey("AccessToken");
                PlayerPrefs.Save();
                ContinueWithMaintenance(introUI, false);
            }
            catch (Exception ex)
            {
                result.access_token = null;
                result.refresh_token = null;
                Log?.LogError("Could not finish interactive login: " + ex.Message);
                ClearSavedLogin();
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
        RefreshCredential saved;
        try
        {
            saved = LoadRefresh();
        }
        catch (Exception ex)
        {
            Log?.LogError("Saved automatic login could not be decrypted: " + ex.Message);
            ClearSavedLogin();
            ShowLoginPanel(introUI);
            yield break;
        }
        byte[] body = BuildRefreshRequest(saved.refresh_token);
        saved.refresh_token = null;
        using (UnityWebRequest request = JsonPost(new Uri(ServerRoot, "auth/session/refresh").AbsoluteUri, body))
        {
            yield return request.SendWebRequest();
            Array.Clear(body, 0, body.Length);
            if (request.responseCode == 401 || request.responseCode == 403)
            {
                ClearSavedLogin();
                ShowLoginPanel(introUI);
                yield break;
            }
            if (request.result != UnityWebRequest.Result.Success)
            {
                // Refresh tokens are single-use. A transport failure can occur
                // after the server committed rotation, so retrying the saved
                // token could be interpreted as replay and revoke its family.
                Log?.LogError("Automatic login temporarily unavailable: " + request.error);
                ClearSavedLogin();
                ShowLoginPanel(introUI);
                yield break;
            }
            TokenResult result;
            try
            {
                result = JsonUtility.FromJson<TokenResult>(request.downloadHandler.text);
            }
            catch (Exception ex)
            {
                Log?.LogError("Automatic login returned invalid credentials: " + ex.Message);
                ClearSavedLogin();
                ShowLoginPanel(introUI);
                yield break;
            }
            if (!ValidTokenResult(result) || !ProviderEnabled(result.provider))
            {
                Log?.LogError("Automatic login returned incomplete credentials");
                ClearSavedLogin();
                ShowLoginPanel(introUI);
                yield break;
            }
            // Persist the rotated refresh credential before exposing the new
            // access credential. The old plaintext exists only in managed
            // memory until the request body is cleared above.
            try
            {
                StoreRefresh(result);
                AccessTokens.Set(result.access_token);
                result.access_token = null;
                PlayerPrefs.DeleteKey("AccessToken");
                PlayerPrefs.Save();
            }
            catch (Exception ex)
            {
                result.access_token = null;
                result.refresh_token = null;
                Log?.LogError("Could not persist the rotated automatic-login credential: " + ex.Message);
                ClearSavedLogin();
                ShowLoginPanel(introUI);
                yield break;
            }
            ContinueWithMaintenance(introUI, true);
        }
    }

    private static void ContinueWithMaintenance(object introUI, bool automatic)
    {
        try
        {
            ContinueMaintenance = true;
            SendMaintenance.Invoke(introUI, new object[] { automatic });
        }
        finally
        {
            ContinueMaintenance = false;
            LoginInProgress = false;
        }
    }

    private static UnityWebRequest JsonPost(string url, byte[] body)
    {
        UnityWebRequest request = new UnityWebRequest(url, UnityWebRequest.kHttpVerbPOST)
        {
            uploadHandler = new UploadHandlerRaw(body),
            downloadHandler = new DownloadHandlerBuffer(),
            timeout = 15
        };
        request.SetRequestHeader("Content-Type", "application/json");
        return request;
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
            Log?.LogError("Could not inspect the secure automatic-login credential: " + ex.Message);
            ClearSavedLogin();
            return false;
        }
    }

    private static void StoreRefresh(TokenResult result)
    {
        string origin = NormalizedServerOrigin();
        RefreshCredential credential = new RefreshCredential
        {
            version = 1,
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
        if (credential == null || credential.version != 1 || credential.origin != origin ||
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

    private static byte[] BuildRefreshRequest(string token)
    {
        if (string.IsNullOrEmpty(token))
        {
            throw new InvalidDataException("refresh token is empty");
        }
        foreach (char item in token)
        {
            bool safe = item >= 'a' && item <= 'z' || item >= 'A' && item <= 'Z' ||
                        item >= '0' && item <= '9' || item == '-' || item == '_';
            if (!safe)
            {
                throw new InvalidDataException("refresh token contains an unexpected character");
            }
        }
        return Encoding.UTF8.GetBytes("{\"refresh_token\":\"" + token + "\"}");
    }

    private static void ClearSavedLogin()
    {
        AccessTokens.Clear();
        PlayerPrefs.SetInt("IsAutoLogin", 0);
        PlayerPrefs.SetInt("StandaloneAutoLogin", 0);
        PlayerPrefs.DeleteKey("AccessToken");
        DeleteCurrentRefresh();
        PlayerPrefs.Save();
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

    private static string NormalizedServerOrigin()
    {
        return NormalizeOrigin(ServerRoot);
    }

    private static bool SameOrigin(Uri left, Uri right)
    {
        return string.Equals(NormalizeOrigin(left), NormalizeOrigin(right), StringComparison.Ordinal);
    }

    private static string NormalizeOrigin(Uri uri)
    {
        if (uri == null || !uri.IsAbsoluteUri || string.IsNullOrEmpty(uri.Host))
        {
            throw new InvalidOperationException("authentication server origin is unavailable");
        }
        string host = uri.IdnHost.ToLowerInvariant();
        int port = uri.IsDefaultPort ? -1 : uri.Port;
        UriBuilder builder = new UriBuilder(uri.Scheme.ToLowerInvariant(), host, port);
        return builder.Uri.GetLeftPart(UriPartial.Authority).TrimEnd('/');
    }

    private static void ValidateOAuthTransport(Uri uri)
    {
        if (uri == null || !uri.IsAbsoluteUri ||
            (uri.Scheme != Uri.UriSchemeHttps && !(uri.Scheme == Uri.UriSchemeHttp && uri.IsLoopback)))
        {
            throw new InvalidOperationException("OAuth requires HTTPS except when connecting to a loopback server");
        }
    }

    private static void ValidateBrowserURL(string raw)
    {
        if (!Uri.TryCreate(raw, UriKind.Absolute, out Uri uri) ||
            (uri.Scheme != Uri.UriSchemeHttps && !(uri.Scheme == Uri.UriSchemeHttp && uri.IsLoopback)) ||
            uri.UserInfo.Length != 0)
        {
            throw new InvalidDataException("server returned an unsafe browser login URL");
        }
    }

    private static void ApplyDiscordBrand(Image box, Image logo, Image title)
    {
        box.color = DiscordBlurple;
        logo.sprite = DiscordSymbol;
        logo.color = Color.white;
        logo.preserveAspect = true;
        title.sprite = DiscordWordmark;
        title.color = Color.white;
        title.preserveAspect = true;
        DisableSpriteLocalizer(logo.gameObject);
        DisableSpriteLocalizer(title.gameObject);
    }

    private static void DisableSpriteLocalizer(GameObject target)
    {
        foreach (Behaviour behaviour in target.GetComponents<Behaviour>())
        {
            if (behaviour.GetType().Name == "SpriteLocalizer")
            {
                behaviour.enabled = false;
            }
        }
    }

    private static Sprite LoadSprite(string resourceName, string name)
    {
        using Stream stream = Assembly.GetExecutingAssembly().GetManifestResourceStream(resourceName);
        if (stream == null)
        {
            throw new FileNotFoundException("Embedded login asset is missing", resourceName);
        }
        byte[] bytes = new byte[stream.Length];
        int offset = 0;
        while (offset < bytes.Length)
        {
            int read = stream.Read(bytes, offset, bytes.Length - offset);
            if (read == 0)
            {
                throw new EndOfStreamException("Unexpected end of embedded login asset " + resourceName);
            }
            offset += read;
        }
        Texture2D texture = new Texture2D(2, 2, TextureFormat.RGBA32, false, false)
        {
            name = name,
            filterMode = FilterMode.Bilinear,
            wrapMode = TextureWrapMode.Clamp
        };
        if (!ImageConversion.LoadImage(texture, bytes, true))
        {
            UnityEngine.Object.Destroy(texture);
            throw new InvalidDataException("Could not decode embedded login asset " + resourceName);
        }
        Sprite sprite = Sprite.Create(
            texture,
            new Rect(0f, 0f, texture.width, texture.height),
            new Vector2(0.5f, 0.5f),
            100f);
        sprite.name = name;
        return sprite;
    }

    private static Button FindButton(Transform root, string name)
    {
        Transform match = FindDescendant(root, name);
        return match == null ? null : match.GetComponent<Button>();
    }

    private static Image FindImage(Transform root, string name)
    {
        Transform match = FindDescendant(root, name);
        return match == null ? null : match.GetComponent<Image>();
    }

    private static void SetActive(Transform root, string name, bool active)
    {
        Transform match = FindDescendant(root, name);
        if (match != null)
        {
            match.gameObject.SetActive(active);
        }
    }

    private static Transform FindDescendant(Transform root, string name)
    {
        if (root == null)
        {
            return null;
        }
        if (root.name == name)
        {
            return root;
        }
        for (int i = 0; i < root.childCount; i++)
        {
            Transform match = FindDescendant(root.GetChild(i), name);
            if (match != null)
            {
                return match;
            }
        }
        return null;
    }

    private static Type FindType(string name)
    {
        Assembly assembly = Assembly.Load("Assembly-CSharp");
        Type type = assembly?.GetType(name, false);
        if (type != null)
        {
            return type;
        }
        foreach (Assembly loaded in AppDomain.CurrentDomain.GetAssemblies())
        {
            type = loaded.GetType(name, false);
            if (type != null)
            {
                return type;
            }
        }
        return null;
    }

    private static object FindSGSingleton(string propertyName)
    {
        Type sg = FindType("SG");
        PropertyInfo property = sg?.GetProperty(propertyName, BindingFlags.Static | BindingFlags.Public);
        return property?.GetValue(null);
    }

    private static bool ProviderEnabled(string provider)
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

    private static Uri CurrentMaintenanceUri()
    {
        Type serverURLInfo = FindType("ὫὡὩὤὣὨὯὭὥὠὩ") ?? FindType("ServerURLInfo");
        FieldInfo maintenanceUri = serverURLInfo?.GetField(
            "ὫὯὯὦὢὤὫὫὧὢὨ",
            BindingFlags.Static | BindingFlags.Public) ?? serverURLInfo?.GetField(
            "MaintenanceUri",
            BindingFlags.Static | BindingFlags.Public);
        Uri result = maintenanceUri?.GetValue(null) as Uri;
        if (result == null || (result.Scheme != Uri.UriSchemeHttp && result.Scheme != Uri.UriSchemeHttps))
        {
            throw new InvalidOperationException("current server maintenance URL is unavailable");
        }
        return result;
    }

    private static MethodInfo FindSetIntroState(Type introUI)
    {
        foreach (MethodInfo method in introUI.GetMethods(BindingFlags.Instance | BindingFlags.NonPublic))
        {
            ParameterInfo[] parameters = method.GetParameters();
            if (method.ReturnType == typeof(void) && parameters.Length == 1 && parameters[0].ParameterType.IsEnum &&
                parameters[0].ParameterType.DeclaringType == introUI && Enum.GetValues(parameters[0].ParameterType).Length == 10)
            {
                return method;
            }
        }
        return null;
    }

    private static MethodInfo FindOpenPCLoginPopup()
    {
        Type uiManager = FindType("ὩὭὨὪὨὨὮὣὪὣὥ") ?? FindType("UIManager");
        if (uiManager == null)
        {
            return null;
        }
        MethodInfo method = uiManager.GetMethod(
            "ὤὤὫὮὯὪὡὭὩὮὠ",
            BindingFlags.Static | BindingFlags.Public,
            null,
            new[] { typeof(Action) },
            null) ?? uiManager.GetMethod(
            "OpenPCLoginPopupUI",
            BindingFlags.Static | BindingFlags.Public,
            null,
            new[] { typeof(Action) },
            null);
        return method != null && method.ReturnType == typeof(void) ? method : null;
    }

    private static MethodInfo FindAccessTokenGetter()
    {
        Type commonPacket = FindType("ὣὡὧὡὦὣὣὬὨὪὫ") ??
                            FindType("CommonPacket") ??
                            FindType("BDNetwork.CommonPacket");
        PropertyInfo property = commonPacket?.GetProperty(
            "ὣὢὣὣὠὥὤὠὧὬὦ",
            BindingFlags.Static | BindingFlags.Public) ?? commonPacket?.GetProperty(
            "AccessToken",
            BindingFlags.Static | BindingFlags.Public);
        MethodInfo getter = property?.GetGetMethod();
        return getter != null && getter.IsStatic && getter.ReturnType == typeof(string) &&
               getter.GetParameters().Length == 0 ? getter : null;
    }

    private static MethodInfo FindClearPCLocalData()
    {
        Type platformManager = FindType("gamfs.Platform.PlatformManager");
        MethodInfo method = platformManager?.GetMethod(
            "ClearPCLocalData",
            BindingFlags.Instance | BindingFlags.Public,
            null,
            Type.EmptyTypes,
            null);
        return method != null && method.ReturnType == typeof(void) ? method : null;
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
    private sealed class ServerAuthentication
    {
        public string mode = null;
        public string[] providers = null;
    }
}
