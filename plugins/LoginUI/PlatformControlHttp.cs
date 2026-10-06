using System;
using System.Collections.Generic;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading;
using System.Threading.Tasks;

namespace Bd2LoginUI;

// Own-origin control requests use OS TLS without Mono/Unity's TLS implementation.
internal static partial class PlatformControlHttp
{
    public static bool IsSupported => RuntimeInformation.IsOSPlatform(OSPlatform.Windows) ||
        RuntimeInformation.IsOSPlatform(OSPlatform.OSX);
    private static readonly object CurlInitializationLock = new();
    private static bool curlInitialized;
    internal sealed class Response
    {
        public int StatusCode;
        public string Body;
        public string Error;
        public byte[] Data;
        public IReadOnlyDictionary<string, string> Headers;
        public bool RefreshInvalid;
        public long ElapsedMilliseconds;
        public long RequestId;
        public long QueueMilliseconds;
        public long TransportMilliseconds;
        public string FailureKind;
        public int NativeErrorCode;
        public bool Success => Error == null && StatusCode >= 200 && StatusCode < 300;
    }

    private static readonly SemaphoreSlim NativeSlots = new(16, 16);
    public static async Task<Response> Send(Uri uri, string method, byte[] body, string authorization,
        int timeoutSeconds, CancellationToken lifetime, CancellationToken generation, int maxResponseBytes = 16 * 1024,
        IReadOnlyDictionary<string, string> requestHeaders = null)
    {
        var elapsed = System.Diagnostics.Stopwatch.StartNew();
        long requestId = Interlocked.Increment(ref nextRequestId);
        long admittedAt = -1;
        Response completed = null;
        try
        {
            if (timeoutSeconds <= 0 || timeoutSeconds > 60) return completed = Failure("Invalid control request");
            using var admission = CancellationTokenSource.CreateLinkedTokenSource(lifetime, generation, ShutdownCancellation.Token);
            admission.CancelAfter(TimeSpan.FromSeconds(timeoutSeconds));
            try { await NativeSlots.WaitAsync(admission.Token).ConfigureAwait(false); }
            catch (OperationCanceledException)
            {
                bool owner = lifetime.IsCancellationRequested || generation.IsCancellationRequested || ShutdownCancellation.IsCancellationRequested;
                return completed = new Response { Error = owner ? "Native request owner canceled" : "Native request deadline exceeded", FailureKind = owner ? "OwnerCanceled" : "DeadlineExceeded" };
            }
            admittedAt = elapsed.ElapsedMilliseconds;
            try
            {
                // At most sixteen dedicated workers exist. Admission awaits without
                // occupying any worker, and native event waits never starve timer/UI
                // or relay tasks sharing the managed thread pool.
                return completed = await SendWorker(uri, method, body, authorization, timeoutSeconds, maxResponseBytes, requestHeaders, elapsed, lifetime, generation).ConfigureAwait(false);
            }
            finally { NativeSlots.Release(); }
        }
        finally
        {
            // The complete native transaction includes server wait; it is not a bandwidth measurement.
            if (completed != null)
            {
                completed.RequestId = requestId;
                completed.ElapsedMilliseconds = elapsed.ElapsedMilliseconds;
                completed.QueueMilliseconds = admittedAt < 0 ? completed.ElapsedMilliseconds : admittedAt;
                completed.TransportMilliseconds = admittedAt < 0 ? 0 : completed.ElapsedMilliseconds - admittedAt;
                LogCompletion(uri, method, completed);
            }
        }
    }

