using System;
using BD2.GameNames;
using static BD2.GameNames.Game;
using System.Reflection;
using BepInEx.Logging;
using HarmonyLib;
using UnityEngine;
using System.Linq;

namespace Bd2LocalIdentity;

internal static class LocalAccountPolicy
{
    private static ManualLogSource Log;
    internal static void Initialize(ManualLogSource logger) => Log = logger;

    internal static void InstallAgeGatePersistence(Harmony harmony)
    {
        // LoginUserResponse field 13 is the client's sole gate for opening
        // AgeGatePopupUI.  Retain the original first-run UI and request; only
        // change a later LoginUser parse after its successful local state has
        // been read from disk.
        Type commonPacket = typeof(BDNetwork.CommonPacket);
        MethodInfo updateAgeGate = commonPacket?.GetGameMethod(
            nameof(BDNetwork.CommonPacket.SendUpdateAgeGateRequest),
            BindingFlags.Static | BindingFlags.Public,
            null,
            new[] { typeof(bool), typeof(int), typeof(int), typeof(int), typeof(Action) },
            null);
        if (updateAgeGate == null)
        {
            throw new MissingMethodException("CommonPacket.SendUpdateAgeGateRequest(bool, int, int, int, Action) was not found");
        }
        Type loginUserResponse = typeof(Proto.Net.LoginUserResponse);
        MethodInfo needsAgeVerificationSetter = loginUserResponse?.GetGameProperty(
            nameof(Proto.Net.LoginUserResponse.NeedsAgeVerification),
            BindingFlags.Instance | BindingFlags.Public)?.GetSetMethod();
        if (needsAgeVerificationSetter == null)
        {
            throw new MissingMethodException("LoginUserResponse.NeedsAgeVerification setter was not found");
        }
        harmony.Patch(
            updateAgeGate,
            prefix: new HarmonyMethod(typeof(LocalAccountPolicy), nameof(UpdateAgeGateRequestPrefix)));
        harmony.Patch(
            needsAgeVerificationSetter,
            prefix: new HarmonyMethod(typeof(LocalAccountPolicy), nameof(NeedsAgeVerificationSetterPrefix)));

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

    internal static void InstallLocalPurchaseBypass(Harmony harmony)
    {
        Type platformRuler = typeof(gamfs.Platform.PlatformRuler);
        MethodInfo getProducts = platformRuler?
            .GetMethods(BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic)
            .SingleOrDefault(method =>
                method.IsGameMethod(nameof(gamfs.Platform.PlatformRuler.GetProductAsync)) &&
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
            prefix: new HarmonyMethod(typeof(LocalAccountPolicy), nameof(GetProductsPrefix)));

        Type platformManager = typeof(gamfs.Platform.PlatformManager);
        MethodInfo purchase = platformManager?.GetMethods(BindingFlags.Instance | BindingFlags.Public)
            .SingleOrDefault(method => method.IsGameMethod(nameof(gamfs.Platform.PlatformManager.Purchase)) &&
                method.GetParameters().Length == 3 &&
                method.GetParameters()[0].ParameterType == typeof(string) &&
                method.GetParameters()[2].ParameterType == typeof(Action));
        MethodInfo finishPurchase = platformManager?.GetMethods(BindingFlags.Instance | BindingFlags.Public)
            .SingleOrDefault(method => method.IsGameMethod(nameof(gamfs.Platform.PlatformManager.FinishPurchase)) &&
                method.GetParameters().Length == 2 &&
                method.GetParameters()[0].ParameterType == typeof(string) &&
                method.GetParameters()[1].ParameterType == typeof(long));
        if (purchase == null || finishPurchase == null)
            throw new MissingMethodException("PlatformManager purchase methods were not found");
        harmony.Patch(purchase, prefix: new HarmonyMethod(
            typeof(LocalAccountPolicy), nameof(LocalPurchasePrefix)));
        harmony.Patch(finishPurchase, prefix: new HarmonyMethod(
            typeof(LocalAccountPolicy), nameof(FinishLocalPurchasePrefix)));
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
            object result = new gamfs.Platform.PurchaseData(__0, 1L,
                "bd2-local-free-infinite", "bd2-local-free-receipt");
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


}
