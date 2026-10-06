using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Security.Cryptography;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using BepInEx.Logging;

namespace Bd2LoginUI;

// Unity keeps its game packet parser; only the HTTPS transport runs through the OS.
// This listener binds loopback only and can forward game paths to one fixed origin.
internal sealed class SecureGameRelay : IDisposable
{
    private static readonly char[] HeaderNewlines = ['\r', '\n'];
    private static readonly string[] HeaderSeparators = ["\r\n"];
    private readonly Uri server;
    private readonly Uri local;
    private readonly string prefix;
    private readonly TcpListener listener;
    private readonly CancellationTokenSource lifetime = new();
    private readonly SemaphoreSlim slots = new(16, 16);
    private readonly ManualLogSource log;
    private int disposed;

    private SecureGameRelay(Uri server, ManualLogSource log)
    {
        if (server == null || server.Scheme != Uri.UriSchemeHttps || server.AbsolutePath != "/" ||
            server.UserInfo.Length != 0 || server.Query.Length != 0 || server.Fragment.Length != 0)
            throw new ArgumentException("Game relay requires a fixed HTTPS origin");
        this.server = server;
        this.log = log;
        byte[] nonce = new byte[32];
        using (var random = RandomNumberGenerator.Create()) random.GetBytes(nonce);
        prefix = "/" + BitConverter.ToString(nonce).Replace("-", "").ToLowerInvariant() + "/";
        Array.Clear(nonce, 0, nonce.Length);
        listener = new TcpListener(IPAddress.Loopback, 0);
        listener.Start();
        local = new Uri("http://127.0.0.1:" + ((IPEndPoint)listener.LocalEndpoint).Port.ToString(CultureInfo.InvariantCulture) + "/");
        new Thread(AcceptLoop) { IsBackground = true, Name = "BD2 secure game transport" }.Start();
        log?.LogInfo("Game HTTPS transport uses native OS networking through a private loopback relay");
    }

    internal static SecureGameRelay Start(Uri server, ManualLogSource log) => new(server, log);

    internal Uri Rewrite(Uri uri)
    {
        return IsRelayedUri(uri) ? new Uri(local, prefix + uri.PathAndQuery.TrimStart('/')) : uri;
    }

    internal bool TryResolve(Uri uri, out Uri original)
    {
        original = null;
        if (uri == null || uri.Scheme != local.Scheme || uri.Host != local.Host || uri.Port != local.Port ||
            !uri.AbsolutePath.StartsWith(prefix, StringComparison.Ordinal)) return false;
        var resolved = new Uri(server, "/" + uri.PathAndQuery[prefix.Length..]);
        if (!IsRelayedUri(resolved)) return false;
        original = resolved;
        return true;
    }

    private bool IsRelayedUri(Uri uri) => uri != null && uri.Scheme == server.Scheme &&
        uri.IdnHost == server.IdnHost && uri.Port == server.Port && uri.UserInfo.Length == 0 &&
        uri.Fragment.Length == 0 && (uri.AbsolutePath.StartsWith("/game/", StringComparison.Ordinal) ||
            uri.AbsolutePath.Equals("/logs", StringComparison.Ordinal));

    private void AcceptLoop()
    {
        while (Volatile.Read(ref disposed) == 0)
        {
            TcpClient client;
            try { client = listener.AcceptTcpClient(); }
            catch (Exception) { if (Volatile.Read(ref disposed) != 0) return; continue; }
            if (!slots.Wait(0)) { client.Dispose(); continue; }
            Task.Run(async () =>
            {
                try { using (client) await Serve(client).ConfigureAwait(false); }
                catch (Exception ex) when (ex is IOException || ex is SocketException || ex is OperationCanceledException || ex is InvalidDataException)
                { log?.LogWarning("Native game relay connection failed: " + ex.GetType().Name); }
                catch (Exception ex) { log?.LogWarning("Native game transport failed: " + ex.GetType().Name); }
                finally { slots.Release(); }
            });
        }
    }

