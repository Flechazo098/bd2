using System;
using System.ComponentModel;
using System.IO;
using System.Runtime.InteropServices;
using TextComponent = BD2.GameSdk.Component;

namespace Bd2FChat;

internal static class WindowsSecretProtection
{
    private const uint CryptProtectUiForbidden = 0x1;
    internal static byte[] Protect(byte[] plain, byte[] entropy)
    {
        DataBlob input = Allocate(plain);
        DataBlob optionalEntropy = Allocate(entropy);
        DataBlob output = default;
        try
        {
            if (!CryptProtectData(ref input, "BD2.NativeTranslation.Secret.v1", ref optionalEntropy, IntPtr.Zero, IntPtr.Zero, CryptProtectUiForbidden, out output))
            {
                throw new Win32Exception(Marshal.GetLastWin32Error(), TextComponent.Translatable("fchat.error.secret_protect").GetString());
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

    internal static byte[] Unprotect(byte[] cipher, byte[] entropy)
    {
        DataBlob input = Allocate(cipher);
        DataBlob optionalEntropy = Allocate(entropy);
        DataBlob output = default;
        IntPtr description = IntPtr.Zero;
        try
        {
            if (!CryptUnprotectData(ref input, out description, ref optionalEntropy, IntPtr.Zero, IntPtr.Zero, CryptProtectUiForbidden, out output))
            {
                throw new Win32Exception(Marshal.GetLastWin32Error(), TextComponent.Translatable("fchat.error.secret_unprotect").GetString());
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
        var blob = new DataBlob { size = bytes.Length, data = Marshal.AllocHGlobal(bytes.Length) };
        Marshal.Copy(bytes, 0, blob.data, bytes.Length);
        return blob;
    }

    private static byte[] Copy(DataBlob blob)
    {
        if (blob.size <= 0 || blob.data == IntPtr.Zero)
        {
            throw new InvalidDataException(TextComponent.Translatable("fchat.error.secret_empty").GetString());
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
