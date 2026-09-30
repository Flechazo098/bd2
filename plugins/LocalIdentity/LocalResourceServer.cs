using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Text;
using System.Threading;
using BepInEx.Logging;

namespace Bd2LocalIdentity;

internal sealed class LocalResourceServer : IDisposable
{
    private const int MaximumRequestHeaderBytes = 16 * 1024;
    private const int MaximumConcurrentRequests = 16;

    private readonly ManualLogSource log;
    private readonly TcpListener listener;
    private readonly Thread acceptThread;
    private readonly Semaphore requestSlots = new Semaphore(MaximumConcurrentRequests, MaximumConcurrentRequests);
    private volatile bool disposed;

    private LocalResourceServer(ManualLogSource log, string rootDirectory)
    {
        this.log = log;
        RootDirectory = rootDirectory;
        listener = new TcpListener(IPAddress.Loopback, 0);
        listener.Start();
        int port = ((IPEndPoint)listener.LocalEndpoint).Port;
        Origin = "http://127.0.0.1:" + port.ToString(CultureInfo.InvariantCulture);
        acceptThread = new Thread(AcceptLoop)
        {
            IsBackground = true,
            Name = "BD2 local resource server"
        };
        acceptThread.Start();
    }

    internal string Origin { get; }

    internal string RootDirectory { get; }

    internal static LocalResourceServer Start(
        ManualLogSource log,
        string configuredDirectory,
        string bundleVersion,
        string gameDataVersion)
    {
        string root = ValidateRoot(configuredDirectory, bundleVersion, gameDataVersion);
        LocalResourceServer server = new LocalResourceServer(log, root);
        log.LogInfo("Local resource HTTP server listening on " + server.Origin);
        return server;
    }

