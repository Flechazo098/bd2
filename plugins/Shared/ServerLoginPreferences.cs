using System;
using System.Security.Cryptography;
using System.Text;
using UnityEngine;

namespace Bd2Login;

// Both plugins use the same normalized origin and key format. Never use the
// official login preference keys, even before the authentication policy loads.
internal static class ServerLoginPreferences
{
    internal static string NormalizeOrigin(Uri uri)
    {
        if (uri == null || !uri.IsAbsoluteUri || string.IsNullOrEmpty(uri.Host) ||
            (uri.Scheme != Uri.UriSchemeHttp && uri.Scheme != Uri.UriSchemeHttps) || uri.UserInfo.Length != 0)
            throw new InvalidOperationException("authentication server origin is unavailable or invalid");
        var builder = new UriBuilder(uri.Scheme.ToLowerInvariant(), uri.IdnHost.ToLowerInvariant(),
            uri.IsDefaultPort ? -1 : uri.Port);
        return builder.Uri.GetLeftPart(UriPartial.Authority).TrimEnd('/');
    }

    internal static string Key(string prefix, string origin)
    {
        string normalized = NormalizeOrigin(new Uri(origin, UriKind.Absolute));
        using SHA256 sha = SHA256.Create();
        byte[] digest = sha.ComputeHash(Encoding.UTF8.GetBytes(normalized));
        var hex = new StringBuilder(digest.Length * 2);
        foreach (byte value in digest) hex.Append(value.ToString("x2"));
        return prefix + hex;
    }

    internal static bool IsAutoLogin(string origin) => Read("IsAutoLogin", origin);
    internal static bool UseAutoLoginPC(string origin) => Read("StandaloneAutoLogin", origin);
    internal static void SetAutoLogin(string origin, bool value) => Write("IsAutoLogin", origin, value);
    internal static void SetUseAutoLoginPC(string origin, bool value) => Write("StandaloneAutoLogin", origin, value);

    private static bool Read(string setting, string origin) =>
        PlayerPrefs.GetInt(Key("BD2LoginV1_" + setting + "_", origin), 0) != 0;

    private static void Write(string setting, string origin, bool value)
    {
        PlayerPrefs.SetInt(Key("BD2LoginV1_" + setting + "_", origin), value ? 1 : 0);
        PlayerPrefs.Save();
    }
}
