using System;
using System.Diagnostics;
using System.Linq;
using System.Reflection;
using System.Threading;
using BepInEx;
using BepInEx.Logging;
using HarmonyLib;
using UnityEngine;

namespace Bd2LocalIdentity;

[BepInPlugin(Guid, Name, Version)]
public sealed class Plugin : BaseUnityPlugin
{
    public const string Guid = "bd2.localidentity";
    public const string Name = "BD2 Local Identity";
    public const string Version = Bd2Build.Versions.Plugin;
    private static ManualLogSource Log;
    private static ClientRouting Routing;
    private static MethodInfo SwitchToFullScreenMethod;
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
            // The non-SDK branch still needs a local bootstrap identity for
            // MaintenanceInfo. OAuth LoginUI replaces this value after its
            // browser/device transaction completes.
            PlayerPrefs.SetString("AccessToken", "bd2-local-development-user");
            PlayerPrefs.Save();
            Routing = ClientRouting.Load(Logger);
            if (Interlocked.Exchange(ref ShutdownHooksInstalled, 1) == 0)
            {
                AppDomain.CurrentDomain.ProcessExit += delegate { DisposeRouting(); };
                AppDomain.CurrentDomain.DomainUnload += delegate { DisposeRouting(); };
            }
            Type appManager = FindType("AppManager");
            PropertyInfo useSdk = appManager?.GetProperty(
                "ὬὦὠὫὡὥὥὦὠὠὠ",
                BindingFlags.Instance | BindingFlags.Public);
            MethodInfo getter = useSdk?.GetGetMethod();
            if (getter == null || getter.ReturnType != typeof(bool) || getter.GetParameters().Length != 0)
            {
                throw new MissingMethodException("AppManager.UseSdk getter was not found (client version mismatch)");
            }

            MethodInfo prefix = typeof(Plugin).GetMethod(
                nameof(UseSdkPrefix),
                BindingFlags.Static | BindingFlags.NonPublic);
            Harmony harmony = new Harmony(Guid);
            harmony.Patch(getter, prefix: new HarmonyMethod(prefix));

