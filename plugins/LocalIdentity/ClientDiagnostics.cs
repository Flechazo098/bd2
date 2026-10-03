using System;
using BD2.GameNames;
using static BD2.GameNames.Game;
using System.Reflection;
using BepInEx.Logging;
using HarmonyLib;
using UnityEngine;
using System.Diagnostics;
using System.Threading;

namespace Bd2LocalIdentity;

internal static class ClientDiagnostics
{
    private static ManualLogSource Log;
    internal static void Initialize(ManualLogSource logger) => Log = logger;

    internal static void InstallMaintenanceTimeoutGuard(Harmony harmony, Type introUI)
    {
        MethodInfo finishMaintenance = introUI?.GetGameMethod(
            nameof(IntroUI.OnFinishMaintenanceRequest),
            BindingFlags.Instance | BindingFlags.Public,
            null,
            Type.EmptyTypes,
            null);
        MethodInfo falseTimeoutTelemetry = introUI?.GetGameMethod(
            "CheckMaintenanceTimeout",
            BindingFlags.Instance | BindingFlags.NonPublic,
            null,
            Type.EmptyTypes,
            null);
        if (finishMaintenance == null || falseTimeoutTelemetry == null)
        {
            throw new MissingMethodException("IntroUI maintenance timeout methods were not found");
        }
        harmony.Patch(
            finishMaintenance,
            postfix: new HarmonyMethod(typeof(ClientDiagnostics), nameof(OnFinishMaintenancePostfix)));
        harmony.Patch(
            falseTimeoutTelemetry,
            prefix: new HarmonyMethod(typeof(ClientDiagnostics), nameof(SkipLocalTimeoutTelemetry)));
    }

    private static void OnFinishMaintenancePostfix(object __instance)
    {
        // CancelMaintenanceTimeout cancels the active CTS and then
        // immediately stores a fresh CTS. Depending on async scheduling, the
        // timeout task can capture that fresh token after the successful
        // response and emit a false 10-second timeout. Cancel the replacement
        // token only after OnFinishMaintenanceRequest has completed.
        FieldInfo timeout = __instance.GetType().GetGameField(
            "_maintenanceTimeoutCts",
            BindingFlags.Instance | BindingFlags.NonPublic);
        CancellationTokenSource source = timeout?.GetValue(__instance) as CancellationTokenSource;
        source?.Cancel();
        Log?.LogInfo("Maintenance timeout guard cancelled after successful response");
    }

    private static bool SkipLocalTimeoutTelemetry()
    {
        // This method only emits intro_server_info_timeout telemetry after ten
        // seconds. Network request failures have their own callbacks. In
        // In the configured client it can outlive a successful local maintenance response.
        return false;
    }

    internal static void InstallDatabaseDiagnostics(Harmony harmony)
    {
        Type rawDataManager = typeof(RawDataManager);
        MethodInfo dbLoad = rawDataManager?.GetGameMethod(
            nameof(RawDataManager.DBLoad),
            BindingFlags.Instance | BindingFlags.Public);
        if (dbLoad == null)
        {
            throw new MissingMethodException("RawDataManager.DBLoad was not found");
        }

        harmony.Patch(
            dbLoad,
            prefix: new HarmonyMethod(typeof(ClientDiagnostics), nameof(DBLoadPrefix)));

        Type clientLocalInfo = typeof(Proto.Local.ClientLocalInfo);
        MethodInfo loadDB = clientLocalInfo?.GetGameMethod(
            nameof(Proto.Local.ClientLocalInfo.LoadDB),
            BindingFlags.Static | BindingFlags.Public);
        if (loadDB == null)
        {
            throw new MissingMethodException("ClientLocalInfo.LoadDB was not found");
        }

        harmony.Patch(
            loadDB,
            prefix: new HarmonyMethod(typeof(ClientDiagnostics), nameof(ClientLocalLoadPrefix)),
            postfix: new HarmonyMethod(typeof(ClientDiagnostics), nameof(ClientLocalLoadPostfix)),
            finalizer: new HarmonyMethod(typeof(ClientDiagnostics), nameof(ClientLocalLoadFinalizer)));

        Log?.LogInfo("Database diagnostics active");
    }

    private static void DBLoadPrefix(RawDataManager.DBOption __0, ref Action __1)
    {
        string dbName = __0.dbName ?? "<unknown>";
        Action original = __1;
        Stopwatch elapsed = Stopwatch.StartNew();
        Log?.LogInfo("DBLoad start: " + dbName);
        __1 = delegate
        {
            Log?.LogInfo("DBLoad callback enter: " + dbName + " (" + elapsed.ElapsedMilliseconds + " ms)");
            try
            {
                original?.Invoke();
            }
            finally
            {
                Log?.LogInfo("DBLoad callback exit: " + dbName + " (" + elapsed.ElapsedMilliseconds + " ms)");
            }
        };
    }

    private static void ClientLocalLoadPrefix(out Stopwatch __state)
    {
        __state = Stopwatch.StartNew();
        Log?.LogInfo("ClientLocalInfo.LoadDB enter");
    }

    private static void ClientLocalLoadPostfix(Stopwatch __state)
    {
        Log?.LogInfo("ClientLocalInfo.LoadDB exit (" + __state.ElapsedMilliseconds + " ms)");
    }

    private static Exception ClientLocalLoadFinalizer(Exception __exception, Stopwatch __state)
    {
        if (__exception != null)
        {
            Log?.LogError(
                "ClientLocalInfo.LoadDB threw after " + __state.ElapsedMilliseconds + " ms: " + __exception);
        }
        return __exception;
    }

}