    private async Task Serve(TcpClient client)
    {
        client.ReceiveTimeout = 5000;
        client.SendTimeout = 5000;
        NetworkStream stream = client.GetStream();
        byte[] header = new byte[16 * 1024];
        int used = 0;
        while (used < header.Length)
        {
            int next = stream.ReadByte();
            if (next < 0) throw new IOException("Incomplete relay headers");
            header[used++] = (byte)next;
            if (used >= 4 && header[used - 4] == 13 && header[used - 3] == 10 && header[used - 2] == 13 && header[used - 1] == 10) break;
        }
        if (used == header.Length) throw new InvalidDataException("Relay header limit");
        string[] lines = Encoding.ASCII.GetString(header, 0, used).Split(HeaderSeparators, StringSplitOptions.None);
        Array.Clear(header, 0, header.Length);
        string[] start = lines[0].Split(' ');
        if (start.Length != 3 || (start[0] != "PUT" && start[0] != "POST" && start[0] != "GET") ||
            !start[1].StartsWith(prefix, StringComparison.Ordinal) || !start[2].StartsWith("HTTP/1.", StringComparison.Ordinal))
            throw new InvalidDataException("Invalid relay request");
        if (!Uri.TryCreate(local, start[1], out Uri target) || !TryResolve(target, out Uri remote))
            throw new InvalidDataException("Invalid relay target");
        bool maintenance = remote.AbsolutePath.Equals("/game/MaintenanceInfo", StringComparison.Ordinal);
        if (maintenance) log?.LogInfo("Game relay received MaintenanceInfo request");
        var headers = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        int length = 0;
        bool lengthSeen = false;
        foreach (string line in lines)
        {
            int separator = line.IndexOf(':');
            if (separator <= 0) continue;
            string name = line[..separator].Trim(), value = line[(separator + 1)..].Trim();
            if (name.Equals("Transfer-Encoding", StringComparison.OrdinalIgnoreCase)) throw new InvalidDataException("Chunked relay requests are not supported");
            if (name.Equals("Content-Length", StringComparison.OrdinalIgnoreCase))
            {
                if (lengthSeen || !int.TryParse(value, NumberStyles.None, CultureInfo.InvariantCulture, out length) || length > 8 * 1024 * 1024)
                    throw new InvalidDataException("Invalid relay content length");
                lengthSeen = true;
            }
            if (name.Equals("Content-Type", StringComparison.OrdinalIgnoreCase) || name.Equals("Cookie", StringComparison.OrdinalIgnoreCase) ||
                name.Equals("b2_app_version", StringComparison.OrdinalIgnoreCase) || name.Equals("b2_bundle_version", StringComparison.OrdinalIgnoreCase) ||
                name.Equals("market_type", StringComparison.OrdinalIgnoreCase)) headers[name] = value;
        }
        byte[] body = length == 0 ? null : new byte[length];
        PlatformControlHttp.Response response = null;
        try
        {
            for (int offset = 0; offset < length;)
            {
                int count = stream.Read(body, offset, length - offset);
                if (count <= 0) throw new IOException("Incomplete relay body");
                offset += count;
            }
            // The native hard deadline leaves margin inside Unity's 30s timeout.
            response = await PlatformControlHttp.Send(remote, start[0], body, null, 25, lifetime.Token,
                lifetime.Token, 64 * 1024 * 1024, headers).ConfigureAwait(false);
            if (maintenance || response.StatusCode == 0 || response.StatusCode >= 400)
                log?.LogInfo("Game relay upstream result: request_id=" + response.RequestId + " status=" + response.StatusCode +
                    " path=" + remote.AbsolutePath + " failure=" + (response.FailureKind ?? "none"));
            if (response.StatusCode == 0 || response.Data == null)
            {
                // Return a complete, recognizable local failure. The game sends all
                // non-Success results through its own same-packet backoff; recovery
                // distinguishes this marker from actual upstream HTTP errors.
                byte[] failureBody = Encoding.UTF8.GetBytes("Native game transport unavailable.\n");
                byte[] failure = Encoding.ASCII.GetBytes("HTTP/1.1 502 Native Transport Failure\r\nConnection: close\r\nCache-Control: no-store\r\nX-BD2-Transport-Failure: 1\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: " +
                    failureBody.Length.ToString(CultureInfo.InvariantCulture) + "\r\n\r\n");
                stream.Write(failure, 0, failure.Length);
                stream.Write(failureBody, 0, failureBody.Length);
                return;
            }
            StringBuilder output = new StringBuilder("HTTP/1.1 ").Append(response.StatusCode.ToString(CultureInfo.InvariantCulture)).Append(" Response\r\nConnection: close\r\nCache-Control: no-store\r\n");
            foreach (KeyValuePair<string, string> item in response.Headers)
            {
                if ((item.Key.Equals("Content-Type", StringComparison.OrdinalIgnoreCase) || item.Key.Equals("Set-Cookie", StringComparison.OrdinalIgnoreCase) ||
                    (item.Key.StartsWith("X-BD2-", StringComparison.OrdinalIgnoreCase) &&
                        !item.Key.Equals("X-BD2-Transport-Failure", StringComparison.OrdinalIgnoreCase))) && item.Value.IndexOfAny(HeaderNewlines) < 0)
                    output.Append(item.Key).Append(": ").Append(item.Value).Append("\r\n");
            }
            output.Append("Content-Length: ").Append(response.Data.Length.ToString(CultureInfo.InvariantCulture)).Append("\r\n\r\n");
            byte[] replyHeader = Encoding.ASCII.GetBytes(output.ToString());
            stream.Write(replyHeader, 0, replyHeader.Length);
            stream.Write(response.Data, 0, response.Data.Length);
            Array.Clear(replyHeader, 0, replyHeader.Length);
        }
        finally
        {
            if (body != null) Array.Clear(body, 0, body.Length);
            if (response?.Data != null) Array.Clear(response.Data, 0, response.Data.Length);
        }
    }

    public void Dispose()
    {
        if (Interlocked.Exchange(ref disposed, 1) != 0) return;
        lifetime.Cancel();
        listener.Stop();
    }
}