    private static string ValidateRoot(string configuredDirectory, string bundleVersion, string gameDataVersion)
    {
        if (string.IsNullOrWhiteSpace(configuredDirectory))
        {
            throw new InvalidDataException("local_resource_directory must not be empty");
        }

        string root;
        try
        {
            root = Path.GetFullPath(configuredDirectory.Trim());
        }
        catch (Exception ex) when (ex is ArgumentException || ex is NotSupportedException || ex is PathTooLongException)
        {
            throw new InvalidDataException("local_resource_directory is not a valid filesystem path", ex);
        }
        if (!Directory.Exists(root))
        {
            throw new DirectoryNotFoundException("Local resource directory does not exist: " + root);
        }
        if ((File.GetAttributes(root) & FileAttributes.ReparsePoint) != 0)
        {
            throw new InvalidDataException("Local resource directory must not be a symbolic link or junction: " + root);
        }

        RequireDirectory(root, "ServerData");
        RequireDirectory(root, "GameData");
        RequireFile(root, "ServerData", "StandaloneWindows64", "HD", bundleVersion, "catalog_alpha.json");
        RequireFile(root, "ServerData", "StandaloneWindows64", "HD", bundleVersion, "catalog_alpha.hash");
        RequireFile(root, "GameData", gameDataVersion, "release", "common-dbdata.info");
        RequireFile(root, "GameData", gameDataVersion, "release", "common-dbdata.bin");
        string volumeRoot = Path.GetPathRoot(root);
        if (root.Length > volumeRoot.Length)
        {
            root = root.TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar);
        }
        return root;
    }

    private static void RequireDirectory(string root, params string[] parts)
    {
        string path = Combine(root, parts);
        if (!Directory.Exists(path))
        {
            throw new DirectoryNotFoundException("Local resource directory is missing: " + path);
        }
    }

    private static void RequireFile(string root, params string[] parts)
    {
        string path = Combine(root, parts);
        if (!File.Exists(path))
        {
            throw new FileNotFoundException("Local resource directory is missing a release file", path);
        }
    }

    private static string Combine(string root, IEnumerable<string> parts)
    {
        string result = root;
        foreach (string part in parts)
        {
            result = Path.Combine(result, part);
        }
        return result;
    }

    private void AcceptLoop()
    {
        while (!disposed)
        {
            TcpClient client;
            try
            {
                client = listener.AcceptTcpClient();
            }
            catch (SocketException) when (disposed)
            {
                return;
            }
            catch (ObjectDisposedException) when (disposed)
            {
                return;
            }
            catch (Exception ex)
            {
                if (!disposed)
                {
                    log.LogError("Local resource accept failed: " + ex.Message);
                }
                continue;
            }

            if (!requestSlots.WaitOne(0))
            {
                client.Dispose();
                continue;
            }
            ThreadPool.QueueUserWorkItem(_ =>
            {
                try
                {
                    HandleClient(client);
                }
                catch (Exception ex)
                {
                    log.LogWarning("Local resource request failed: " + ex.Message);
                }
                finally
                {
                    client.Dispose();
                    requestSlots.Release();
                }
            });
        }
    }

    private void HandleClient(TcpClient client)
    {
        client.ReceiveTimeout = 10_000;
        client.SendTimeout = 30_000;
        using NetworkStream stream = client.GetStream();
        string header = ReadRequestHeader(stream);
        if (header == null)
        {
            WriteError(stream, 400, "Bad Request");
            return;
        }

        string[] lines = header.Split(new[] { "\r\n" }, StringSplitOptions.None);
        string[] requestLine = lines[0].Split(' ');
        if (requestLine.Length != 3 || (requestLine[2] != "HTTP/1.1" && requestLine[2] != "HTTP/1.0"))
        {
            WriteError(stream, 400, "Bad Request");
            return;
        }
        bool head = requestLine[0] == "HEAD";
        if (!head && requestLine[0] != "GET")
        {
            WriteError(stream, 405, "Method Not Allowed", "Allow: GET, HEAD\r\n");
            return;
        }

        Dictionary<string, string> headers = ParseHeaders(lines);
        string path = ResolvePath(requestLine[1]);
        if (path == null || !File.Exists(path))
        {
            WriteError(stream, 404, "Not Found");
            return;
        }

        using FileStream file = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read);
        long start = 0;
        long end = file.Length - 1;
        bool partial = false;
        if (headers.TryGetValue("Range", out string range))
        {
            if (!TryParseRange(range, file.Length, out start, out end))
            {
                WriteError(stream, 416, "Range Not Satisfiable", "Content-Range: bytes */" + file.Length + "\r\n");
                return;
            }
            partial = true;
        }

        long contentLength = file.Length == 0 ? 0 : end - start + 1;
        StringBuilder response = new StringBuilder();
        response.Append(partial ? "HTTP/1.1 206 Partial Content\r\n" : "HTTP/1.1 200 OK\r\n");
        response.Append("Content-Length: ").Append(contentLength).Append("\r\n");
        response.Append("Content-Type: ").Append(GetContentType(path)).Append("\r\n");
        response.Append("Accept-Ranges: bytes\r\n");
        if (partial)
        {
            response.Append("Content-Range: bytes ").Append(start).Append('-').Append(end).Append('/').Append(file.Length).Append("\r\n");
        }
        response.Append("Cache-Control: public, max-age=31536000, immutable\r\n");
        response.Append("Connection: close\r\n\r\n");
        WriteAscii(stream, response.ToString());
        if (head || contentLength == 0)
        {
            return;
        }

        file.Position = start;
        byte[] buffer = new byte[128 * 1024];
        long remaining = contentLength;
        while (remaining > 0)
        {
            int read = file.Read(buffer, 0, (int)Math.Min(buffer.Length, remaining));
            if (read == 0)
            {
                throw new EndOfStreamException("Local resource file ended before its declared length");
            }
            stream.Write(buffer, 0, read);
            remaining -= read;
        }
    }

    private string ResolvePath(string requestTarget)
    {
        int query = requestTarget.IndexOfAny(new[] { '?', '#' });
        string rawPath = query >= 0 ? requestTarget.Substring(0, query) : requestTarget;
        string decoded;
        try
        {
            decoded = Uri.UnescapeDataString(rawPath);
        }
        catch
        {
            return null;
        }
        if (decoded.IndexOf('\0') >= 0)
        {
            return null;
        }

        string[] parts = decoded.Replace('\\', '/').Split(new[] { '/' }, StringSplitOptions.RemoveEmptyEntries);
        if (parts.Length < 2 || (parts[0] != "ServerData" && parts[0] != "GameData"))
        {
            return null;
        }
        foreach (string part in parts)
        {
            if (part == "." || part == ".." || part.IndexOf(':') >= 0)
            {
                return null;
            }
        }

        string candidate = Combine(RootDirectory, parts);
        try
        {
            candidate = Path.GetFullPath(candidate);
        }
        catch
        {
            return null;
        }
        string prefix = RootDirectory + Path.DirectorySeparatorChar;
        if (!candidate.StartsWith(prefix, StringComparison.OrdinalIgnoreCase) || ContainsReparsePoint(parts))
        {
            return null;
        }
        return candidate;
    }

    private bool ContainsReparsePoint(string[] parts)
    {
        string current = RootDirectory;
        foreach (string part in parts)
        {
            current = Path.Combine(current, part);
            try
            {
                if ((File.GetAttributes(current) & FileAttributes.ReparsePoint) != 0)
                {
                    return true;
                }
            }
            catch
            {
                return true;
            }
        }
        return false;
    }

    private static string ReadRequestHeader(Stream stream)
    {
        byte[] bytes = new byte[MaximumRequestHeaderBytes];
        int count = 0;
        while (count < bytes.Length)
        {
            int read = stream.Read(bytes, count, 1);
            if (read == 0)
            {
                return null;
            }
            count++;
            if (count >= 4 && bytes[count - 4] == '\r' && bytes[count - 3] == '\n' &&
                bytes[count - 2] == '\r' && bytes[count - 1] == '\n')
            {
                return Encoding.ASCII.GetString(bytes, 0, count - 4);
            }
        }
        return null;
    }

    private static Dictionary<string, string> ParseHeaders(string[] lines)
    {
        Dictionary<string, string> result = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        for (int i = 1; i < lines.Length; i++)
        {
            int separator = lines[i].IndexOf(':');
            if (separator <= 0)
            {
                continue;
            }
            result[lines[i].Substring(0, separator).Trim()] = lines[i].Substring(separator + 1).Trim();
        }
        return result;
    }

    private static bool TryParseRange(string value, long length, out long start, out long end)
    {
        start = 0;
        end = length - 1;
        if (length == 0 || !value.StartsWith("bytes=", StringComparison.OrdinalIgnoreCase) || value.IndexOf(',') >= 0)
        {
            return false;
        }
        string[] bounds = value.Substring(6).Split('-');
        if (bounds.Length != 2)
        {
            return false;
        }
        if (bounds[0].Length == 0)
        {
            if (!long.TryParse(bounds[1], NumberStyles.None, CultureInfo.InvariantCulture, out long suffix) || suffix <= 0)
            {
                return false;
            }
            start = Math.Max(0, length - suffix);
            return true;
        }
        if (!long.TryParse(bounds[0], NumberStyles.None, CultureInfo.InvariantCulture, out start) || start < 0 || start >= length)
        {
            return false;
        }
        if (bounds[1].Length != 0 &&
            (!long.TryParse(bounds[1], NumberStyles.None, CultureInfo.InvariantCulture, out end) || end < start))
        {
            return false;
        }
        end = Math.Min(end, length - 1);
        return true;
    }

    private static string GetContentType(string path)
    {
        switch (Path.GetExtension(path).ToLowerInvariant())
        {
            case ".json":
                return "application/json; charset=utf-8";
            case ".hash":
            case ".info":
            case ".version":
                return "text/plain; charset=utf-8";
            default:
                return "application/octet-stream";
        }
    }

    private static void WriteError(Stream stream, int status, string reason, string additionalHeaders = "")
    {
        WriteAscii(stream, "HTTP/1.1 " + status + " " + reason + "\r\n" + additionalHeaders +
            "Content-Length: 0\r\nCache-Control: no-store\r\nConnection: close\r\n\r\n");
    }

    private static void WriteAscii(Stream stream, string value)
    {
        byte[] bytes = Encoding.ASCII.GetBytes(value);
        stream.Write(bytes, 0, bytes.Length);
    }

    public void Dispose()
    {
        if (disposed)
        {
            return;
        }
        disposed = true;
        listener.Stop();
        if (Thread.CurrentThread != acceptThread)
        {
            acceptThread.Join(1_000);
        }
        log.LogInfo("Local resource HTTP server stopped");
    }
}
