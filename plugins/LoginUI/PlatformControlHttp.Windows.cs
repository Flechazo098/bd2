using System;
using System.Collections.Generic;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading;

namespace Bd2LoginUI;

internal static partial class PlatformControlHttp
{
    private static readonly object PoolLock = new object();
    private static readonly CancellationTokenSource ShutdownCancellation = new CancellationTokenSource();
    private static readonly Dictionary<string, WindowsPool> WindowsPools = new Dictionary<string, WindowsPool>();
    private static readonly Dictionary<IntPtr, WindowsRequest> WindowsRequests = new Dictionary<IntPtr, WindowsRequest>();
    private static readonly WinHttpCallback WindowsCallback = OnWindowsStatus;
    private static bool shuttingDown;

    public static void Shutdown()
    {
        lock (PoolLock)
        {
            if (shuttingDown) return;
            shuttingDown = true;
        }
        ShutdownCancellation.Cancel();
        lock (PoolLock)
        {
            foreach (WindowsPool pool in WindowsPools.Values) pool.Retired = true;
            foreach (WindowsPool pool in WindowsPools.Values) if (pool.Users == 0) pool.Close();
            WindowsPools.Clear();
            foreach (CurlPool pool in CurlPools.Values)
                while (pool.Idle.Count != 0) curl_easy_cleanup(pool.Idle.Pop());
            CurlPools.Clear();
        }
    }

    private sealed class WindowsPool
    {
        internal IntPtr Session, Connection;
        internal int Users;
        internal string Key;
        internal bool Retired;
        internal DateTime LastUsed;
        internal void Close()
        {
            if (Connection != IntPtr.Zero) WinHttpCloseHandle(Connection);
            if (Session != IntPtr.Zero) WinHttpCloseHandle(Session);
            Connection = Session = IntPtr.Zero;
        }
    }

    private static WindowsPool AcquireWindows(Uri uri, NativeProxySettings proxy)
    {
        string key = uri.GetLeftPart(UriPartial.Authority) + "|" + proxy.Key;
        lock (PoolLock)
        {
            if (shuttingDown) throw new OperationCanceledException();
            List<string> stale = new List<string>();
            foreach (KeyValuePair<string, WindowsPool> entry in WindowsPools)
                if (entry.Value.Users == 0 && (DateTime.UtcNow - entry.Value.LastUsed > TimeSpan.FromMinutes(2) || WindowsPools.Count >= 16))
                    stale.Add(entry.Key);
            foreach (string old in stale) { WindowsPools[old].Close(); WindowsPools.Remove(old); }
            if (!WindowsPools.TryGetValue(key, out WindowsPool pool))
            {
                pool = new WindowsPool { Key = key };
                string server = proxy.ProxyUrl == null ? null : new Uri(proxy.ProxyUrl).Authority;
                pool.Session = WinHttpOpen("BD2LoginUI", server == null ? 1u : 3u, server, null, 0x10000000);
                if (pool.Session == IntPtr.Zero) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
                pool.Connection = WinHttpConnect(pool.Session, uri.IdnHost, (ushort)uri.Port, 0);
                if (pool.Connection == IntPtr.Zero) { int error = Marshal.GetLastWin32Error(); pool.Close(); throw new System.ComponentModel.Win32Exception(error); }
                WindowsPools.Add(key, pool);
            }
            pool.Users++;
            return pool;
        }
    }
    private static void ReleaseWindows(WindowsPool pool, bool reusable)
    {
        lock (PoolLock)
        {
            pool.LastUsed = DateTime.UtcNow;
            // Closing an aborted request can leave WinHTTP connection-cache entries
            // waiting for server-side I/O. Retire that cache instead of routing fresh
            // work through it; existing leases retain their session until finished.
            if (!reusable)
            {
                pool.Retired = true;
                if (WindowsPools.TryGetValue(pool.Key, out WindowsPool current) && ReferenceEquals(current, pool))
                    WindowsPools.Remove(pool.Key);
            }
            if (--pool.Users == 0 && pool.Retired) pool.Close();
        }
    }

