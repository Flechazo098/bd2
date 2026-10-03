using System;
using System.ComponentModel;
using System.IO;
using System.Runtime.InteropServices;
using System.Security.Cryptography;
using System.Text;
using UnityEngine;

namespace Bd2LoginUI;

internal sealed class MemoryAccessTokenStore
{
    private string value;
    private string origin;
    private string provider;
    private long expiresAt;

    public string Get()
    {
        return value ?? string.Empty;
    }

    public void Set(string token, string tokenOrigin, string tokenProvider, long expiresInSeconds)
    {
        if (string.IsNullOrEmpty(token) || string.IsNullOrEmpty(tokenOrigin) ||
            string.IsNullOrEmpty(tokenProvider) || expiresInSeconds <= 0)
        {
            throw new ArgumentException("access token metadata is incomplete", nameof(token));
        }
        value = token;
        origin = tokenOrigin;
        provider = tokenProvider;
        expiresAt = DateTimeOffset.UtcNow.ToUnixTimeSeconds() + expiresInSeconds;
    }

    public bool IsUsable(string tokenOrigin, long safetyWindowSeconds = 30)
    {
        return !string.IsNullOrEmpty(value) && origin == tokenOrigin &&
               !string.IsNullOrEmpty(provider) &&
               expiresAt > DateTimeOffset.UtcNow.ToUnixTimeSeconds() + safetyWindowSeconds;
    }

    public void Clear()
    {
        value = null;
        origin = null;
        provider = null;
        expiresAt = 0;
    }
}

internal interface IRefreshCredentialStore
{
    bool IsSupported { get; }
    bool Contains(string origin);
    RefreshCredential Load(string origin);
    void Save(string origin, RefreshCredential credential);
    void Delete(string origin);
}

[Serializable]
internal sealed class RefreshCredential
{
    public int version;
    public string origin;
    public string provider;
    public string refresh_token;
    public long expires_at;
    public string pending_attempt_id;
    public string pending_refresh_token;
}

internal static class PlatformRefreshCredentialStore
{
    public static IRefreshCredentialStore Create()
    {
        if (RuntimeInformation.IsOSPlatform(OSPlatform.Windows))
        {
            return new WindowsDpapiRefreshCredentialStore();
        }
        if (RuntimeInformation.IsOSPlatform(OSPlatform.OSX))
        {
            return new MacOSKeychainRefreshCredentialStore();
        }
        return new UnsupportedRefreshCredentialStore();
    }
}

internal sealed class UnsupportedRefreshCredentialStore : IRefreshCredentialStore
{
    public bool IsSupported => false;

    public bool Contains(string origin)
    {
        return false;
    }

    public RefreshCredential Load(string origin)
    {
        throw new PlatformNotSupportedException("secure refresh credential storage is unavailable on this platform");
    }

    public void Save(string origin, RefreshCredential credential)
    {
        throw new PlatformNotSupportedException("secure refresh credential storage is unavailable on this platform");
    }

    public void Delete(string origin)
    {
    }
}

internal sealed class WindowsDpapiRefreshCredentialStore : IRefreshCredentialStore
{
    private const string Purpose = "BD2.LoginUI.Refresh.v1";
    private const string PreferencePrefix = "BD2OAuthRefreshV1_";
    private const uint CryptProtectUiForbidden = 0x1;

    public bool IsSupported => true;

    public bool Contains(string origin)
    {
        return PlayerPrefs.HasKey(PreferenceFor(origin));
    }

    public RefreshCredential Load(string origin)
    {
        string encoded = PlayerPrefs.GetString(PreferenceFor(origin), string.Empty);
        if (string.IsNullOrEmpty(encoded))
        {
            throw new InvalidDataException("saved Windows credential is empty");
        }
        byte[] cipher;
        try
        {
            cipher = Convert.FromBase64String(encoded);
        }
        catch (FormatException ex)
        {
            throw new InvalidDataException("saved Windows credential is malformed", ex);
        }
        byte[] entropy = Entropy(origin);
        byte[] plain = null;
        try
        {
            plain = Unprotect(cipher, entropy);
            string json = Encoding.UTF8.GetString(plain);
            return JsonUtility.FromJson<RefreshCredential>(json);
        }
        finally
        {
            Clear(cipher);
            Clear(entropy);
            Clear(plain);
        }
    }

