using System;
using System.Collections.Generic;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading;
using System.Threading.Tasks;

namespace Bd2LoginUI;

// Own-origin control requests use OS TLS without Mono/Unity's TLS implementation.
internal static class PlatformControlHttp
{
    public static bool IsSupported => RuntimeInformation.IsOSPlatform(OSPlatform.Windows) ||
        RuntimeInformation.IsOSPlatform(OSPlatform.OSX);
    private static readonly object CurlInitializationLock = new object();
    private static bool curlInitialized;
    internal sealed class Response
    {
        public int StatusCode;
        public string Body;
        public string Error;
        public byte[] Data;
        public IReadOnlyDictionary<string, string> Headers;
        public bool RefreshInvalid;
        public bool Success => Error == null && StatusCode >= 200 && StatusCode < 300;
    }

    public static Task<Response> Send(Uri uri, string method, byte[] body, string authorization,
        int timeoutSeconds, CancellationToken lifetime, CancellationToken generation, int maxResponseBytes = 16 * 1024,
        IReadOnlyDictionary<string, string> requestHeaders = null)
    {
        return Task.Run(() =>
        {
            using (CancellationTokenSource cancellation = CancellationTokenSource.CreateLinkedTokenSource(lifetime, generation))
            {
                try
                {
                    if (uri == null || !uri.IsAbsoluteUri ||
                        (uri.Scheme != Uri.UriSchemeHttps && !(uri.Scheme == Uri.UriSchemeHttp && uri.IsLoopback)) ||
                        !string.IsNullOrEmpty(uri.UserInfo) || !string.IsNullOrEmpty(uri.Fragment) ||
                        (method != "GET" && method != "POST" && method != "PUT") || timeoutSeconds <= 0 || timeoutSeconds > 60 ||
                        maxResponseBytes <= 0 || maxResponseBytes > 64 * 1024 * 1024 ||
                        (body != null && body.Length > 64 * 1024 * 1024) ||
                        (authorization != null && (authorization.Length > 16384 || authorization.IndexOf('\r') >= 0 || authorization.IndexOf('\n') >= 0)))
                        return Failure("Invalid control request");
                    cancellation.CancelAfter(TimeSpan.FromSeconds(timeoutSeconds));
                    cancellation.Token.ThrowIfCancellationRequested();
                    if (requestHeaders != null)
                    {
                        if (requestHeaders.Count > 64) return Failure("Invalid control request headers");
                        int headerSize = 0;
                        foreach (KeyValuePair<string, string> header in requestHeaders)
                        {
                            if (!AllowedRequestHeader(header.Key) || header.Key.Length > 256 || !ValidHeaderName(header.Key) || header.Value == null || header.Value.Length > 16384 ||
                                header.Value.IndexOf('\r') >= 0 || header.Value.IndexOf('\n') >= 0)
                                return Failure("Invalid control request headers");
                            headerSize += header.Key.Length + header.Value.Length;
                            if (headerSize > 65536) return Failure("Invalid control request headers");
                        }
                    }
                    if (RuntimeInformation.IsOSPlatform(OSPlatform.Windows))
                        return SendWindows(uri, method, body, authorization, timeoutSeconds, cancellation.Token, maxResponseBytes, requestHeaders);
                    if (RuntimeInformation.IsOSPlatform(OSPlatform.OSX))
                        return SendMac(uri, method, body, authorization, timeoutSeconds, cancellation.Token, maxResponseBytes, requestHeaders);
                    return Failure("Native control transport is unsupported");
                }
                catch (OperationCanceledException) { return Failure("Control request canceled"); }
                catch (Exception exception) { return Failure("Native control transport failed: " + exception.GetType().Name); }
            }
        });
    }

    private static Response Failure(string error) => new Response { Error = error };

