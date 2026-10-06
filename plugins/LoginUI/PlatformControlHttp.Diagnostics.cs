using System;
using System.Globalization;
using System.Threading;

namespace Bd2LoginUI;

internal static partial class PlatformControlHttp
{
    private static long nextRequestId;
    private static Action<string> completionLogger;
    internal static void SetCompletionLogger(Action<string> logger) => Interlocked.Exchange(ref completionLogger, logger);

    private static void LogCompletion(Uri uri, string method, Response response)
    {
        try
        {
            Action<string> logger = Volatile.Read(ref completionLogger);
            if (logger == null) return;
            string safeMethod = method == "GET" || method == "POST" || method == "PUT" ? method : "invalid";
            logger(string.Format(CultureInfo.InvariantCulture,
                "Native HTTP transaction: request_id={0} method={1} path={2} status={3} queue_ms={4} transport_ms={5} total_ms={6} response_bytes={7} failure={8} native_error={9} timing_scope=includes_server_wait",
                response.RequestId, safeMethod, DiagnosticPath(uri), response.StatusCode,
                response.QueueMilliseconds, response.TransportMilliseconds, response.ElapsedMilliseconds,
                response.Data?.LongLength ?? 0, response.FailureKind ?? (response.Error != null ? "Transport" : response.Success ? "none" : "HttpStatus"), response.NativeErrorCode));
        }
        catch { /* Logging must never change the request result. */ }
    }

    private static string DiagnosticPath(Uri uri)
    {
        if (uri == null || !uri.IsAbsoluteUri) return "invalid";
        string path = uri.AbsolutePath;
        switch (path)
        {
            case "/auth/config":
            case "/auth/device":
            case "/auth/session/refresh":
            case "/auth/session/revoke":
            case "/client/runtime":
            case "/client/resources":
            case "/readyz":
            case "/livez":
            case "/healthz":
            case "/StateCheckInfoJson":
            case "/logs": return path;
        }
        string[] segments = path.Split('/');
        if (segments.Length == 5 && segments[1] == "auth" && segments[2] == "device" && segments[4] == "poll")
            return "/auth/device/{id}/poll";
        if (segments.Length == 3 && segments[1] == "game" && DiagnosticGamePacket(segments[2])) return path;
        return "redacted";
    }

    // Game routes use static PascalCase packet names; identifiers are carried in the request body.
    private static bool DiagnosticGamePacket(string name)
    {
        if (name.Length == 0 || name.Length > 80 || name[0] < 'A' || name[0] > 'Z') return false;
        foreach (char value in name)
            if (!((value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9'))) return false;
        return true;
    }
}
