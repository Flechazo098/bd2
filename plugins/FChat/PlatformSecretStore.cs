using TextComponent = BD2.GameSdk.Component;
using System;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using UnityEngine;

namespace Bd2FChat;

internal static class PlatformSecretStore
{
    private const string Prefix = "BD2NativeTranslationSecretV1_";
    internal static bool IsSupported => RuntimeInformation.IsOSPlatform(OSPlatform.Windows) || RuntimeInformation.IsOSPlatform(OSPlatform.OSX);

    internal static bool Contains(string scope)
    {
        if (RuntimeInformation.IsOSPlatform(OSPlatform.Windows)) return PlayerPrefs.HasKey(Prefix + scope);
        if (!RuntimeInformation.IsOSPlatform(OSPlatform.OSX)) return false;
        int status = Find(scope, out _, out IntPtr data, out IntPtr item);
        Release(data, item);
        if (status == -25300) return false;
        Check(status);
        return true;
    }

    internal static string Read(string scope)
    {
        if (RuntimeInformation.IsOSPlatform(OSPlatform.Windows))
        {
            if (!Contains(scope)) return string.Empty;
            byte[] cipher = Convert.FromBase64String(PlayerPrefs.GetString(Prefix + scope));
            byte[] entropy = Entropy(scope), plain = null;
            try { plain = WindowsSecretProtection.Unprotect(cipher, entropy); return Encoding.UTF8.GetString(plain); }
            finally { Clear(cipher); Clear(entropy); Clear(plain); }
        }
        RequireSupported();
        int status = Find(scope, out uint length, out IntPtr data, out IntPtr item);
        if (status == -25300) { Release(data, item); return string.Empty; }
        byte[] bytes = null;
        try { Check(status); if (length > 16384) throw new InvalidDataException(TextComponent.Translatable("fchat.error.secret_large").GetString()); bytes = new byte[length]; Marshal.Copy(data, bytes, 0, bytes.Length); return Encoding.UTF8.GetString(bytes); }
        finally { Clear(bytes); Release(data, item); }
    }

    internal static void Write(string scope, string secret)
    {
        RequireSupported();
        byte[] plain = Encoding.UTF8.GetBytes(secret);
        try
        {
            if (RuntimeInformation.IsOSPlatform(OSPlatform.Windows))
            {
                byte[] entropy = Entropy(scope), cipher = null;
                try { cipher = WindowsSecretProtection.Protect(plain, entropy); PlayerPrefs.SetString(Prefix + scope, Convert.ToBase64String(cipher)); PlayerPrefs.Save(); }
                finally { Clear(entropy); Clear(cipher); }
                return;
            }
            int status = Find(scope, out _, out IntPtr data, out IntPtr item);
            try
            {
                if (status == 0) status = SecKeychainItemModifyAttributesAndData(item, IntPtr.Zero, (uint)plain.Length, plain);
                else if (status == -25300)
                {
                    byte[] service = Service(scope), account = Encoding.UTF8.GetBytes("api-key");
                    status = SecKeychainAddGenericPassword(IntPtr.Zero, (uint)service.Length, service, (uint)account.Length, account, (uint)plain.Length, plain, out item);
                }
                Check(status);
            }
            finally { Release(data, item); }
        }
        finally { Clear(plain); }
    }

    internal static void Delete(string scope)
    {
        if (RuntimeInformation.IsOSPlatform(OSPlatform.Windows)) { PlayerPrefs.DeleteKey(Prefix + scope); PlayerPrefs.Save(); return; }
        RequireSupported();
        int status = Find(scope, out _, out IntPtr data, out IntPtr item);
        try { if (status == -25300) return; Check(status); Check(SecKeychainItemDelete(item)); }
        finally { Release(data, item); }
    }

    private static byte[] Entropy(string scope) => Encoding.UTF8.GetBytes("BD2.NativeTranslation.Secret.v1\0" + scope);
    private static byte[] Service(string scope) => Encoding.UTF8.GetBytes("BD2 Native Translation API: " + scope);
    private static void Clear(byte[] bytes) { if (bytes != null) Array.Clear(bytes, 0, bytes.Length); }
    private static void RequireSupported() { if (!IsSupported) throw new PlatformNotSupportedException(TextComponent.Translatable("fchat.error.secret_unsupported").GetString()); }
    private static void Check(int status) { if (status != 0) throw new InvalidOperationException(TextComponent.Translatable("fchat.error.secret_failed", status).GetString()); }
    private static int Find(string scope, out uint length, out IntPtr data, out IntPtr item)
    {
        byte[] service = Service(scope), account = Encoding.UTF8.GetBytes("api-key");
        return SecKeychainFindGenericPassword(IntPtr.Zero, (uint)service.Length, service, (uint)account.Length, account, out length, out data, out item);
    }
    private static void Release(IntPtr data, IntPtr item)
    {
        if (data != IntPtr.Zero && SecKeychainItemFreeContent(IntPtr.Zero, data) != 0) Plugin.Log?.LogWarning("Secure API key content cleanup failed");
        if (item != IntPtr.Zero) CFRelease(item);
    }

    [DllImport("/System/Library/Frameworks/Security.framework/Security")] private static extern int SecKeychainFindGenericPassword(IntPtr keychain, uint serviceLength, byte[] service, uint accountLength, byte[] account, out uint length, out IntPtr data, out IntPtr item);
    [DllImport("/System/Library/Frameworks/Security.framework/Security")] private static extern int SecKeychainAddGenericPassword(IntPtr keychain, uint serviceLength, byte[] service, uint accountLength, byte[] account, uint length, byte[] data, out IntPtr item);
    [DllImport("/System/Library/Frameworks/Security.framework/Security")] private static extern int SecKeychainItemModifyAttributesAndData(IntPtr item, IntPtr attributes, uint length, byte[] data);
    [DllImport("/System/Library/Frameworks/Security.framework/Security")] private static extern int SecKeychainItemDelete(IntPtr item);
    [DllImport("/System/Library/Frameworks/Security.framework/Security")] private static extern int SecKeychainItemFreeContent(IntPtr attributes, IntPtr data);
    [DllImport("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")] private static extern void CFRelease(IntPtr item);
}
