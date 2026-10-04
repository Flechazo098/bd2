using System;
using System.IO;
using System.Reflection;
using BD2.GameNames;
using Bd2Login;
using BepInEx.Logging;

using static BD2.GameNames.Game;

namespace Bd2LoginUI;

// Shared server origin and resolved client transition bindings; feature state stays in its owner.
internal static class LoginRuntime
{
    internal static ManualLogSource Log;
    internal static MethodInfo SetIntroState;
    internal static MethodInfo SendMaintenance;
    internal static MethodInfo CancelMaintenanceTimeout;
    internal static Uri ServerRoot;
    internal static MethodInfo OpenPCLoginPopup;
    internal static MethodInfo EnterGame;
    internal static string NormalizedServerOrigin()
    {
        return NormalizeOrigin(ServerRoot);
    }

    internal static bool SameOrigin(Uri left, Uri right)
    {
        return string.Equals(NormalizeOrigin(left), NormalizeOrigin(right), StringComparison.Ordinal);
    }

    private static string NormalizeOrigin(Uri uri) => ServerLoginPreferences.NormalizeOrigin(uri);

    internal static void ValidateOAuthTransport(Uri uri)
    {
        if (uri == null || !uri.IsAbsoluteUri ||
            (uri.Scheme != Uri.UriSchemeHttps && !(uri.Scheme == Uri.UriSchemeHttp && uri.IsLoopback)))
        {
            throw new InvalidOperationException("OAuth requires HTTPS except when connecting to a loopback server");
        }
    }

    internal static void ValidateBrowserURL(string raw)
    {
        if (!Uri.TryCreate(raw, UriKind.Absolute, out Uri uri) ||
            (uri.Scheme != Uri.UriSchemeHttps && !(uri.Scheme == Uri.UriSchemeHttp && uri.IsLoopback)) ||
            uri.UserInfo.Length != 0)
        {
            throw new InvalidDataException("server returned an unsafe browser login URL");
        }
    }

    internal static T FindUnitySingleton<T>() where T : UnityEngine.Object
    {
        return UnityEngine.Object.FindObjectOfType<T>();
    }

    internal static Uri CurrentMaintenanceUri()
    {
        Type serverURLInfo = typeof(BDNetwork.ServerURLInfo);
        FieldInfo maintenanceUri = serverURLInfo?.GetGameField(
            nameof(BDNetwork.ServerURLInfo.MaintenanceUri),
            BindingFlags.Static | BindingFlags.Public);
        Uri result = maintenanceUri?.GetValue(null) as Uri;
        if (result == null || (result.Scheme != Uri.UriSchemeHttp && result.Scheme != Uri.UriSchemeHttps))
        {
            throw new InvalidOperationException("current server maintenance URL is unavailable");
        }
        return result;
    }

    internal static MethodInfo FindOpenPCLoginPopup()
    {
        Type uiManager = typeof(UIManager);
        MethodInfo method = uiManager.GetGameMethod(
            nameof(UIManager.OpenPCLoginPopupUI),
            BindingFlags.Static | BindingFlags.Public,
            null,
            new[] { typeof(Action) },
            null);
        return method != null && method.ReturnType == typeof(void) ? method : null;
    }

    internal static MethodInfo FindAccessTokenGetter()
    {
        MethodInfo getter = Game.Getter(() => BDNetwork.CommonPacket.AccessToken);
        return getter != null && getter.IsStatic && getter.ReturnType == typeof(string) &&
               getter.GetParameters().Length == 0 ? getter : null;
    }

    internal static MethodInfo FindClearPCLocalData()
    {
        Type platformManager = typeof(gamfs.Platform.PlatformManager);
        MethodInfo method = platformManager?.GetGameMethod(
            nameof(gamfs.Platform.PlatformManager.ClearPCLocalData),
            BindingFlags.Instance | BindingFlags.Public,
            null,
            Type.EmptyTypes,
            null);
        return method != null && method.ReturnType == typeof(void) ? method : null;
    }

}