    public void Save(string origin, RefreshCredential credential)
    {
        byte[] plain = Encoding.UTF8.GetBytes(JsonUtility.ToJson(credential));
        byte[] entropy = Entropy(origin);
        byte[] cipher = null;
        try
        {
            cipher = Protect(plain, entropy);
            PlayerPrefs.SetString(PreferenceFor(origin), Convert.ToBase64String(cipher));
            PlayerPrefs.Save();
        }
        finally
        {
            Clear(plain);
            Clear(entropy);
            Clear(cipher);
        }
    }

    public void Delete(string origin)
    {
        PlayerPrefs.DeleteKey(PreferenceFor(origin));
        PlayerPrefs.Save();
    }

    private static string PreferenceFor(string origin)
    {
        byte[] input = Encoding.UTF8.GetBytes(origin);
        byte[] digest;
        using (SHA256 sha = SHA256.Create())
        {
            digest = sha.ComputeHash(input);
        }
        try
        {
            return PreferencePrefix + Hex(digest);
        }
        finally
        {
            Clear(input);
            Clear(digest);
        }
    }

    private static byte[] Entropy(string origin)
    {
        return Encoding.UTF8.GetBytes(Purpose + "\0" + origin);
    }

    private static byte[] Protect(byte[] plain, byte[] entropy)
    {
        DataBlob input = Allocate(plain);
        DataBlob optionalEntropy = Allocate(entropy);
        DataBlob output = default;
        try
        {
            if (!CryptProtectData(ref input, Purpose, ref optionalEntropy, IntPtr.Zero, IntPtr.Zero, CryptProtectUiForbidden, out output))
            {
                throw new Win32Exception(Marshal.GetLastWin32Error(), "Windows DPAPI could not protect the refresh credential");
            }
            return Copy(output);
        }
        finally
        {
            ZeroAndFreeHGlobal(ref input);
            ZeroAndFreeHGlobal(ref optionalEntropy);
            LocalFreeBlob(ref output, false);
        }
    }

    private static byte[] Unprotect(byte[] cipher, byte[] entropy)
    {
        DataBlob input = Allocate(cipher);
        DataBlob optionalEntropy = Allocate(entropy);
        DataBlob output = default;
        IntPtr description = IntPtr.Zero;
        try
        {
            if (!CryptUnprotectData(ref input, out description, ref optionalEntropy, IntPtr.Zero, IntPtr.Zero, CryptProtectUiForbidden, out output))
            {
                throw new Win32Exception(Marshal.GetLastWin32Error(), "Windows DPAPI could not unprotect the refresh credential");
            }
            return Copy(output);
        }
        finally
        {
            ZeroAndFreeHGlobal(ref input);
            ZeroAndFreeHGlobal(ref optionalEntropy);
            LocalFreeBlob(ref output, true);
            if (description != IntPtr.Zero)
            {
                LocalFree(description);
            }
        }
    }

    private static DataBlob Allocate(byte[] bytes)
    {
        if (bytes == null || bytes.Length == 0)
        {
            return default;
        }
        DataBlob blob = new DataBlob { size = bytes.Length, data = Marshal.AllocHGlobal(bytes.Length) };
        Marshal.Copy(bytes, 0, blob.data, bytes.Length);
        return blob;
    }

    private static byte[] Copy(DataBlob blob)
    {
        if (blob.size <= 0 || blob.data == IntPtr.Zero)
        {
            throw new InvalidDataException("platform credential store returned an empty value");
        }
        byte[] bytes = new byte[blob.size];
        Marshal.Copy(blob.data, bytes, 0, bytes.Length);
        return bytes;
    }