            Type introUI = FindType("IntroUI");
            InstallClientRouting(harmony, introUI);
            TryInstall("startup fullscreen", () => InstallStartupFullscreen(harmony, appManager));
            TryInstall("performance overlay suppression", () => InstallPerformanceOverlaySuppression(harmony));
            TryInstall("maintenance timeout guard", () => InstallMaintenanceTimeoutGuard(harmony, introUI));
            TryInstall("age-gate persistence", () => InstallAgeGatePersistence(harmony));
            TryInstall("local purchase bypass", () => InstallLocalPurchaseBypass(harmony));
            TryInstall("database diagnostics", () => InstallDatabaseDiagnostics(harmony));
            Logger.LogInfo("Local identity active: AppManager.UseSdk => false");
        }
        catch (Exception ex)
        {
            Logger.LogError("Local identity patch failed: " + ex);
        }
    }

    private void OnApplicationQuit()
    {
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

    private static void InstallStartupFullscreen(Harmony harmony, Type appManager)
    {
        MethodInfo initializeResolution = appManager?.GetMethod(
            "ὪὣὬὤὪὥὣὨὤὦὧ",
            BindingFlags.Instance | BindingFlags.NonPublic,
            null,
            Type.EmptyTypes,
            null);
        SwitchToFullScreenMethod = appManager?.GetMethod(
            "SwitchToFullScreen",
            BindingFlags.Instance | BindingFlags.Public,
            null,
            new[] { typeof(bool) },
            null);
        if (initializeResolution == null || SwitchToFullScreenMethod == null)
        {
            throw new MissingMethodException("AppManager fullscreen methods were not found (client version mismatch)");
        }
        harmony.Patch(
            initializeResolution,
            postfix: new HarmonyMethod(typeof(Plugin), nameof(InitializeResolutionPostfix)));
        Log?.LogInfo("Native startup fullscreen patch installed");
    }

    private static void InitializeResolutionPostfix(object __instance)
    {
        try
        {
            // Use the game's own FullScreenWindow path so the behaviour is
            // identical on Windows and macOS. This runs once during startup;
            // later user-initiated switches to windowed mode remain intact.
            SwitchToFullScreenMethod?.Invoke(__instance, new object[] { false });
        }
        catch (Exception ex)
        {
            Log?.LogError("Could not apply native startup fullscreen: " + ex);
        }
    }

    private static void InstallPerformanceOverlaySuppression(Harmony harmony)
    {
        Type fpsCheck = FindType("FPS_Check");
        MethodInfo start = fpsCheck?.GetMethod(
            "StartFPS",
            BindingFlags.Instance | BindingFlags.Public,
            null,
            Type.EmptyTypes,
            null);
        MethodInfo render = fpsCheck?.GetMethod(
            "ὬὡὡὬὢὭὦὣὥὬὯ",
            BindingFlags.Instance | BindingFlags.NonPublic,
            null,
            Type.EmptyTypes,
            null);
        MethodInfo onEnable = fpsCheck?.GetMethod(
            "OnEnable",
            BindingFlags.Instance | BindingFlags.NonPublic,
            null,
            Type.EmptyTypes,
            null);
        MethodInfo onInitialize = fpsCheck?.GetMethod(
            "OnInitialize",
            BindingFlags.Instance | BindingFlags.Public,
            null,
            Type.EmptyTypes,
            null);
        if (start == null || render == null || onEnable == null || onInitialize == null)
        {
            throw new MissingMethodException("FPS_Check overlay methods were not found (client version mismatch)");
        }

        HarmonyMethod skip = new HarmonyMethod(typeof(Plugin), nameof(SkipPerformanceOverlay));
        HarmonyMethod hide = new HarmonyMethod(typeof(Plugin), nameof(HidePerformanceOverlayPostfix));
        harmony.Patch(start, prefix: skip);
        harmony.Patch(render, prefix: skip);
        harmony.Patch(onEnable, postfix: hide);
        harmony.Patch(onInitialize, postfix: hide);
        Log?.LogInfo("FPS_Check performance overlay disabled");
    }

    private static bool SkipPerformanceOverlay(object __instance)
    {
        HidePerformanceOverlay(__instance);
        return false;
    }

    private static void HidePerformanceOverlayPostfix(object __instance)
    {
        HidePerformanceOverlay(__instance);
    }

    private static void HidePerformanceOverlay(object instance)
    {
        if (instance == null)
        {
            return;
        }
        try
        {
            MethodInfo disposeRecorders = instance.GetType().GetMethod(
                "DisposeRecorders",
                BindingFlags.Instance | BindingFlags.Public,
                null,
                Type.EmptyTypes,
                null);
            disposeRecorders?.Invoke(instance, null);

            FieldInfo textField = instance.GetType().GetField(
                "_text",
                BindingFlags.Instance | BindingFlags.Public);
            object text = textField?.GetValue(instance);
            PropertyInfo textProperty = text?.GetType().GetProperty(
                "text",
                BindingFlags.Instance | BindingFlags.Public);
            if (textProperty?.CanWrite == true)
            {
                textProperty.SetValue(text, string.Empty);
            }
            if (text is Component component && component != null)
            {
                component.gameObject.SetActive(false);
            }
        }
        catch (Exception ex)
        {
            Log?.LogError("Could not suppress FPS_Check overlay: " + ex);
        }
    }

    private static void InstallClientRouting(Harmony harmony, Type introUI)
    {
        Type serverURLInfo = FindType("ὫὡὩὤὣὨὯὭὥὠὩ") ?? FindType("BDNetwork.ServerURLInfo");
        MethodInfo load = serverURLInfo?.GetMethod(
            "ὣὪὦὮὦὠὪὯὧὯὭ",
            BindingFlags.Static | BindingFlags.Public,
            null,
            Type.EmptyTypes,
            null) ?? serverURLInfo?.GetMethod(
            "Load",
            BindingFlags.Static | BindingFlags.Public,
            null,
            Type.EmptyTypes,
            null);
        MethodInfo sendMaintenance = introUI?.GetMethod(
            "SendMaintenanceInfo",
            BindingFlags.Instance | BindingFlags.Public,
            null,
            new[] { typeof(bool) },
            null);
        Type commonPacket = FindType("ὣὡὧὡὦὣὣὬὨὪὫ");
        MethodInfo makeCDNInfo = commonPacket?.GetMethod(
            "ὥὢὨὡὪὨὥὥὪὩὠ",
            BindingFlags.Static | BindingFlags.Public,
            null,
            new[] { typeof(string) },
            null);
        if (load == null || sendMaintenance == null || makeCDNInfo == null)
        {
            throw new MissingMethodException("2.35.10 server/resource routing methods were not found");
        }
        harmony.Patch(load, postfix: new HarmonyMethod(typeof(Plugin), nameof(ApplyServerOrigin)));
        harmony.Patch(sendMaintenance, prefix: new HarmonyMethod(typeof(Plugin), nameof(ApplyServerOrigin)));
        harmony.Patch(makeCDNInfo, postfix: new HarmonyMethod(typeof(Plugin), nameof(ApplyResources)));
        Log?.LogInfo("Server origin and native CdnInfo routing patches installed");
    }

    private static void ApplyServerOrigin()
    {
        ClientRouting routing = Routing;
        if (routing == null)
        {
            Log?.LogError("Client routing is unavailable while applying the server origin");
            return;
        }
        routing.ApplyServerOrigin();
    }

    private static void ApplyResources()
    {
        ClientRouting routing = Routing;
        if (routing == null)
        {
            Log?.LogError("Client routing is unavailable while applying resources");
            return;
        }
        routing.ApplyResources();
    }

    private static void InstallMaintenanceTimeoutGuard(Harmony harmony, Type introUI)
    {
        MethodInfo finishMaintenance = introUI?.GetMethod(
            "OnFinishMaintenanceRequest",
            BindingFlags.Instance | BindingFlags.Public,
            null,
            Type.EmptyTypes,
            null);
        MethodInfo falseTimeoutTelemetry = introUI?.GetMethod(
            "ὡὡὤὧὫὥὬὯὣὠὬ",
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
            postfix: new HarmonyMethod(typeof(Plugin), nameof(OnFinishMaintenancePostfix)));
        harmony.Patch(
            falseTimeoutTelemetry,
            prefix: new HarmonyMethod(typeof(Plugin), nameof(SkipLocalTimeoutTelemetry)));
    }

    private static void OnFinishMaintenancePostfix(object __instance)
    {
        // CancelMaintenanceTimeout cancels the active CTS and then
        // immediately stores a fresh CTS. Depending on async scheduling, the
        // timeout task can capture that fresh token after the successful
        // response and emit a false 10-second timeout. Cancel the replacement
        // token only after OnFinishMaintenanceRequest has completed.
        FieldInfo timeout = __instance.GetType().GetField(
            "ὫὥὨὨὠὯὥὭὨὨὪ",
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

    private static void InstallAgeGatePersistence(Harmony harmony)
    {
        // LoginUserResponse field 13 is the client's sole gate for opening
        // AgeGatePopupUI.  Retain the original first-run UI and request; only
        // change a later LoginUser parse after its successful local state has
        // been read from disk.
        Type commonPacket = FindType("ὣὡὧὡὦὣὣὬὨὪὫ");
        MethodInfo updateAgeGate = commonPacket?.GetMethod(
            "ὪὯὭὣὨὡὬὪὭὨὡ",
            BindingFlags.Static | BindingFlags.Public,
            null,
            new[] { typeof(bool), typeof(int), typeof(int), typeof(int), typeof(Action) },
            null);
        if (updateAgeGate == null)
        {
            throw new MissingMethodException("CommonPacket.SendUpdateAgeGateRequest(bool, int, int, int, Action) was not found");
        }
        Type loginUserResponse = FindType("Proto.Net.LoginUserResponse");
        MethodInfo needsAgeVerificationSetter = loginUserResponse?.GetProperty(
            "NeedsAgeVerification",
            BindingFlags.Instance | BindingFlags.Public)?.GetSetMethod();
        if (needsAgeVerificationSetter == null)
        {
            throw new MissingMethodException("LoginUserResponse.NeedsAgeVerification setter was not found");
        }
        harmony.Patch(
            updateAgeGate,
            prefix: new HarmonyMethod(typeof(Plugin), nameof(UpdateAgeGateRequestPrefix)));
        harmony.Patch(
            needsAgeVerificationSetter,
            prefix: new HarmonyMethod(typeof(Plugin), nameof(NeedsAgeVerificationSetterPrefix)));

        Log?.LogInfo("Local age-gate confirmation persistence active (confirmed=" + AgeGateState.IsConfirmed() + ")");
    }

    private static void NeedsAgeVerificationSetterPrefix(ref bool value)
    {
        if (value && AgeGateState.IsConfirmed())
        {
            value = false;
            Log?.LogInfo("Used persisted local age-gate confirmation for LoginUser");
        }
    }

    private static void UpdateAgeGateRequestPrefix(ref Action __4)
    {
        // CommonPacket invokes this callback only after it has parsed the
        // empty UpdateAgeGateResponse and accepted errorType == 0.  Wrapping
        // it therefore never records failed/cancelled submissions.
        Action continuation = __4;
        __4 = delegate
        {
            try
            {
                AgeGateState.MarkConfirmed();
                Log?.LogInfo("Stored successful local age-gate confirmation");
            }
            catch (Exception ex)
            {
                // Preserve the original continuation: inability to persist
                // should not break a successfully completed first login.
                Log?.LogWarning("Could not persist local age-gate confirmation: " + ex.Message);
            }
            continuation?.Invoke();
        };
    }

    private static void InstallLocalPurchaseBypass(Harmony harmony)
    {
        Type platformRuler = FindType("ὧὩὬὦὤὥὦὢὤὠὡ");
        MethodInfo getProducts = platformRuler?
            .GetMethods(BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic)
            .FirstOrDefault(method =>
                method.Name == "ὮὨὡὩὠὭὧὦὭὥὮ" &&
                method.GetParameters().Length == 6 &&
                method.GetParameters()[0].ParameterType == typeof(string[]) &&
                method.GetParameters()[3].ParameterType == typeof(Action) &&
                method.GetParameters()[4].ParameterType == typeof(Action<int, int, string>) &&
                method.GetParameters()[5].ParameterType == typeof(bool));
        if (getProducts == null)
        {
            throw new MissingMethodException("PlatformRuler.GetProductAsync was not found");
        }
        harmony.Patch(
            getProducts,
            prefix: new HarmonyMethod(typeof(Plugin), nameof(GetProductsPrefix)));

        Type platformManager = FindType("gamfs.Platform.PlatformManager");
        MethodInfo purchase = platformManager?.GetMethods(BindingFlags.Instance | BindingFlags.Public)
            .FirstOrDefault(method => method.Name == "Purchase" &&
                method.GetParameters().Length == 3 &&
                method.GetParameters()[0].ParameterType == typeof(string) &&
                method.GetParameters()[2].ParameterType == typeof(Action));
        MethodInfo finishPurchase = platformManager?.GetMethods(BindingFlags.Instance | BindingFlags.Public)
            .FirstOrDefault(method => method.Name == "FinishPurchase" &&
                method.GetParameters().Length == 2 &&
                method.GetParameters()[0].ParameterType == typeof(string) &&
                method.GetParameters()[1].ParameterType == typeof(long));
        if (purchase == null || finishPurchase == null)
            throw new MissingMethodException("PlatformManager purchase methods were not found");
        harmony.Patch(purchase, prefix: new HarmonyMethod(
            typeof(Plugin), nameof(LocalPurchasePrefix)));
        harmony.Patch(finishPurchase, prefix: new HarmonyMethod(
            typeof(Plugin), nameof(FinishLocalPurchasePrefix)));
        Log?.LogInfo("Local purchase price lookup disabled");
        Log?.LogInfo("Infinite reroll confirmation is local/free; all other paid purchases are blocked");
    }

    private static bool GetProductsPrefix(Action __3)
    {
        // A private server has no Neon/GPG commerce identity. Treat price
        // prefetch as complete so startup can continue without contacting the
        // production payment API. No purchase result or currency is forged.
        __3?.Invoke();
        return false;
    }

    private static bool LocalPurchasePrefix(string __0, object __1, Action __2)
    {
        // Product 9100033 is the infinite-reroll confirmation. The
        // local server grants the last preview through CashShopBuy without
        // contacting Neon/GPG. No other real-money product is authorized.
        if (__0 == "brd2_limited_pack_660" || __0 == "brd2_limited_pack_660_ios")
        {
            Type purchaseData = FindType("ὦὯὢὡὥὨὦὯὤὯὩ");
            object result = Activator.CreateInstance(purchaseData, new object[]
            {
                __0,
                1L,
                "bd2-local-free-infinite",
                "bd2-local-free-receipt"
            });
            Log?.LogInfo("Approved local/free infinite-reroll confirmation");
            (__1 as Delegate)?.DynamicInvoke(result);
            return false;
        }
        Log?.LogWarning("Blocked unsupported paid product: " + (__0 ?? "<null>"));
        __2?.Invoke();
        return false;
    }

    private static bool FinishLocalPurchasePrefix(string __0)
    {
        if (__0 == "bd2-local-free-infinite")
        {
            Log?.LogInfo("Finished local/free infinite-reroll confirmation");
            return false;
        }
        return true;
    }

    private static void InstallDatabaseDiagnostics(Harmony harmony)
    {
        Type rawDataManager = FindType("RawDataManager");
        MethodInfo dbLoad = rawDataManager?.GetMethod(
            "DBLoad",
            BindingFlags.Instance | BindingFlags.Public);
        if (dbLoad == null)
        {
            throw new MissingMethodException("RawDataManager.DBLoad was not found");
        }

        harmony.Patch(
            dbLoad,
            prefix: new HarmonyMethod(typeof(Plugin), nameof(DBLoadPrefix)));

        Type clientLocalInfo = FindType("Proto.Local.ClientLocalInfo");
        MethodInfo loadDB = clientLocalInfo?.GetMethod(
            "LoadDB",
            BindingFlags.Static | BindingFlags.Public);
        if (loadDB == null)
        {
            throw new MissingMethodException("ClientLocalInfo.LoadDB was not found");
        }

        harmony.Patch(
            loadDB,
            prefix: new HarmonyMethod(typeof(Plugin), nameof(ClientLocalLoadPrefix)),
            postfix: new HarmonyMethod(typeof(Plugin), nameof(ClientLocalLoadPostfix)),
            finalizer: new HarmonyMethod(typeof(Plugin), nameof(ClientLocalLoadFinalizer)));

        Log?.LogInfo("Database diagnostics active");
    }

    private static void DBLoadPrefix(object __0, ref Action __1)
    {
        string dbName = ReadFirstStringField(__0) ?? "<unknown>";
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

    private static string ReadFirstStringField(object value)
    {
        if (value == null)
        {
            return null;
        }
        FieldInfo field = value.GetType()
            .GetFields(BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic)
            .FirstOrDefault(candidate => candidate.FieldType == typeof(string));
        return field?.GetValue(value) as string;
    }

    internal static Type FindType(string name)
    {
        Assembly assembly = Assembly.Load("Assembly-CSharp");
        Type type = assembly?.GetType(name);
        if (type != null)
        {
            return type;
        }
        foreach (Assembly loaded in AppDomain.CurrentDomain.GetAssemblies())
        {
            type = loaded.GetType(name);
            if (type != null)
            {
                return type;
            }
        }
        return null;
    }
}
