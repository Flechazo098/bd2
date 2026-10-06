using System.Globalization;
using System;
using System.Collections.Concurrent;
using System.IO;
using System.Linq;
using System.Text;
using System.Threading;
using BepInEx.Logging;

namespace Bd2CaptureEnvironment;

internal static class CaptureWriter
{
    private const int MaxBodyBytes = 16 * 1024 * 1024;
    private const int MaxQueuedRecords = 256;
    private const int WriterShutdownSeconds = 30;
    private static readonly object FailureFileLock = new();
    private static readonly BlockingCollection<CaptureRecord> WriteQueue =
        new(new ConcurrentQueue<CaptureRecord>(), MaxQueuedRecords);
    private static ManualLogSource Log;
    internal static string DirectoryPath { get; private set; }
    private static string JsonlPath;
    private static string ReadableLogPath;
    private static Thread WriterThread;
    private static int WriterFailed;
    private static int RejectionLogged;
    private static int StopRequested;
    internal static void Initialize(string root, ManualLogSource log)
    {
        Log = log;
        DirectoryPath = Path.GetFullPath(Path.Combine(root, "Capture", DateTime.Now.ToString("yyyyMMdd-HHmmss", CultureInfo.InvariantCulture)));
        JsonlPath = Path.Combine(DirectoryPath, "capture.jsonl");
        ReadableLogPath = Path.Combine(DirectoryPath, "capture.log");
        Directory.CreateDirectory(Path.Combine(DirectoryPath, "bodies"));
        StartWriter();
    }
    private sealed class CaptureRecord
    {
        public DateTime Timestamp;
        public long Id;
        public string Direction;
        public string Path;
        public string Type;
        public byte[] Body;
        public int Length;
        public string Note;
    }

    internal static void QueueCapture(long id, string direction, string path,
        string type, byte[] body)
    {
        if (Volatile.Read(ref WriterFailed) != 0)
        {
            LogRejectedPacket(id, direction, path,
                "capture writer is unavailable");
            return;
        }
        if (WriteQueue.IsAddingCompleted)
        {
            string closedReason = "capture queue was already closed when packet " +
                id + " " + direction + " " + path + " arrived";
            MarkIncomplete(closedReason, null);
            LogRejectedPacket(id, direction, path, closedReason);
            return;
        }
        int length = body?.Length ?? 0;
        string note = body == null ? "protobuf body is null" : null;
        byte[] owned = null;
        if (length <= MaxBodyBytes)
        {
            if (length != 0)
            {
                owned = new byte[length];
                Buffer.BlockCopy(body, 0, owned, 0, length);
            }
        }
        else
        {
            note = "body exceeds " + MaxBodyBytes + " byte capture limit";
        }
        var record = new CaptureRecord
        {
            Timestamp = DateTime.Now,
            Id = id,
            Direction = direction,
            Path = path,
            Type = type,
            Body = owned,
            Length = length,
            Note = note
        };
        try
        {
            if (WriteQueue.TryAdd(record)) return;
        }
        catch (InvalidOperationException)
        {
            // CompleteAdding can race a producer during application shutdown.
        }
        string reason = WriteQueue.IsAddingCompleted
            ? "capture queue closed before packet " + id + " " + direction +
                " " + path + " could be queued"
            : "capture queue capacity " + MaxQueuedRecords +
                " was exceeded; packet " + id + " " + direction + " " + path +
                " was not captured";
        MarkIncomplete(reason, null);
        LogRejectedPacket(id, direction, path, reason);
    }

    private static void StartWriter()
    {
        WriterThread = new Thread(WriterLoop)
        {
            IsBackground = true,
            Name = "BD2 capture writer"
        };
        WriterThread.Start();
    }

    internal static void Stop()
    {
        if (Interlocked.Exchange(ref StopRequested, 1) != 0) return;
        CompleteWriterQueue();
        if (WriterThread == null || !WriterThread.IsAlive) return;
        if (WriterThread.Join(TimeSpan.FromSeconds(WriterShutdownSeconds))) return;
        MarkIncomplete(
            "capture writer did not drain " + WriteQueue.Count +
            " queued records within " + WriterShutdownSeconds +
            " seconds during shutdown", null);
    }

    private static void WriterLoop()
    {
        try
        {
            using var json = new StreamWriter(JsonlPath, false, new UTF8Encoding(false));
            using var readable = new StreamWriter(
                ReadableLogPath, false, new UTF8Encoding(false));
            readable.WriteLine("timestamp                         id      dir   path                                     protobuf type                                      bytes  body");
            readable.WriteLine(new string('-', 160));
            foreach (CaptureRecord record in WriteQueue.GetConsumingEnumerable())
            {
                try
                {
                    string bodyFile = WriteBody(record);
                    json.WriteLine(RecordJson(record, bodyFile));
                    readable.WriteLine(ReadableLine(record, bodyFile));
                    json.Flush();
                    readable.Flush();
                }
                catch (Exception ex)
                {
                    MarkIncomplete(
                        "capture writer failed while writing packet " + record.Id +
                        " " + record.Direction + " " + record.Path, ex);
                    return;
                }
            }
        }
        catch (Exception ex)
        {
            MarkIncomplete("capture writer failed to initialize or finalize", ex);
        }
    }

