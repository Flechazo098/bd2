using System;
using BD2.GameNames;
using static BD2.GameNames.Game;
using System.Reflection;
using BepInEx.Logging;
using HarmonyLib;

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
        MethodInfo updateAgeGate = (commonPacket?.GetGameMethod(
            nameof(BDNetwork.CommonPacket.SendUpdateAgeGateRequest),
            BindingFlags.Static | BindingFlags.Public,
            null,
            [typeof(bool), typeof(int), typeof(int), typeof(int), typeof(Action)],
            null)) ?? throw new MissingMethodException("CommonPacket.SendUpdateAgeGateRequest(bool, int, int, int, Action) was not found");
        Type loginUserResponse = typeof(Proto.Net.LoginUserResponse);
        MethodInfo needsAgeVerificationSetter = (loginUserResponse?.GetGameProperty(
            nameof(Proto.Net.LoginUserResponse.NeedsAgeVerification),
            BindingFlags.Instance | BindingFlags.Public)?.GetSetMethod()) ?? throw new MissingMethodException("LoginUserResponse.NeedsAgeVerification setter was not found");
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

}
