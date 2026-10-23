using System;
using System.Reflection;
using BepInEx.Logging;
using HarmonyLib;
using UnityEngine;

namespace Bd2LocalIdentity;

internal static class DebuggerErrorNotification
{
    internal static void Install(Harmony harmony, ManualLogSource logger)
    {
        Type notifier = AccessTools.TypeByName("SRDebugger.UI.Other.ErrorNotifier");
        MethodInfo show = notifier?.GetMethod(
            "ShowErrorWarning", BindingFlags.Instance | BindingFlags.Public,
            null, Type.EmptyTypes, null);
        MethodInfo awake = notifier?.GetMethod(
            "Awake", BindingFlags.Instance | BindingFlags.NonPublic,
            null, Type.EmptyTypes, null);
        if (notifier == null || !typeof(MonoBehaviour).IsAssignableFrom(notifier) ||
            show == null || show.ReturnType != typeof(void) ||
            awake == null || awake.ReturnType != typeof(void))
        {
            throw new MissingMethodException("SRDebugger ErrorNotifier methods were not found (client version mismatch)");
        }

        harmony.Patch(show, prefix: new HarmonyMethod(typeof(DebuggerErrorNotification), nameof(SuppressWarning)));
        harmony.Patch(awake, postfix: new HarmonyMethod(typeof(DebuggerErrorNotification), nameof(HideNotifier)));
        logger.LogInfo("SRDebugger blinking error notification disabled; console logging remains active");
    }

    private static bool SuppressWarning(object __instance)
    {
        HideNotifier(__instance);
        return false;
    }

    private static void HideNotifier(object __instance)
    {
        if (__instance is Component component && component != null)
        {
            component.gameObject.SetActive(false);
        }
    }
}
