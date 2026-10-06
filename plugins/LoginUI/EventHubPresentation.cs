using System;
using System.Reflection;
using BD2.GameNames;
using Google.Protobuf;
using HarmonyLib;
using Proto.Net;
using gamfs;
using static Bd2LoginUI.LoginRuntime;
using static Bd2LoginUI.SessionRecovery;

namespace Bd2LoginUI;

// An authoritative empty hub response must also remove presentation cached by
// the client. The original receiver returns before doing its normal reset.
internal static class EventHubPresentation
{
    internal static void Install(Harmony harmony)
    {
        MethodInfo receive = typeof(MiniEventHubPacket).GetGameMethod(
            "RecvMiniEventHubInfoResponse", BindingFlags.Static | BindingFlags.NonPublic,
            null, [typeof(IMessage), typeof(int), typeof(int), typeof(Action)], null);
        if (receive == null || receive.ReturnType != typeof(bool))
            throw new MissingMethodException("MiniEventHubPacket.RecvMiniEventHubInfoResponse(IMessage) was not found");
        harmony.Patch(receive, postfix: new HarmonyMethod(typeof(EventHubPresentation), nameof(ReceivePostfix)));
        Log?.LogInfo("Authoritative empty mini-event hub presentation reset installed");
    }

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0031", Justification = "UnityEngine.Object null checks also detect destroyed native objects.")]
    private static void ReceivePostfix(IMessage __0, int __2, bool __result)
    {
        if (ServerRoot == null || !EstablishedGameSession || !__result || __2 > 0 ||
            __0 is not MiniEventHubInfoResponse response || response.MiniEventHubInfo.Count != 0) return;
        try
        {
            // Detach the old manager subscriber before Reset removes its UID.
            // The client's scheduler exposes subscriber removal, but no global
            // mini-hub-only disposal API; do not disturb unrelated game timers.
            foreach (MiniEventHubDBInfo hub in MiniEventHubPacket.MiniEventHubDBInfoes)
                if (hub != null && SG.Schedule != null)
                    SG.Schedule.RemoveMiniEventHubChangeState(hub.Uid, "MiniEventHubManager");
            MiniEventHubPacket.Reset();
            MiniEventHubManager.SetCurrentEventHub();
            // Close only the stale hub window through the normal UI lifecycle.
            // CloseProcess releases its slots and resumes the menu presentation.
            MiniEventMainUI hubUI = UIManager.GetUI<MiniEventMainUI>();
            if (hubUI != null) hubUI.CloseUI();
            // UpdateUI clears its own old subscriptions and closes the button
            // when CurrentEventHubDBInfo is null. SetMiniEventHub would send a
            // new request, so it must not be called while receiving this one.
            foreach (MenuUI_MiniEventHubButton button in UnityEngine.Object.FindObjectsOfType<MenuUI_MiniEventHubButton>(true))
                if (button != null)
                {
                    try { button.UpdateUI(); }
                    catch (Exception error) { Log?.LogWarning("Empty mini-event hub menu refresh failed: " + error.GetType().Name); }
                }
            Log?.LogInfo("Empty mini-event hub response cleared cached hub selection and menu presentation");
        }
        catch (Exception error)
        {
            Log?.LogWarning("Empty mini-event hub presentation reset failed: " + error.GetType().Name);
        }
    }
}
