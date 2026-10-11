using System;
using System.Reflection;
using BD2.GameNames;
using BepInEx.Logging;
using HarmonyLib;
using UnityEngine.Analytics;

namespace Bd2LocalIdentity;

internal static class ClientNetworkPolicy
{
    private static Uri ConfiguredServerRoot { get; set; }

    internal static void Install(Harmony harmony, Uri serverRoot, ManualLogSource log)
    {
        ConfiguredServerRoot = serverRoot ?? throw new ArgumentNullException(nameof(serverRoot));

        Patch(harmony, PrivateMethod<gamfs.Platform.PlatformRuler>("InitNeonAsync"), nameof(SkipBackgroundNetwork));
        Patch(harmony, Game.Method<gamfs.Platform.AppsFlyerRuler>(sdk => sdk.Initialize(null, null, null, false)), nameof(SkipBackgroundNetwork));
        Patch(harmony, Game.Method(() => UserDeviceInfo.RefreshDeviceInfo(null, null)), nameof(DeviceLookupPrefix));
        Patch(harmony, Game.Method<gamfs.LiveLogger>(logger => logger.SendEvent(null, null)), nameof(SkipBackgroundNetwork));
        Patch(harmony, Game.Method<gamfs.LiveLogger>(logger => logger.WaitEvent(null, null)), nameof(SkipBackgroundNetwork));
        Patch(harmony, Game.Method<gamfs.LiveLogger>(logger => logger.RunAndSave(0, null)), nameof(SkipBackgroundNetwork));
        Patch(harmony, PrivateMethod<gamfs.LiveLogger>("StartLogger"), nameof(SkipBackgroundNetwork));
        Patch(harmony, Game.Method<CrashReporterManager>(reporter => reporter.RefreshCrashReportInfo()), nameof(SkipBackgroundNetwork));
        Patch(harmony, Game.Method<CrashReporter>(reporter => reporter.AnalyticsImplement(default)), nameof(SkipBackgroundNetwork));
        Patch(harmony, Game.Method<CrashReporter>(reporter => reporter.SetCrashReporterOnlineInfo(null)), nameof(SkipBackgroundNetwork));
        Patch(harmony, Game.Method<CrashReporter>(reporter => reporter.SendUnreportedCrashReport()), nameof(NoCrashUploadPrefix));
        Patch(harmony, Game.Method<CrashReporter>(reporter => reporter.MakeInitial(null, default, null, null, null, default)), nameof(LocalCrashModePrefix));

        Analytics.initializeOnStartup = false;
        Analytics.deviceStatsEnabled = false;
        Analytics.limitUserTracking = true;
        Analytics.enabled = false;
        log?.LogInfo("Configured-server background network policy installed: Neon, attribution, device lookup, cloud analytics and remote logs disabled; crash logs remain local");
    }

    private static MethodInfo PrivateMethod<T>(string name) =>
        typeof(T).GetGameMethod(name, BindingFlags.Instance | BindingFlags.NonPublic, null, Type.EmptyTypes, null)
        ?? throw new MissingMethodException(typeof(T).FullName, name);

    private static void Patch(Harmony harmony, MethodInfo target, string prefix)
    {
        if (target == null) throw new MissingMethodException("Client background network method was not found");
        harmony.Patch(target, prefix: new HarmonyMethod(typeof(ClientNetworkPolicy), prefix));
    }

    private static bool SkipBackgroundNetwork() => ConfiguredServerRoot == null;

    private static bool DeviceLookupPrefix(Action __1)
    {
        if (ConfiguredServerRoot == null) return true;
        // SplashUI already uses this callback to select the OS language on lookup failure.
        __1?.Invoke();
        return false;
    }

    private static bool NoCrashUploadPrefix(ref bool __result)
    {
        if (ConfiguredServerRoot == null) return true;
        __result = false;
        return false;
    }

    private static void LocalCrashModePrefix(ref eCrashWriteType __1)
    {
        if (ConfiguredServerRoot != null &&
            (__1 == eCrashWriteType.EWRITESERVER || __1 == eCrashWriteType.EWRITEGAv3))
            __1 = eCrashWriteType.EWRITEFILE;
    }
}