    private static Task<Response> SendWorker(Uri uri, string method, byte[] body, string authorization,
        int timeoutSeconds, int maxResponseBytes, IReadOnlyDictionary<string, string> requestHeaders,
        System.Diagnostics.Stopwatch elapsed, CancellationToken lifetime, CancellationToken generation)
    {
        return Task.Factory.StartNew(() =>
        {
            using var cancellation = CancellationTokenSource.CreateLinkedTokenSource(lifetime, generation, ShutdownCancellation.Token);
            try
            {
                if (uri == null || !uri.IsAbsoluteUri ||
                    (uri.Scheme != Uri.UriSchemeHttps && !(uri.Scheme == Uri.UriSchemeHttp && uri.IsLoopback)) ||
                    !string.IsNullOrEmpty(uri.UserInfo) || !string.IsNullOrEmpty(uri.Fragment) ||
                    (method != "GET" && method != "POST" && method != "PUT") || timeoutSeconds <= 0 || timeoutSeconds > 60 ||
                    maxResponseBytes <= 0 || maxResponseBytes > 64 * 1024 * 1024 ||
                    (body != null && body.Length > 64 * 1024 * 1024) ||
                    (authorization != null && (authorization.Length > 16384 || authorization.Contains('\r') || authorization.Contains('\n'))))
                    return Timed(Failure("Invalid control request"), elapsed);
                long budget = timeoutSeconds * 1000L - elapsed.ElapsedMilliseconds;
                if (budget <= 0) throw new OperationCanceledException();
                cancellation.CancelAfter(TimeSpan.FromMilliseconds(budget));
                cancellation.Token.ThrowIfCancellationRequested();
                if (requestHeaders != null)
                {
                    if (requestHeaders.Count > 64) return Timed(Failure("Invalid control request headers"), elapsed);
                    int headerSize = 0;
                    foreach (KeyValuePair<string, string> header in requestHeaders)
                    {
                        if (!AllowedRequestHeader(header.Key) || header.Key.Length > 256 || !ValidHeaderName(header.Key) || header.Value == null || header.Value.Length > 16384 ||
                            header.Value.Contains('\r') || header.Value.Contains('\n'))
                            return Timed(Failure("Invalid control request headers"), elapsed);
                        headerSize += header.Key.Length + header.Value.Length;
                        if (headerSize > 65536) return Timed(Failure("Invalid control request headers"), elapsed);
                    }
                }
                if (RuntimeInformation.IsOSPlatform(OSPlatform.Windows))
                    return Timed(SendWindows(uri, method, body, authorization, timeoutSeconds, maxResponseBytes, requestHeaders, cancellation.Token), elapsed);
                if (RuntimeInformation.IsOSPlatform(OSPlatform.OSX))
                    return Timed(SendMac(uri, method, body, authorization, timeoutSeconds, maxResponseBytes, requestHeaders, elapsed, cancellation.Token), elapsed);
                return Timed(Failure("Native control transport is unsupported"), elapsed);
            }
            catch (OperationCanceledException)
            {
                bool owner = lifetime.IsCancellationRequested || generation.IsCancellationRequested || ShutdownCancellation.IsCancellationRequested;
                return Timed(new Response { Error = owner ? "Native request owner canceled" : "Native request deadline exceeded", FailureKind = owner ? "OwnerCanceled" : "DeadlineExceeded" }, elapsed);
            }
            catch (NativeProxyConfigurationException exception) { return Timed(new Response { Error = exception.Message, FailureKind = "ProxyConfiguration" }, elapsed); }
            catch (Exception exception) { return Timed(Failure("Native control transport failed: " + exception.GetType().Name), elapsed); }
        }, CancellationToken.None, TaskCreationOptions.LongRunning, TaskScheduler.Default);
    }

    private static Response Timed(Response response, System.Diagnostics.Stopwatch elapsed) { response.ElapsedMilliseconds = elapsed.ElapsedMilliseconds; return response; }
    private static Response Failure(string error) => new() { Error = error, FailureKind = "Transport" };

