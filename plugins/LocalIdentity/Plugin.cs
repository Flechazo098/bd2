using System;
using BD2.GameNames;
using static BD2.GameNames.Game;
using System.Reflection;
using System.Threading;
using BepInEx;
using BepInEx.Logging;
using HarmonyLib;

namespace Bd2LocalIdentity;

[BepInPlugin(PluginId, Name, Version)]
public sealed class Plugin : BaseUnityPlugin
{
    public const string PluginId = "bd2.localidentity";
    public const string Name = "BD2 Local Identity";
    public const string Version = Bd2Build.Versions.Plugin;
    private static ManualLogSource Log;
    private static ClientRouting Routing;
    private static int ShutdownHooksInstalled;

    internal static void LogWarning(string message)
    {
        Log?.LogWarning(message);
    }

    private void Awake()
    {
        try
        {
            Log = Logger;
            // These hooks live until process exit; disposing Harmony would unpatch them.
#pragma warning disable CA2000
            var harmony = new Harmony(PluginId);
#pragma warning restore CA2000
            ClientInstanceIsolation.Install(harmony, Logger);
            TryInstall("Unity log archival", () => UnityLogArchive.ArchivePrevious(Logger));
            StartCoroutine(ClientInstanceIsolation.InitializeCache());
            Routing = ClientRouting.Load(Logger);
            TryInstall("background network policy", () => ClientNetworkPolicy.Install(harmony, Routing.ServerRoot, Logger));
            if (Interlocked.Exchange(ref ShutdownHooksInstalled, 1) == 0)
            {
                AppDomain.CurrentDomain.ProcessExit += delegate { DisposeRouting(); };
                AppDomain.CurrentDomain.DomainUnload += delegate { DisposeRouting(); };
            }
            Type appManager = typeof(AppManager);
            MethodInfo getter = Game.Getter<AppManager, bool>(app => app.IsPlatformLogin);
            if (getter == null || getter.ReturnType != typeof(bool) || getter.GetParameters().Length != 0)
            {
                throw new MissingMethodException("AppManager.IsPlatformLogin getter was not found (client version mismatch)");
            }

            MethodInfo prefix = typeof(Plugin).GetGameMethod(
                nameof(UseSdkPrefix),
                BindingFlags.Static | BindingFlags.NonPublic);
            LocalLoginState.Install(harmony, Routing.ServerOrigin, Logger);
            harmony.Patch(getter, prefix: new HarmonyMethod(prefix));
            TryInstall("OS time zone device country", () => SystemTimeZoneRegion.Install(harmony, Logger));

            ClientPresentation.Initialize(Logger);
            LocalAccountPolicy.Initialize(Logger);
            ClientDiagnostics.Initialize(Logger);
            Type introUI = typeof(IntroUI);
            Routing.Install(harmony);
            TryInstall("startup fullscreen", () => ClientPresentation.InstallStartupFullscreen(harmony, appManager));
            TryInstall("performance overlay suppression", () => ClientPresentation.InstallPerformanceOverlaySuppression(harmony));
            TryInstall("maintenance timeout guard", () => ClientDiagnostics.InstallMaintenanceTimeoutGuard(harmony, introUI));
            TryInstall("age-gate persistence", () => LocalAccountPolicy.InstallAgeGatePersistence(harmony));
            TryInstall("database diagnostics", () => ClientDiagnostics.InstallDatabaseDiagnostics(harmony));
            TryInstall("commission navigation completion", () => CommissionNavigation.Install(harmony, Logger));
            TryInstall("field reward position ordering", () => FieldRewardPosition.Install(harmony, Logger));
            TryInstall("event hub presentation", () => EventHubPresentation.Install(harmony, Logger));
            TryInstall("event request diagnostics", () => EventRequestDiagnostics.Install(harmony, Logger));
            Logger.LogInfo("Local identity active: AppManager.IsPlatformLogin => false");
        }
        catch (Exception ex)
        {
            Logger.LogError("Local identity patch failed: " + ex);
        }
    }

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Performance", "CA1822", Justification = "Unity invokes this instance lifecycle callback.")]
    private void OnApplicationQuit()
    {
        ClientInstanceIsolation.SaveOnExit();
        DisposeRouting();
    }

    private static void DisposeRouting()
    {
        ClientRouting routing = Interlocked.Exchange(ref Routing, null);
        routing?.Dispose();
    }

    private void TryInstall(string name, Action install)
    {
        try
        {
            install();
        }
        catch (Exception ex)
        {
            Logger.LogError("Local identity " + name + " patch failed: " + ex);
        }
    }

    private static bool UseSdkPrefix(ref bool __result)
    {
        __result = false;
        return false;
    }


}
