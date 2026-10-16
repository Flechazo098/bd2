using System;
using System.Reflection;
using BD2.GameNames;
using HarmonyLib;
using UnityEngine;

namespace Bd2LocalIdentity;

internal static class NoticePresentation
{
    internal static void Install(Harmony harmony)
    {
        const BindingFlags declared = BindingFlags.Instance | BindingFlags.Public | BindingFlags.DeclaredOnly;
        MethodInfo click = typeof(NewsUI).GetGameMethod(nameof(NewsUI.OnClickUI), declared, null, [typeof(GameObject)], null);
        MethodInfo close = typeof(NewsUI).GetGameMethod(nameof(NewsUI.CloseUI), declared, null, Type.EmptyTypes, null);
        MethodInfo refresh = typeof(NewsPopupUI).GetGameMethod(nameof(NewsPopupUI.Refresh), declared, null, Type.EmptyTypes, null);
        if (click?.DeclaringType != typeof(NewsUI) || close?.DeclaringType != typeof(NewsUI) ||
            refresh?.DeclaringType != typeof(NewsPopupUI) || click.ReturnType != typeof(void) ||
            close.ReturnType != typeof(void) || refresh.ReturnType != typeof(void))
            throw new MissingMethodException("Declared news presentation methods were not found");
        harmony.Patch(click,
            prefix: new HarmonyMethod(typeof(NoticePresentation), nameof(ClickPrefix)));
        harmony.Patch(close,
            prefix: new HarmonyMethod(typeof(NoticePresentation), nameof(ClosePrefix)));
        harmony.Patch(refresh,
            prefix: new HarmonyMethod(typeof(NoticePresentation), nameof(RefreshPrefix)));
    }

    private static bool ClickPrefix(UIBase __instance, GameObject __0)
    {
        if (__instance is not NewsUI news || !news.IsNewsPopupUI || !news.IsClickedBackButton(__0)) return true;
        news.CloseUI();
        return false;
    }

    private static void ClosePrefix(UIBase __instance)
    {
        if (__instance is not NewsUI news || !news.IsNewsPopupUI || !news.CanCloseUI()) return;
        news.IsNewsPopupUI = false;
        UIManager.AddUI<NewsPopupUI>(popup => popup.Refresh());
    }

    private static void RefreshPrefix(NewsPopupUI __instance) => __instance.NoticeDBInfoList.Clear();
}
