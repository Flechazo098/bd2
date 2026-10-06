using System;
using BD2.GameNames;
using static BD2.GameNames.Game;
using System.Reflection;
using BepInEx.Logging;
using HarmonyLib;
using UnityEngine;

namespace Bd2LocalIdentity;

internal static class ClientPresentation
{
    private static ManualLogSource Log;
    private static MethodInfo SwitchToFullScreenMethod;
    internal static void Initialize(ManualLogSource logger) => Log = logger;

    internal static void InstallStartupFullscreen(Harmony harmony, Type appManager)
    {
        MethodInfo initializeResolution = appManager?.GetGameMethod(
            "InitPCResolution",
            BindingFlags.Instance | BindingFlags.NonPublic,
            null,
            Type.EmptyTypes,
            null);
        SwitchToFullScreenMethod = appManager?.GetGameMethod(
            nameof(AppManager.SwitchToFullScreen),
            BindingFlags.Instance | BindingFlags.Public,
            null,
            [typeof(bool)],
            null);
        if (initializeResolution == null || SwitchToFullScreenMethod == null)
        {
            throw new MissingMethodException("AppManager fullscreen methods were not found (client version mismatch)");
        }
        harmony.Patch(
            initializeResolution,
            postfix: new HarmonyMethod(typeof(ClientPresentation), nameof(InitializeResolutionPostfix)));
        Log?.LogInfo("Native startup fullscreen patch installed");
    }

    private static void InitializeResolutionPostfix(object __instance)
    {
        try
        {
            // Use the game's own FullScreenWindow path so the behaviour is
            // identical on Windows and macOS. This runs once during startup;
            // later user-initiated switches to windowed mode remain intact.
            SwitchToFullScreenMethod?.Invoke(__instance, [false]);
        }
        catch (Exception ex)
        {
            Log?.LogError("Could not apply native startup fullscreen: " + ex);
        }
    }

    internal static void InstallPerformanceOverlaySuppression(Harmony harmony)
    {
        Type fpsCheck = typeof(FPS_Check);
        MethodInfo start = fpsCheck?.GetGameMethod(
            nameof(FPS_Check.StartFPS),
            BindingFlags.Instance | BindingFlags.Public,
            null,
            Type.EmptyTypes,
            null);
        MethodInfo render = fpsCheck?.GetGameMethod(
            "TextRender",
            BindingFlags.Instance | BindingFlags.NonPublic,
            null,
            Type.EmptyTypes,
            null);
        MethodInfo onEnable = fpsCheck?.GetGameMethod(
            "OnEnable",
            BindingFlags.Instance | BindingFlags.NonPublic,
            null,
            Type.EmptyTypes,
            null);
        MethodInfo onInitialize = fpsCheck?.GetGameMethod(
            nameof(FPS_Check.OnInitialize),
            BindingFlags.Instance | BindingFlags.Public,
            null,
            Type.EmptyTypes,
            null);
        if (start == null || render == null || onEnable == null || onInitialize == null)
        {
            throw new MissingMethodException("FPS_Check overlay methods were not found (client version mismatch)");
        }

        var skip = new HarmonyMethod(typeof(ClientPresentation), nameof(SkipPerformanceOverlay));
        var hide = new HarmonyMethod(typeof(ClientPresentation), nameof(HidePerformanceOverlayPostfix));
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
            MethodInfo disposeRecorders = instance.GetType().GetGameMethod(
                nameof(FPS_Check.DisposeRecorders),
                BindingFlags.Instance | BindingFlags.Public,
                null,
                Type.EmptyTypes,
                null);
            disposeRecorders?.Invoke(instance, null);

            FieldInfo textField = instance.GetType().GetGameField(
                "_text",
                BindingFlags.Instance | BindingFlags.Public);
            object text = textField?.GetValue(instance);
            PropertyInfo textProperty = text?.GetType().GetGameProperty(
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


}