    private static void CompleteWriterQueue()
    {
        if (WriteQueue.IsAddingCompleted) return;
        try { WriteQueue.CompleteAdding(); }
        catch (InvalidOperationException) { }
    }

    internal static void MarkIncomplete(string reason, Exception error)
    {
        if (Interlocked.CompareExchange(ref WriterFailed, 1, 0) != 0) return;
        CompleteWriterQueue();
        string detail = DateTime.Now.ToString("O") + Environment.NewLine + reason;
        if (error != null) detail += Environment.NewLine + error;
        detail += Environment.NewLine;
        try
        {
            if (!string.IsNullOrEmpty(DirectoryPath))
            {
                Directory.CreateDirectory(DirectoryPath);
                lock (FailureFileLock)
                {
                    File.WriteAllText(
                        Path.Combine(DirectoryPath, "INCOMPLETE.txt"),
                        detail, new UTF8Encoding(false));
                }
            }
        }
        catch (Exception markerError)
        {
            Log?.LogError("could not write capture INCOMPLETE marker: " + markerError);
        }
        Log?.LogError("CAPTURE IS INCOMPLETE: " + reason +
            (error == null ? "" : Environment.NewLine + error));
    }

    private static void LogRejectedPacket(long id, string direction, string path,
        string reason)
    {
        if (Interlocked.Exchange(ref RejectionLogged, 1) != 0) return;
        Log?.LogError("CAPTURE PACKETS ARE BEING REJECTED: packet " + id + " " +
            direction + " " + path + "; " + reason);
    }

    private static string WriteBody(CaptureRecord record)
    {
        if (record.Body == null || record.Body.Length == 0) return null;
        string type = record.Type ?? record.Path ?? "protobuf";
        int dot = type.LastIndexOf('.');
        if (dot >= 0) type = type[(dot + 1)..];
        string safe = new string(type.Select(ch => char.IsLetterOrDigit(ch) ? ch : '_')
            .ToArray()).Trim('_');
        if (safe.Length == 0) safe = "protobuf";
        string name = record.Id.ToString("D6", CultureInfo.InvariantCulture) + "_" + record.Direction +
            "_" + safe + ".pb";
        File.WriteAllBytes(Path.Combine(DirectoryPath, "bodies", name), record.Body);
        return "bodies/" + name;
    }

    private static string RecordJson(CaptureRecord record, string bodyFile)
    {
        return "{" +
            "\"timestamp\":\"" + Escape(record.Timestamp.ToString("O")) + "\"," +
            "\"id\":" + record.Id + "," +
            "\"direction\":\"" + Escape(record.Direction) + "\"," +
            "\"path\":\"" + Escape(record.Path) + "\"," +
            "\"protobuf_type\":\"" + Escape(record.Type) + "\"," +
            "\"length\":" + record.Length + "," +
            "\"body\":" + JsonString(bodyFile) + "," +
            "\"note\":" + JsonString(record.Note) + "}";
    }

    private static string ReadableLine(CaptureRecord record, string bodyFile)
    {
        string direction = record.Direction == "request" ? "REQ" : "RESP";
        return string.Format(CultureInfo.InvariantCulture, "{0,-33} {1,6:D6}  {2,-4}  {3,-40} {4,-50} {5,8}  {6}",
            record.Timestamp.ToString("O"), record.Id, direction,
            Truncate(record.Path, 40), Truncate(record.Type, 50), record.Length,
            bodyFile ?? record.Note ?? "-");
    }

    private static string Truncate(string value, int width)
    {
        value ??= "";
        return value.Length <= width ? value : value[..(width - 1)] + "…";
    }

    private static string JsonString(string value)
    {
        return value == null ? "null" : "\"" + Escape(value) + "\"";
    }

    private static string Escape(string value)
    {
        if (value == null) return "";
        var escaped = new StringBuilder(value.Length + 16);
        foreach (char character in value)
        {
            switch (character)
            {
                case '\"': escaped.Append("\\\""); break;
                case '\\': escaped.Append("\\\\"); break;
                case '\b': escaped.Append("\\b"); break;
                case '\f': escaped.Append("\\f"); break;
                case '\n': escaped.Append("\\n"); break;
                case '\r': escaped.Append("\\r"); break;
                case '\t': escaped.Append("\\t"); break;
                default:
                    if (character <= '\u001f')
                        escaped.Append("\\u").Append(((int)character).ToString("x4", CultureInfo.InvariantCulture));
                    else
                        escaped.Append(character);
                    break;
            }
        }
        return escaped.ToString();
    }


}