    // Every native API invocation and handle close is serialized. Async operations
    // return immediately; WinHTTP owns outstanding I/O until HANDLE_CLOSING.
    // Pinned buffers remain alive until that final callback, including cancellation.
    private sealed class WindowsRequest
    {
        internal readonly object Gate = new object();
        internal readonly AutoResetEvent Ready = new AutoResetEvent(false);
        internal readonly ManualResetEvent Closed = new ManualResetEvent(false);
        internal IntPtr Handle;
        internal bool Closing;
        internal uint Status, Read;
        internal int Error;
        internal void Close()
        {
            lock (Gate)
            {
                if (Closing) return;
                Closing = true;
                if (Handle != IntPtr.Zero) WinHttpCloseHandle(Handle);
            }
        }
        internal void Invoke(Func<bool> call)
        {
            lock (Gate)
            {
                if (Closing) throw new OperationCanceledException();
                if (!call())
                {
                    int error = Marshal.GetLastWin32Error();
                    if (error != 997) throw new System.ComponentModel.Win32Exception(error);
                }
            }
        }
        internal void Wait(uint wanted, CancellationToken token)
        {
            while (true)
            {
                token.ThrowIfCancellationRequested();
                Ready.WaitOne();
                token.ThrowIfCancellationRequested();
                lock (Gate)
                {
                    if (Error != 0) throw new System.ComponentModel.Win32Exception(Error);
                    if (Status == wanted) return;
                    if (Closing) throw new OperationCanceledException();
                }
            }
        }
    }
    private static void OnWindowsStatus(IntPtr handle, UIntPtr context, uint status, IntPtr information, uint length)
    {
        WindowsRequest request;
        lock (PoolLock) WindowsRequests.TryGetValue(handle, out request);
        if (request == null) return;
        // Do not acquire Gate: WinHTTP may synchronously invoke a callback inside
        // an API/CloseHandle. Callback updates precede setting the event.
        if (status == 0x800)
        {
            // Remove before waking the lease: WinHTTP may reuse this numeric
            // handle immediately after the final callback has completed.
            lock (PoolLock) WindowsRequests.Remove(handle);
            request.Ready.Set(); request.Closed.Set(); return;
        }
        if (status == 0x200000) request.Error = Marshal.ReadInt32(information, IntPtr.Size);
        if (status == 0x80000) request.Read = length;
        if (status == 0x400000 || status == 0x20000 || status == 0x80000 || status == 0x200000)
        { request.Status = status; request.Ready.Set(); }
    }

    private static Response SendWindows(Uri uri, string method, byte[] body, string authorization,
        int timeoutSeconds, CancellationToken token, int maxResponseBytes, IReadOnlyDictionary<string, string> requestHeaders)
    {
        NativeProxySettings proxy = NativeProxyPolicy.Resolve(uri);
        token.ThrowIfCancellationRequested();
        WindowsPool pool = AcquireWindows(uri, proxy);
        WindowsRequest state = new WindowsRequest();
        GCHandle bodyPin = default, readPin = default;
        bool callbackInstalled = false, reusable = false;
        try
        {
            state.Handle = WinHttpOpenRequest(pool.Connection, method, uri.PathAndQuery, null, null, IntPtr.Zero,
                uri.Scheme == Uri.UriSchemeHttps ? 0x00800000u : 0u);
            if (state.Handle == IntPtr.Zero) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
            lock (PoolLock) WindowsRequests.Add(state.Handle, state);
            if (WinHttpSetStatusCallback(state.Handle, WindowsCallback, 0x400000 | 0x20000 | 0x80000 | 0x200000 | 0x800, UIntPtr.Zero) == new IntPtr(-1))
                throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
            callbackInstalled = true;
            using (token.Register(() => { state.Close(); state.Ready.Set(); }))
            {
                uint disabled = 0x1 | 0x2 | 0x4; // disable cookies, redirects, automatic authentication
                state.Invoke(() => WinHttpSetOption(state.Handle, 63, ref disabled, 4));
                int timeout = checked(timeoutSeconds * 1000);
                state.Invoke(() => WinHttpSetTimeouts(state.Handle, timeout, timeout, timeout, timeout));
                string headers = "Accept: application/json\r\n";
                if (body != null && !HasContentType(requestHeaders)) headers += "Content-Type: application/json\r\n";
                if (authorization != null) headers += "Authorization: " + authorization + "\r\n";
                if (requestHeaders != null) foreach (KeyValuePair<string, string> header in requestHeaders) headers += header.Key + ": " + header.Value + "\r\n";
                IntPtr input = IntPtr.Zero;
                if (body != null && body.Length != 0) { bodyPin = GCHandle.Alloc(body, GCHandleType.Pinned); input = bodyPin.AddrOfPinnedObject(); }
                state.Invoke(() => WinHttpSendRequest(state.Handle, headers, (uint)headers.Length, input, (uint)(body?.Length ?? 0), (uint)(body?.Length ?? 0), UIntPtr.Zero));
                state.Wait(0x400000, token);
                state.Invoke(() => WinHttpReceiveResponse(state.Handle, IntPtr.Zero));
                state.Wait(0x20000, token);
                uint status = 0, size = 4;
                state.Invoke(() => WinHttpQueryHeaders(state.Handle, 19 | 0x20000000, null, out status, ref size, IntPtr.Zero));
                Dictionary<string, string> responseHeaders = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
                size = 0;
                lock (state.Gate)
                {
                    token.ThrowIfCancellationRequested();
                    WinHttpQueryHeadersText(state.Handle, 22, null, IntPtr.Zero, ref size, IntPtr.Zero);
                }
                if (size > 65536) return Failure("Control response headers exceeded limit");
                if (size > 0)
                {
                    int capacity = (int)size;
                    IntPtr buffer = Marshal.AllocHGlobal(capacity);
                    try
                    {
                        state.Invoke(() => WinHttpQueryHeadersText(state.Handle, 22, null, buffer, ref size, IntPtr.Zero));
                        foreach (string line in (Marshal.PtrToStringUni(buffer) ?? "").Split(new[] { "\r\n" }, StringSplitOptions.None)) KeepResponseHeader(responseHeaders, line);
                    }
                    finally { for (int i = 0; i < capacity; i++) Marshal.WriteByte(buffer, i, 0); Marshal.FreeHGlobal(buffer); }
                }
                byte[] readBuffer = new byte[16384];
                readPin = GCHandle.Alloc(readBuffer, GCHandleType.Pinned);
                using (MemoryStream response = new MemoryStream())
                {
                    try
                    {
                        while (true)
                        {
                            state.Invoke(() => WinHttpReadData(state.Handle, readPin.AddrOfPinnedObject(), (uint)readBuffer.Length, IntPtr.Zero));
                            state.Wait(0x80000, token);
                            if (state.Read == 0) break;
                            if (response.Length + state.Read > maxResponseBytes) return Failure("Control response exceeded limit");
                            response.Write(readBuffer, 0, (int)state.Read);
                        }
                        token.ThrowIfCancellationRequested();
                        byte[] data = response.ToArray();
                        reusable = true;
                        return new Response { StatusCode = (int)status, Data = data, Body = Encoding.UTF8.GetString(data), Headers = responseHeaders, RefreshInvalid = IsRefreshInvalid(responseHeaders) };
                    }
                    finally { if (response.TryGetBuffer(out ArraySegment<byte> used)) Array.Clear(used.Array, used.Offset, used.Count); }
                }
            }
        }
        catch (System.ComponentModel.Win32Exception error)
        {
            token.ThrowIfCancellationRequested();
            return new Response { Error = "WinHTTP error " + error.NativeErrorCode, NativeErrorCode = error.NativeErrorCode, FailureKind = "Transport" };
        }
        finally
        {
            if (state.Handle != IntPtr.Zero)
            {
                state.Close();
                if (callbackInstalled) state.Closed.WaitOne();
                lock (PoolLock)
                    if (WindowsRequests.TryGetValue(state.Handle, out WindowsRequest current) && ReferenceEquals(current, state))
                        WindowsRequests.Remove(state.Handle);
            }
            if (bodyPin.IsAllocated) bodyPin.Free();
            if (readPin.IsAllocated) { byte[] buffer = (byte[])readPin.Target; Array.Clear(buffer, 0, buffer.Length); readPin.Free(); }
            state.Ready.Dispose(); state.Closed.Dispose();
            ReleaseWindows(pool, reusable);
        }
    }