    private static Response SendWindows(Uri uri, string method, byte[] body, string authorization,
        int timeoutSeconds, CancellationToken token, int maxResponseBytes, IReadOnlyDictionary<string, string> requestHeaders)
    {
        IntPtr session = IntPtr.Zero, connection = IntPtr.Zero, request = IntPtr.Zero;
        try
        {
            // Owned endpoints connect directly; browser OAuth keeps its own proxy settings.
            session = WinHttpOpen("BD2LoginUI", 1, null, null, 0); // WINHTTP_ACCESS_TYPE_NO_PROXY
            if (session == IntPtr.Zero) return WindowsFailure();
            int timeout = checked(timeoutSeconds * 1000);
            if (!WinHttpSetTimeouts(session, timeout, timeout, timeout, timeout)) return WindowsFailure();
            connection = WinHttpConnect(session, uri.IdnHost, (ushort)uri.Port, 0);
            if (connection == IntPtr.Zero) return WindowsFailure();
            request = WinHttpOpenRequest(connection, method, uri.PathAndQuery, null, null, IntPtr.Zero,
                uri.Scheme == Uri.UriSchemeHttps ? 0x00800000u : 0u);
            if (request == IntPtr.Zero) return WindowsFailure();
            uint disabled = 0x2 | 0x4; // WINHTTP_DISABLE_COOKIES | WINHTTP_DISABLE_REDIRECTS
            if (!WinHttpSetOption(request, 63, ref disabled, 4)) return WindowsFailure();
            string headers = "Accept: application/json\r\n";
            if (body != null && !HasContentType(requestHeaders)) headers += "Content-Type: application/json\r\n";
            if (authorization != null) headers += "Authorization: " + authorization + "\r\n";
            if (requestHeaders != null)
                foreach (KeyValuePair<string, string> header in requestHeaders) headers += header.Key + ": " + header.Value + "\r\n";
            token.ThrowIfCancellationRequested();
            // Calls have native timeouts; never close a handle concurrently with a P/Invoke call.
            if (!WinHttpSendRequest(request, headers, (uint)headers.Length, body,
                (uint)(body?.Length ?? 0), (uint)(body?.Length ?? 0), UIntPtr.Zero)) return WindowsFailure();
            token.ThrowIfCancellationRequested();
            if (!WinHttpReceiveResponse(request, IntPtr.Zero)) return WindowsFailure();
            uint status, size = 4;
            if (!WinHttpQueryHeaders(request, 19 | 0x20000000, null, out status, ref size, IntPtr.Zero)) return WindowsFailure();
            Dictionary<string, string> responseHeaders = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
            size = 0;
            WinHttpQueryHeadersText(request, 22, null, IntPtr.Zero, ref size, IntPtr.Zero);
            if (size > 64 * 1024) return Failure("Control response headers exceeded limit");
            if (size > 0 && size <= 64 * 1024)
            {
                int headerCapacity = (int)size;
                IntPtr headerBuffer = Marshal.AllocHGlobal((int)size);
                try
                {
                    if (WinHttpQueryHeadersText(request, 22, null, headerBuffer, ref size, IntPtr.Zero))
                        foreach (string line in (Marshal.PtrToStringUni(headerBuffer) ?? string.Empty).Split(new[] { "\r\n" }, StringSplitOptions.None))
                            KeepResponseHeader(responseHeaders, line);
                }
                finally
                {
                    for (int index = 0; index < headerCapacity; index++) Marshal.WriteByte(headerBuffer, index, 0);
                    Marshal.FreeHGlobal(headerBuffer);
                }
            }
            using (MemoryStream response = new MemoryStream())
            {
                byte[] buffer = new byte[4096];
                try
                {
                while (true)
                {
                    token.ThrowIfCancellationRequested();
                    uint read;
                    if (!WinHttpReadData(request, buffer, (uint)buffer.Length, out read)) return WindowsFailure();
                    if (read == 0) break;
                    if (response.Length + read > maxResponseBytes) return Failure("Control response exceeded limit");
                    response.Write(buffer, 0, (int)read);
                }
                token.ThrowIfCancellationRequested();
                byte[] data = response.ToArray();
                Array.Clear(response.GetBuffer(), 0, (int)response.Length);
                Array.Clear(buffer, 0, buffer.Length);
                return new Response { StatusCode = (int)status, Data = data, Headers = responseHeaders, Body = Encoding.UTF8.GetString(data),
                    RefreshInvalid = IsRefreshInvalid(responseHeaders) };
                }
                finally
                {
                    Array.Clear(buffer, 0, buffer.Length);
                    if (response.TryGetBuffer(out ArraySegment<byte> used)) Array.Clear(used.Array, used.Offset, used.Count);
                }
            }
        }
        finally
        {
            if (request != IntPtr.Zero) WinHttpCloseHandle(request);
            if (connection != IntPtr.Zero) WinHttpCloseHandle(connection);
            if (session != IntPtr.Zero) WinHttpCloseHandle(session);
        }
    }

