using System;
using System.Collections.Generic;
using System.Reflection;
using BD2.GameNames;
using BepInEx.Logging;
using HarmonyLib;

namespace Bd2CashShop;

// Native reward recursion leaves the previous skin window registered. AddUI
// then rejects the next window with the same name without invoking its callback.
internal static class PrestigeSkinRewardPresentation
{
    private static ManualLogSource log;

    internal static void Install(Harmony harmony, ManualLogSource logger)
    {
        log = logger;
        var target = typeof(PrestigeSkinGetUI).GetGameMethod("RecursiveShowPrestigeSkin",
            BindingFlags.Public | BindingFlags.NonPublic | BindingFlags.Static, null,
            new[] { typeof(Queue<ItemBaseInfo>), typeof(Action) }, null);
        if (target == null) throw new MissingMethodException("PrestigeSkinGetUI.RecursiveShowPrestigeSkin");
        harmony.Patch(target, prefix: new HarmonyMethod(typeof(PrestigeSkinRewardPresentation), nameof(ClosePreviousWindows)));
    }

    private static void ClosePreviousWindows(Queue<ItemBaseInfo> __0)
    {
        // Snapshot both aliases before removing either window. CloseProcess
        // releases assets and clears the end-step callback; it does not invoke it.
        var prestige = UIManager.GetUI<PrestigeSkinGetUI>();
        var special = UIManager.GetUI("SpecialSkinGetUI") as PrestigeSkinGetUI;
        int closed = 0;
        if (prestige != null)
        {
            prestige.CloseForceWithoutAnimation();
            closed++;
        }
        if (special != null && special != prestige)
        {
            special.CloseForceWithoutAnimation();
            closed++;
        }
        log?.LogInfo("Prestige skin reward queue: remaining=" + (__0?.Count ?? 0) + " closed_windows=" + closed);
    }
}