    private static void ZeroAndFreeHGlobal(ref DataBlob blob)
    {
        if (blob.data == IntPtr.Zero)
        {
            return;
        }
        ZeroUnmanaged(blob.data, blob.size);
        Marshal.FreeHGlobal(blob.data);
        blob = default;
    }

    private static void LocalFreeBlob(ref DataBlob blob, bool clear)
    {
        if (blob.data == IntPtr.Zero)
        {
            return;
        }
        if (clear)
        {
            ZeroUnmanaged(blob.data, blob.size);
        }
        LocalFree(blob.data);
        blob = default;
    }

    private static void ZeroUnmanaged(IntPtr data, int length)
    {
        for (int i = 0; i < length; i++)
        {
            Marshal.WriteByte(data, i, 0);
        }
    }

    private static string Hex(byte[] value)
    {
        StringBuilder builder = new StringBuilder(value.Length * 2);
        foreach (byte item in value)
        {
            builder.Append(item.ToString("x2"));
        }
        return builder.ToString();
    }

    private static void Clear(byte[] bytes)
    {
        if (bytes != null)
        {
            Array.Clear(bytes, 0, bytes.Length);
        }
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct DataBlob
    {
        public int size;
        public IntPtr data;
    }

    [DllImport("crypt32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool CryptProtectData(
        ref DataBlob dataIn,
        string description,
        ref DataBlob optionalEntropy,
        IntPtr reserved,
        IntPtr prompt,
        uint flags,
        out DataBlob dataOut);

    [DllImport("crypt32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool CryptUnprotectData(
        ref DataBlob dataIn,
        out IntPtr description,
        ref DataBlob optionalEntropy,
        IntPtr reserved,
        IntPtr prompt,
        uint flags,
        out DataBlob dataOut);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern IntPtr LocalFree(IntPtr memory);
}

internal sealed class MacOSKeychainRefreshCredentialStore : IRefreshCredentialStore
{
    private const int Success = 0;
    private const int ItemNotFound = -25300;
    private const string Account = "refresh-v1";

    public bool IsSupported => true;

    public bool Contains(string origin)
    {
        int status = Find(origin, out uint length, out IntPtr data, out IntPtr item);
        ReleaseFound(length, data, item);
        if (status == Success)
        {
            return true;
        }
        if (status == ItemNotFound)
        {
            return false;
        }
        throw StatusException(status);
    }

    public RefreshCredential Load(string origin)
    {
        int status = Find(origin, out uint length, out IntPtr data, out IntPtr item);
        if (status != Success)
        {
            ReleaseFound(length, data, item);
            throw StatusException(status);
        }
        byte[] bytes = new byte[length];
        try
        {
            Marshal.Copy(data, bytes, 0, bytes.Length);
            return JsonUtility.FromJson<RefreshCredential>(Encoding.UTF8.GetString(bytes));
        }
        finally
        {
            Array.Clear(bytes, 0, bytes.Length);
            ReleaseFound(length, data, item);
        }
    }

    public void Save(string origin, RefreshCredential credential)
    {
        byte[] value = Encoding.UTF8.GetBytes(JsonUtility.ToJson(credential));
        try
        {
            int status = Find(origin, out uint oldLength, out IntPtr oldData, out IntPtr item);
            if (status == Success)
            {
                if (oldData != IntPtr.Zero)
                {
                    SecKeychainItemFreeContent(IntPtr.Zero, oldData);
                }
                try
                {
                    status = SecKeychainItemModifyAttributesAndData(item, IntPtr.Zero, (uint)value.Length, value);
                }
                finally
                {
                    ReleaseItem(item);
                }
            }
            else if (status == ItemNotFound)
            {
                byte[] service = Service(origin);
                byte[] account = Encoding.UTF8.GetBytes(Account);
                try
                {
                    status = SecKeychainAddGenericPassword(IntPtr.Zero, (uint)service.Length, service, (uint)account.Length, account, (uint)value.Length, value, out item);
                    ReleaseItem(item);
                }
                finally
                {
                    Array.Clear(service, 0, service.Length);
                    Array.Clear(account, 0, account.Length);
                }
            }
            else
            {
                ReleaseFound(oldLength, oldData, item);
            }
            if (status != Success)
            {
                throw StatusException(status);
            }
        }
        finally
        {
            Array.Clear(value, 0, value.Length);
        }
    }

    public void Delete(string origin)
    {
        int status = Find(origin, out uint length, out IntPtr data, out IntPtr item);
        if (data != IntPtr.Zero)
        {
            SecKeychainItemFreeContent(IntPtr.Zero, data);
        }
        if (status == ItemNotFound)
        {
            ReleaseItem(item);
            return;
        }
        if (status != Success)
        {
            ReleaseItem(item);
            throw StatusException(status);
        }
        try
        {
            status = SecKeychainItemDelete(item);
        }
        finally
        {
            ReleaseItem(item);
        }
        if (status != Success)
        {
            throw StatusException(status);
        }
    }

    private static int Find(string origin, out uint length, out IntPtr data, out IntPtr item)
    {
        byte[] service = Service(origin);
        byte[] account = Encoding.UTF8.GetBytes(Account);
        try
        {
            return SecKeychainFindGenericPassword(IntPtr.Zero, (uint)service.Length, service, (uint)account.Length, account, out length, out data, out item);
        }
        finally
        {
            Array.Clear(service, 0, service.Length);
            Array.Clear(account, 0, account.Length);
        }
    }

    private static byte[] Service(string origin)
    {
        return Encoding.UTF8.GetBytes("BD2 Login UI OAuth: " + origin);
    }

    private static void ReleaseFound(uint length, IntPtr data, IntPtr item)
    {
        if (data != IntPtr.Zero)
        {
            SecKeychainItemFreeContent(IntPtr.Zero, data);
        }
        ReleaseItem(item);
    }

    private static void ReleaseItem(IntPtr item)
    {
        if (item != IntPtr.Zero)
        {
            CFRelease(item);
        }
    }

    private static Exception StatusException(int status)
    {
        return status == ItemNotFound
            ? new FileNotFoundException("refresh credential was not found in macOS Keychain")
            : new InvalidOperationException("macOS Keychain operation failed with OSStatus " + status);
    }

    [DllImport("/System/Library/Frameworks/Security.framework/Security")]
    private static extern int SecKeychainFindGenericPassword(
        IntPtr keychainOrArray,
        uint serviceNameLength,
        byte[] serviceName,
        uint accountNameLength,
        byte[] accountName,
        out uint passwordLength,
        out IntPtr passwordData,
        out IntPtr itemRef);

    [DllImport("/System/Library/Frameworks/Security.framework/Security")]
    private static extern int SecKeychainAddGenericPassword(
        IntPtr keychain,
        uint serviceNameLength,
        byte[] serviceName,
        uint accountNameLength,
        byte[] accountName,
        uint passwordLength,
        byte[] passwordData,
        out IntPtr itemRef);

    [DllImport("/System/Library/Frameworks/Security.framework/Security")]
    private static extern int SecKeychainItemModifyAttributesAndData(
        IntPtr itemRef,
        IntPtr attrList,
        uint length,
        byte[] data);

    [DllImport("/System/Library/Frameworks/Security.framework/Security")]
    private static extern int SecKeychainItemDelete(IntPtr itemRef);

    [DllImport("/System/Library/Frameworks/Security.framework/Security")]
    private static extern int SecKeychainItemFreeContent(IntPtr attrList, IntPtr data);

    [DllImport("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")]
    private static extern void CFRelease(IntPtr item);
}
