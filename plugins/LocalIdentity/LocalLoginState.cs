using System;
using System.Reflection;
using BD2.GameNames;
using Bd2Login;
using BepInEx.Logging;
using HarmonyLib;
using gamfs.Platform;

namespace Bd2LocalIdentity;

internal static class LocalLoginState
{
    private static string Origin;

    internal static void Install(Harmony harmony, string serverOrigin, ManualLogSource log)
    {
        Origin = ServerLoginPreferences.NormalizeOrigin(new Uri(serverOrigin));
        PropertyInfo automatic = typeof(PlatformManager).GetGameProperty(
            nameof(PlatformManager.IsAutoLogin), BindingFlags.Public | BindingFlags.Instance);
        PropertyInfo checkbox = typeof(PlatformManager).GetGameProperty(
            nameof(PlatformManager.UseAutoLoginPC), BindingFlags.Public | BindingFlags.Instance);
        MethodInfo accessToken = Game.Getter(() => BDNetwork.CommonPacket.AccessToken);
        Validate(automatic);
        Validate(checkbox);
        if (accessToken == null) throw new MissingMethodException("CommonPacket.AccessToken getter was not found");

        harmony.Patch(automatic.GetGetMethod(), prefix: new HarmonyMethod(typeof(LocalLoginState), nameof(AutoLoginGetter)));
        harmony.Patch(automatic.GetSetMethod(), prefix: new HarmonyMethod(typeof(LocalLoginState), nameof(AutoLoginSetter)));
        harmony.Patch(checkbox.GetGetMethod(), prefix: new HarmonyMethod(typeof(LocalLoginState), nameof(CheckboxGetter)));
        harmony.Patch(checkbox.GetSetMethod(), prefix: new HarmonyMethod(typeof(LocalLoginState), nameof(CheckboxSetter)));
        // Skip the original getter's PlayerPrefs access. LoginUI's postfix
        // supplies its in-memory OAuth token when installed; this value also
        // supports LocalIdentity alone without writing the official token key.
        harmony.Patch(accessToken, prefix: new HarmonyMethod(typeof(LocalLoginState), nameof(BootstrapToken)));
        log.LogInfo("Login preferences isolated by server origin; official login keys are untouched");
    }

    private static void Validate(PropertyInfo property)
    {
        if (property == null || property.PropertyType != typeof(bool) ||
            property.GetGetMethod() == null || property.GetSetMethod() == null)
            throw new MissingMemberException("PlatformManager auto-login property was not found (client version mismatch)");
    }

    private static bool AutoLoginGetter(ref bool __result)
    {
        __result = ServerLoginPreferences.IsAutoLogin(Origin);
        return false;
    }

    private static bool AutoLoginSetter(bool __0)
    {
        ServerLoginPreferences.SetAutoLogin(Origin, __0);
        return false;
    }

    private static bool CheckboxGetter(ref bool __result)
    {
        __result = ServerLoginPreferences.UseAutoLoginPC(Origin);
        return false;
    }

    private static bool CheckboxSetter(bool __0)
    {
        ServerLoginPreferences.SetUseAutoLoginPC(Origin, __0);
        return false;
    }

    private static bool BootstrapToken(ref string __result)
    {
        __result = "bd2-local-development-user";
        return false;
    }
}