    private static Response SendMac(Uri uri, string method, byte[] body, string authorization,
        int timeoutSeconds, int maxResponseBytes, IReadOnlyDictionary<string, string> requestHeaders, System.Diagnostics.Stopwatch elapsed, CancellationToken token)
    {
        IntPtr curl = IntPtr.Zero, headers = IntPtr.Zero, pinnedBody = IntPtr.Zero;
        CurlPool pool = null;
        bool reusable = false;
        using var response = new MemoryStream();
        bool exceeded = false;
        int headerBytes = 0, headerCount = 0;
        var responseHeaders = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        CurlWrite write = (data, size, count, context) =>
        {
            try
            {
                ulong length = checked(size.ToUInt64() * count.ToUInt64());
                if (token.IsCancellationRequested || length > (ulong)maxResponseBytes || response.Length + (long)length > maxResponseBytes)
                { exceeded = !token.IsCancellationRequested; return UIntPtr.Zero; }
                byte[] bytes = new byte[(int)length];
                try { Marshal.Copy(data, bytes, 0, bytes.Length); response.Write(bytes, 0, bytes.Length); }
                finally { Array.Clear(bytes, 0, bytes.Length); }
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
                try { Marshal.Copy(data, bytes, 0, bytes.Length); KeepResponseHeader(responseHeaders, Encoding.UTF8.GetString(bytes).TrimEnd('\r', '\n')); }
                finally { Array.Clear(bytes, 0, bytes.Length); }
                return new UIntPtr(length);
            }
            catch { return UIntPtr.Zero; }
        };
        try
        {
            // System libcurl is built with the OS TLS backend and its default trust store.
            if (!EnsureCurlInitialized()) return Failure("Could not initialize system curl");
            NativeProxySettings proxy = NativeProxyPolicy.Resolve(uri);
            token.ThrowIfCancellationRequested();
            curl = AcquireCurl(uri, proxy, token, out pool);
            curl_easy_reset(curl);
            SetCurl(curl, 10004, proxy.ProxyUrl ?? string.Empty); // explicitly suppress environment proxies for direct routes
            SetCurl(curl, 10177, string.Empty); // policy already applied destination bypass
            if (curl == IntPtr.Zero) return Failure("Could not create system curl request");
            SetCurl(curl, 10002, uri.AbsoluteUri);
            SetCurl(curl, 10036, method);
            SetCurl(curl, 52, 0L); // never follow redirects
            SetCurl(curl, 64, 1L); // verify peer chain
            SetCurl(curl, 81, 2L); // verify hostname
            SetCurl(curl, 99, 1L); // no signals on worker threads
            long remaining = timeoutSeconds * 1000L - elapsed.ElapsedMilliseconds;
            if (remaining <= 0) throw new OperationCanceledException();
            SetCurl(curl, 155, remaining);
            SetCurl(curl, 156, remaining);
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
            if (result != 0) return new Response { Error = exceeded ? "Control response exceeded limit" : "System curl error " + result, NativeErrorCode = result, FailureKind = "Transport" };
            int infoResult = RuntimeInformation.ProcessArchitecture == Architecture.Arm64
                ? curl_easy_getinfo_arm64(curl, 0x200002, 0, 0, 0, 0, 0, 0, out long status)
                : curl_easy_getinfo(curl, 0x200002, out status);
            if (infoResult != 0) return Failure("Could not read system curl status");
            byte[] data = response.ToArray();
            reusable = true;
            Array.Clear(response.GetBuffer(), 0, (int)response.Length);
            return new Response
            {
                StatusCode = (int)status,
                Data = data,
                Headers = responseHeaders,
                Body = Encoding.UTF8.GetString(data),
                RefreshInvalid = IsRefreshInvalid(responseHeaders)
            };
        }
        finally
        {
            // Reset while callbacks and body/header storage still exist. Reset keeps
            // only libcurl connection/DNS caches, clearing request secrets and state.
            if (curl != IntPtr.Zero) { curl_easy_reset(curl); ReleaseCurl(pool, curl, reusable); }
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

    private static void KeepResponseHeader(Dictionary<string, string> headers, string line)
    {
        int separator = line.IndexOf(':');
        if (separator <= 0) return;
        string name = line[..separator].Trim();
        if (name.Equals("Content-Type", StringComparison.OrdinalIgnoreCase) || name.Equals("Set-Cookie", StringComparison.OrdinalIgnoreCase) ||
            name.StartsWith("X-BD2-", StringComparison.OrdinalIgnoreCase))
            headers[name] = line[(separator + 1)..].Trim();
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
    [DllImport(Curl, CallingConvention = CallingConvention.Cdecl)] private static extern void curl_easy_reset(IntPtr curl);
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
    [DllImport(Curl, EntryPoint = "curl_easy_setopt", CallingConvention = CallingConvention.Cdecl, BestFitMapping = false)] private static extern int curl_easy_setopt_string(IntPtr curl, int option, [MarshalAs(UnmanagedType.LPUTF8Str)] string value);
    [DllImport(Curl, EntryPoint = "curl_easy_setopt", CallingConvention = CallingConvention.Cdecl)] private static extern int curl_easy_setopt_write(IntPtr curl, int option, CurlWrite value);
    [DllImport(Curl, EntryPoint = "curl_easy_setopt", CallingConvention = CallingConvention.Cdecl)] private static extern int curl_easy_setopt_progress(IntPtr curl, int option, CurlProgress value);
    [DllImport(Curl, CallingConvention = CallingConvention.Cdecl, BestFitMapping = false)] private static extern IntPtr curl_slist_append(IntPtr list, [MarshalAs(UnmanagedType.LPUTF8Str)] string value);
    [DllImport(Curl, CallingConvention = CallingConvention.Cdecl)] private static extern void curl_slist_free_all(IntPtr list);
}
