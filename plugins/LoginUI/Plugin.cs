using System;
using System.Reflection;
using BepInEx;
using HarmonyLib;
using UnityEngine.Networking;
using BD2.GameNames;

using static BD2.GameNames.Game;
using static Bd2LoginUI.LoginPanel;
using static Bd2LoginUI.LoginRuntime;
using static Bd2LoginUI.SessionRecovery;
using static Bd2LoginUI.LoginController;

namespace Bd2LoginUI;

[BepInPlugin(PluginId, Name, Version)]
[BepInDependency("bd2.localidentity", BepInDependency.DependencyFlags.HardDependency)]
public sealed class Plugin : BaseUnityPlugin
{
    public const string PluginId = "bd2.login.ui";
    public const string Name = "BD2 Login UI";
    public const string Version = Bd2Build.Versions.Plugin;

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Performance", "CA1822", Justification = "Unity invokes this instance lifecycle callback.")]
    private void OnApplicationQuit()
    {
        ApplicationQuitting = true;
        DisposeGameRelay();
        PlatformControlHttp.Shutdown();
    }

    private void Awake()
    {
        try
        {
            Log = Logger;
            PlatformControlHttp.SetCompletionLogger(message => Logger.LogInfo(message));
            if (!string.IsNullOrEmpty(Environment.GetEnvironmentVariable("UNITY_PROXYSERVER")) &&
                !string.IsNullOrEmpty(Environment.GetEnvironmentVariable("UNITY_NOPROXY")))
                Logger.LogInfo("Game loopback transport uses launch-time Unity proxy bypass");
            AppDomain.CurrentDomain.ProcessExit += (_, _) =>
            {
                DisposeGameRelay();
                PlatformControlHttp.Shutdown();
            };
            EnsureRecoveryHost();
            InitializeCredentials();
            InitializeBranding();

            Type introUI = typeof(IntroUI);
            MethodInfo awake = introUI?.GetGameMethod(
                "Awake",
                BindingFlags.Instance | BindingFlags.NonPublic,
                null,
                Type.EmptyTypes,
                null);
            if (awake == null || awake.ReturnType != typeof(void))
            {
                throw new MissingMethodException("IntroUI.Awake() was not found (client version mismatch)");
            }
            SendMaintenance = Game.Method<IntroUI>(ui => ui.SendMaintenanceInfo(false));
            CancelMaintenanceTimeout = introUI.GetGameMethod(
                "CancelMaintenanceTimeout", BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic, null, Type.EmptyTypes, null);
            SetIntroState = introUI.GetGameMethod("SetIntroState", BindingFlags.Instance | BindingFlags.NonPublic);
            EnterGame = introUI.GetGameMethod(
                "Enter",
                BindingFlags.Instance | BindingFlags.NonPublic,
                null,
                Type.EmptyTypes,
                null);
            OpenPCLoginPopup = FindOpenPCLoginPopup();
            MethodInfo accessTokenGetter = FindAccessTokenGetter();
            MethodInfo clearPCLocalData = FindClearPCLocalData();
            MethodInfo disposeWebRequest = typeof(UnityWebRequest).GetGameMethod(
                nameof(UnityWebRequest.Dispose),
                BindingFlags.Instance | BindingFlags.Public,
                null,
                Type.EmptyTypes,
                null);
            Type networkManager = typeof(BDNetwork.NetworkManager);
            MethodInfo clientNetworkError = networkManager?.GetGameMethod(
                nameof(BDNetwork.NetworkManager.ClientNetworkError),
                BindingFlags.Instance | BindingFlags.Public);
            MethodInfo exponentialBackOff = networkManager?.GetGameMethod(
                "ExponetialBackOff",
                BindingFlags.Instance | BindingFlags.NonPublic);
            if (SendMaintenance == null || CancelMaintenanceTimeout == null || CancelMaintenanceTimeout.ReturnType != typeof(void) || SetIntroState == null || OpenPCLoginPopup == null ||
                accessTokenGetter == null || clearPCLocalData == null || disposeWebRequest == null ||
                clientNetworkError == null || exponentialBackOff == null || EnterGame == null)
            {
                throw new MissingMethodException("IntroUI authentication transition methods were not found (client version mismatch)");
            }

            // Harmony.Dispose calls UnpatchSelf; these hooks live until process exit.
#pragma warning disable CA2000
            var harmony = new Harmony(PluginId);
#pragma warning restore CA2000
            SessionDiagnostics.Install(harmony);
            EventRequestDiagnostics.Install(harmony);
            EventHubPresentation.Install(harmony);
            harmony.Patch(awake, postfix: new HarmonyMethod(typeof(SessionRecovery), nameof(IntroAwakePostfix)));
            var maintenancePrefix = new HarmonyMethod(typeof(LoginController), nameof(SendMaintenancePrefix))
            {
                after = ["bd2.localidentity"]
            };
            harmony.Patch(SendMaintenance, prefix: maintenancePrefix);
            harmony.Patch(
                accessTokenGetter,
                postfix: new HarmonyMethod(typeof(LoginController), nameof(AccessTokenPostfix)));
            harmony.Patch(
                clearPCLocalData,
                postfix: new HarmonyMethod(typeof(LoginController), nameof(ClearPCLocalDataPostfix)));
            harmony.Patch(
                typeof(UnityWebRequest).GetMethod(nameof(UnityWebRequest.SendWebRequest), Type.EmptyTypes),
                prefix: new HarmonyMethod(typeof(SessionRecovery), nameof(SendWebRequestPrefix)));
            harmony.Patch(
                disposeWebRequest,
                prefix: new HarmonyMethod(typeof(SessionRecovery), nameof(DisposeWebRequestPrefix)));
            harmony.Patch(
                clientNetworkError,
                prefix: new HarmonyMethod(typeof(SessionRecovery), nameof(SuppressNetworkErrorDuringRecovery)));
            harmony.Patch(
                exponentialBackOff,
                prefix: new HarmonyMethod(typeof(SessionRecovery), nameof(ExponentialBackoffPrefix)));
            harmony.Patch(
                SetIntroState,
                postfix: new HarmonyMethod(typeof(SessionRecovery), nameof(SetIntroStatePostfix)));
            Logger.LogInfo("Server-authoritative Discord and Google login UI patch installed");
        }
        catch (Exception ex)
        {
            Logger.LogError("Login UI patch failed: " + ex);
        }
    }

}