    private static Response WindowsFailure() => Failure("WinHTTP error " + Marshal.GetLastWin32Error());

    [DllImport("winhttp.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern IntPtr WinHttpOpen(string agent, uint access, string proxy, string bypass, uint flags);
    [DllImport("winhttp.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern IntPtr WinHttpConnect(IntPtr session, string server, ushort port, uint reserved);
    [DllImport("winhttp.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern IntPtr WinHttpOpenRequest(IntPtr connection, string verb, string path, string version,
        string referer, IntPtr acceptTypes, uint flags);
    [DllImport("winhttp.dll", SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool WinHttpSetTimeouts(IntPtr handle, int resolve, int connect, int send, int receive);
    [DllImport("winhttp.dll", SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool WinHttpSetOption(IntPtr handle, uint option, ref uint value, uint size);
    [DllImport("winhttp.dll", CharSet = CharSet.Unicode, SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool WinHttpSendRequest(IntPtr request, string headers, uint headersLength, byte[] body,
        uint bodyLength, uint totalLength, UIntPtr context);
    [DllImport("winhttp.dll", SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool WinHttpReceiveResponse(IntPtr request, IntPtr reserved);
    [DllImport("winhttp.dll", CharSet = CharSet.Unicode, SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool WinHttpQueryHeaders(IntPtr request, uint info, string name, out uint value, ref uint size, IntPtr index);
    [DllImport("winhttp.dll", EntryPoint = "WinHttpQueryHeaders", CharSet = CharSet.Unicode, SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool WinHttpQueryHeadersText(IntPtr request, uint info, string name, IntPtr value, ref uint size, IntPtr index);
    [DllImport("winhttp.dll", SetLastError = true)] [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool WinHttpReadData(IntPtr request, [Out] byte[] buffer, uint capacity, out uint read);
    [DllImport("winhttp.dll")] [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool WinHttpCloseHandle(IntPtr handle);

    private static Response SendMac(Uri uri, string method, byte[] body, string authorization,
        int timeoutSeconds, CancellationToken token, int maxResponseBytes, IReadOnlyDictionary<string, string> requestHeaders)
    {
        IntPtr curl = IntPtr.Zero, headers = IntPtr.Zero, pinnedBody = IntPtr.Zero;
        using (MemoryStream response = new MemoryStream())
        {
            bool exceeded = false;
            int headerBytes = 0, headerCount = 0;
            Dictionary<string, string> responseHeaders = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
            CurlWrite write = (data, size, count, context) =>
            {
                try
                {
                    ulong length = checked(size.ToUInt64() * count.ToUInt64());
                    if (token.IsCancellationRequested || length > (ulong)maxResponseBytes || response.Length + (long)length > maxResponseBytes)
                    { exceeded = !token.IsCancellationRequested; return UIntPtr.Zero; }
                    byte[] bytes = new byte[(int)length];
                    Marshal.Copy(data, bytes, 0, bytes.Length);
                    response.Write(bytes, 0, bytes.Length);
                    return new UIntPtr(length);
                }
                catch { return UIntPtr.Zero; }
            };
            CurlProgress progress = (context, downloadTotal, downloadNow, uploadTotal, uploadNow) => token.IsCancellationRequested ? 1 : 0;
            CurlWrite header = (data, size, count, context) =>
            {
                try
                {
                    ulong length = checked(size.ToUInt64() * count.ToUInt64());
                    if (length > 16384 || token.IsCancellationRequested || headerBytes + (long)length > 65536 || ++headerCount > 256)
                        return UIntPtr.Zero;
                    headerBytes += (int)length;
                    byte[] bytes = new byte[(int)length];
                    Marshal.Copy(data, bytes, 0, bytes.Length);
                    KeepResponseHeader(responseHeaders, Encoding.UTF8.GetString(bytes).TrimEnd('\r', '\n'));
                    return new UIntPtr(length);
                }
                catch { return UIntPtr.Zero; }
            };
            try
            {
                // System libcurl is built with the OS TLS backend and its default trust store.
                if (!EnsureCurlInitialized()) return Failure("Could not initialize system curl");
                curl = curl_easy_init();
                if (curl == IntPtr.Zero) return Failure("Could not create system curl request");
                SetCurl(curl, 10002, uri.AbsoluteUri);
                SetCurl(curl, 10036, method);
                SetCurl(curl, 52, 0L); // never follow redirects
                SetCurl(curl, 64, 1L); // verify peer chain
                SetCurl(curl, 81, 2L); // verify hostname
                SetCurl(curl, 99, 1L); // no signals on worker threads
                SetCurl(curl, 155, checked(timeoutSeconds * 1000L));
                SetCurl(curl, 156, checked(timeoutSeconds * 1000L));
                SetCurl(curl, 181, uri.Scheme == Uri.UriSchemeHttps ? 2L : 3L);
                SetCurl(curl, 182, 2L); // redirects are disabled
                SetCurl(curl, 10018, "BD2LoginUI");
                headers = AddCurlHeader(headers, "Accept: application/json");
                if (authorization != null) headers = AddCurlHeader(headers, "Authorization: " + authorization);
                if (requestHeaders != null)
                    foreach (KeyValuePair<string, string> item in requestHeaders) headers = AddCurlHeader(headers, item.Key + ": " + item.Value);
                if (body != null)
                {
                    if (!HasContentType(requestHeaders)) headers = AddCurlHeader(headers, "Content-Type: application/json");
                    pinnedBody = Marshal.AllocHGlobal(Math.Max(1, body.Length));
                    if (body.Length > 0) Marshal.Copy(body, 0, pinnedBody, body.Length);
                    SetCurl(curl, 10015, pinnedBody);
                    SetCurl(curl, 60, (long)body.Length);
                }
                SetCurl(curl, 10023, headers);
                SetCurl(curl, 20011, Marshal.GetFunctionPointerForDelegate(write));
                SetCurl(curl, 20079, Marshal.GetFunctionPointerForDelegate(header));
                SetCurl(curl, 20219, Marshal.GetFunctionPointerForDelegate(progress));
                SetCurl(curl, 43, 0L);
                token.ThrowIfCancellationRequested();
                int result = curl_easy_perform(curl);
                GC.KeepAlive(write);
                GC.KeepAlive(progress);
                GC.KeepAlive(header);
                token.ThrowIfCancellationRequested();
                if (result != 0) return Failure(exceeded ? "Control response exceeded limit" : "System curl error " + result);
                long status;
                int infoResult = RuntimeInformation.ProcessArchitecture == Architecture.Arm64
                    ? curl_easy_getinfo_arm64(curl, 0x200002, 0, 0, 0, 0, 0, 0, out status)
                    : curl_easy_getinfo(curl, 0x200002, out status);
                if (infoResult != 0) return Failure("Could not read system curl status");
                byte[] data = response.ToArray();
                Array.Clear(response.GetBuffer(), 0, (int)response.Length);
                return new Response { StatusCode = (int)status, Data = data, Headers = responseHeaders, Body = Encoding.UTF8.GetString(data),
                    RefreshInvalid = IsRefreshInvalid(responseHeaders) };
            }
            finally
            {
                if (curl != IntPtr.Zero) curl_easy_cleanup(curl);
                if (headers != IntPtr.Zero) curl_slist_free_all(headers);
                if (pinnedBody != IntPtr.Zero)
                {
                    for (int index = 0; index < (body?.Length ?? 0); index++) Marshal.WriteByte(pinnedBody, index, 0);
                    Marshal.FreeHGlobal(pinnedBody);
                }
                if (response.TryGetBuffer(out ArraySegment<byte> used)) Array.Clear(used.Array, used.Offset, used.Count);
                GC.KeepAlive(write);
                GC.KeepAlive(progress);
                GC.KeepAlive(header);
            }
        }
    }

    private static void KeepResponseHeader(Dictionary<string, string> headers, string line)
    {
        int separator = line.IndexOf(':');
        if (separator <= 0) return;
        string name = line.Substring(0, separator).Trim();
        if (name.Equals("Content-Type", StringComparison.OrdinalIgnoreCase) || name.Equals("Set-Cookie", StringComparison.OrdinalIgnoreCase) ||
            name.StartsWith("X-BD2-", StringComparison.OrdinalIgnoreCase))
            headers[name] = line.Substring(separator + 1).Trim();
    }

    private static bool HasContentType(IReadOnlyDictionary<string, string> headers)
    {
        if (headers == null) return false;
        foreach (string name in headers.Keys)
            if (name.Equals("Content-Type", StringComparison.OrdinalIgnoreCase)) return true;
        return false;
    }

    private static bool IsRefreshInvalid(Dictionary<string, string> headers) =>
        headers.TryGetValue("X-BD2-Refresh-Invalid", out string value) && value == "1";

    private static bool AllowedRequestHeader(string name) => name != null &&
        (name.Equals("Content-Type", StringComparison.OrdinalIgnoreCase) || name.Equals("Cookie", StringComparison.OrdinalIgnoreCase) ||
         name.Equals("b2_app_version", StringComparison.OrdinalIgnoreCase) || name.Equals("b2_bundle_version", StringComparison.OrdinalIgnoreCase) ||
         name.Equals("market_type", StringComparison.OrdinalIgnoreCase) || name.StartsWith("X-BD2-", StringComparison.OrdinalIgnoreCase));

    private static bool ValidHeaderName(string name)
    {
        foreach (char character in name)
            if (!((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
                (character >= '0' && character <= '9') || character == '-' || character == '_')) return false;
        return true;
    }

    private static IntPtr AddCurlHeader(IntPtr list, string header)
    {
        IntPtr next = curl_slist_append(list, header);
        if (next == IntPtr.Zero) throw new InvalidOperationException("Could not create native request headers");
        return next;
    }
    private static bool EnsureCurlInitialized()
    {
        lock (CurlInitializationLock)
        {
            if (curlInitialized) return true;
            if (curl_global_init(3) != 0) return false;
            curlInitialized = true;
            return true;
        }
    }
    private static void SetCurl(IntPtr curl, int option, long value)
    {
        int result = RuntimeInformation.ProcessArchitecture == Architecture.Arm64
            ? curl_easy_setopt_arm64_long(curl, option, 0, 0, 0, 0, 0, 0, value)
            : curl_easy_setopt_long(curl, option, value);
        if (result != 0) throw new InvalidOperationException("System curl option failed");
    }
    private static void SetCurl(IntPtr curl, int option, string value)
    {
        if (RuntimeInformation.ProcessArchitecture != Architecture.Arm64)
        {
            if (curl_easy_setopt_string(curl, option, value) != 0) throw new InvalidOperationException("System curl option failed");
            return;
        }
        byte[] utf8 = Encoding.UTF8.GetBytes(value + "\0");
        IntPtr pointer = Marshal.AllocHGlobal(utf8.Length);
        try { Marshal.Copy(utf8, 0, pointer, utf8.Length); SetCurl(curl, option, pointer); }
        finally { Array.Clear(utf8, 0, utf8.Length); Marshal.FreeHGlobal(pointer); }
    }
    private static void SetCurl(IntPtr curl, int option, IntPtr value)
    {
        int result = RuntimeInformation.ProcessArchitecture == Architecture.Arm64
            ? curl_easy_setopt_arm64_pointer(curl, option, 0, 0, 0, 0, 0, 0, value)
            : curl_easy_setopt_pointer(curl, option, value);
        if (result != 0) throw new InvalidOperationException("System curl option failed");
    }
    private const string Curl = "/usr/lib/libcurl.dylib";
    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate UIntPtr CurlWrite(IntPtr data, UIntPtr size, UIntPtr count, IntPtr context);
    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate int CurlProgress(IntPtr context, long downloadTotal, long downloadNow, long uploadTotal, long uploadNow);
    [DllImport(Curl, CallingConvention = CallingConvention.Cdecl)] private static extern int curl_global_init(long flags);
    [DllImport(Curl, CallingConvention = CallingConvention.Cdecl)] private static extern IntPtr curl_easy_init();
    [DllImport(Curl, CallingConvention = CallingConvention.Cdecl)] private static extern void curl_easy_cleanup(IntPtr curl);
    [DllImport(Curl, CallingConvention = CallingConvention.Cdecl)] private static extern int curl_easy_perform(IntPtr curl);
    [DllImport(Curl, CallingConvention = CallingConvention.Cdecl)] private static extern int curl_easy_getinfo(IntPtr curl, int info, out long value);
    [DllImport(Curl, EntryPoint = "curl_easy_setopt", CallingConvention = CallingConvention.Cdecl)] private static extern int curl_easy_setopt_long(IntPtr curl, int option, long value);
    [DllImport(Curl, EntryPoint = "curl_easy_setopt", CallingConvention = CallingConvention.Cdecl)] private static extern int curl_easy_setopt_pointer(IntPtr curl, int option, IntPtr value);
    // Apple's arm64 variadic ABI puts unnamed arguments on the stack. Occupying
    // x2..x7 with unused arguments places the value at the first stack slot.
    [DllImport(Curl, EntryPoint = "curl_easy_setopt", CallingConvention = CallingConvention.Cdecl)]
    private static extern int curl_easy_setopt_arm64_long(IntPtr curl, int option, ulong x2, ulong x3, ulong x4, ulong x5, ulong x6, ulong x7, long value);
    [DllImport(Curl, EntryPoint = "curl_easy_setopt", CallingConvention = CallingConvention.Cdecl)]
    private static extern int curl_easy_setopt_arm64_pointer(IntPtr curl, int option, ulong x2, ulong x3, ulong x4, ulong x5, ulong x6, ulong x7, IntPtr value);
    [DllImport(Curl, EntryPoint = "curl_easy_getinfo", CallingConvention = CallingConvention.Cdecl)]
    private static extern int curl_easy_getinfo_arm64(IntPtr curl, int info, ulong x2, ulong x3, ulong x4, ulong x5, ulong x6, ulong x7, out long value);
    [DllImport(Curl, EntryPoint = "curl_easy_setopt", CallingConvention = CallingConvention.Cdecl)] private static extern int curl_easy_setopt_string(IntPtr curl, int option, [MarshalAs(UnmanagedType.LPUTF8Str)] string value);
    [DllImport(Curl, EntryPoint = "curl_easy_setopt", CallingConvention = CallingConvention.Cdecl)] private static extern int curl_easy_setopt_write(IntPtr curl, int option, CurlWrite value);
    [DllImport(Curl, EntryPoint = "curl_easy_setopt", CallingConvention = CallingConvention.Cdecl)] private static extern int curl_easy_setopt_progress(IntPtr curl, int option, CurlProgress value);
    [DllImport(Curl, CallingConvention = CallingConvention.Cdecl)] private static extern IntPtr curl_slist_append(IntPtr list, [MarshalAs(UnmanagedType.LPUTF8Str)] string value);
    [DllImport(Curl, CallingConvention = CallingConvention.Cdecl)] private static extern void curl_slist_free_all(IntPtr list);
}