    [UnmanagedFunctionPointer(CallingConvention.Winapi)] private delegate void WinHttpCallback(IntPtr handle, UIntPtr context, uint status, IntPtr information, uint length);
    [DllImport("winhttp.dll", SetLastError = true)] private static extern IntPtr WinHttpSetStatusCallback(IntPtr handle, WinHttpCallback callback, uint flags, UIntPtr reserved);
    [DllImport("winhttp.dll", CharSet = CharSet.Unicode, SetLastError = true)] private static extern IntPtr WinHttpOpen(string agent, uint access, string proxy, string bypass, uint flags);
    [DllImport("winhttp.dll", CharSet = CharSet.Unicode, SetLastError = true)] private static extern IntPtr WinHttpConnect(IntPtr session, string server, ushort port, uint reserved);
    [DllImport("winhttp.dll", CharSet = CharSet.Unicode, SetLastError = true)] private static extern IntPtr WinHttpOpenRequest(IntPtr connection, string verb, string path, string version, string referer, IntPtr acceptTypes, uint flags);
    [DllImport("winhttp.dll", SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] private static extern bool WinHttpSetTimeouts(IntPtr handle, int resolve, int connect, int send, int receive);
    [DllImport("winhttp.dll", SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] private static extern bool WinHttpSetOption(IntPtr handle, uint option, ref uint value, uint size);
    [DllImport("winhttp.dll", CharSet = CharSet.Unicode, SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] private static extern bool WinHttpSendRequest(IntPtr request, string headers, uint headersLength, IntPtr body, uint bodyLength, uint totalLength, UIntPtr context);
    [DllImport("winhttp.dll", SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] private static extern bool WinHttpReceiveResponse(IntPtr request, IntPtr reserved);
    [DllImport("winhttp.dll", CharSet = CharSet.Unicode, SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] private static extern bool WinHttpQueryHeaders(IntPtr request, uint info, string name, out uint value, ref uint size, IntPtr index);
    [DllImport("winhttp.dll", EntryPoint = "WinHttpQueryHeaders", CharSet = CharSet.Unicode, SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] private static extern bool WinHttpQueryHeadersText(IntPtr request, uint info, string name, IntPtr value, ref uint size, IntPtr index);
    [DllImport("winhttp.dll", SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)] private static extern bool WinHttpReadData(IntPtr request, IntPtr buffer, uint capacity, IntPtr read);
    [DllImport("winhttp.dll")] [return: MarshalAs(UnmanagedType.Bool)] private static extern bool WinHttpCloseHandle(IntPtr handle);
}
