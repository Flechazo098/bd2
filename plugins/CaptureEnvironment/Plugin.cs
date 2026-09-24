using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Text;
using System.Threading;
using BepInEx;
using BepInEx.Logging;
using Google.Protobuf;
using HarmonyLib;
using UnityEngine;

namespace Bd2CaptureEnvironment;

[BepInPlugin(Guid, Name, Version)]
public sealed class Plugin : BaseUnityPlugin
{
    public const string Guid = "bd2.capture.environment";
    public const string Name = "BD2 Capture Environment";
    public const string Version = Bd2Build.Versions.Plugin;

    private const int MaxBodyBytes = 16 * 1024 * 1024;
    private const int MaxQueuedRecords = 256;
    private const int WriterShutdownSeconds = 30;
    private const string PlayerPrefsPrefix = "BD2OfficialCapture:" + Bd2Build.Versions.Client + ":";
    private static readonly object CorrelationLock = new object();
    private static readonly object FailureFileLock = new object();
    private static readonly Dictionary<string, Queue<PendingRequest>> PendingRequests =
        new Dictionary<string, Queue<PendingRequest>>(StringComparer.Ordinal);
    private static readonly BlockingCollection<CaptureRecord> WriteQueue =
        new BlockingCollection<CaptureRecord>(
            new ConcurrentQueue<CaptureRecord>(), MaxQueuedRecords);
    private static ManualLogSource Log;
    private static string CaptureDirectory;
    private static string JsonlPath;
    private static string ReadableLogPath;
    private static string IsolatedDataDirectory;
    private static string IsolatedGameDataDirectory;
    private static long Sequence;
    private static Thread WriterThread;
    private static int WriterFailed;
    private static int RejectionLogged;
    private static int StopRequested;

    private void Awake()
    {
        try
        {
            Log = Logger;
            string root = Paths.GameRootPath;
            IsolatedDataDirectory = Path.GetFullPath(Path.Combine(root, "IsolatedUserData"));
            IsolatedGameDataDirectory = Path.Combine(IsolatedDataDirectory, "Data", "t");
            CaptureDirectory = Path.GetFullPath(Path.Combine(
                root, "Capture", DateTime.Now.ToString("yyyyMMdd-HHmmss")));
            JsonlPath = Path.Combine(CaptureDirectory, "capture.jsonl");
            ReadableLogPath = Path.Combine(CaptureDirectory, "capture.log");
            EnsurePrivateGameDataDirectory();
            Directory.CreateDirectory(Path.Combine(CaptureDirectory, "bodies"));
            StartWriter();

            var harmony = new Harmony(Guid);
            InstallStorageIsolation(harmony);
            string effectiveDataPath = Application.persistentDataPath;
            if (!string.Equals(
                    Path.GetFullPath(effectiveDataPath).TrimEnd(Path.DirectorySeparatorChar),
                    IsolatedDataDirectory.TrimEnd(Path.DirectorySeparatorChar),
                    StringComparison.OrdinalIgnoreCase))
                throw new InvalidOperationException(
                    "persistentDataPath isolation verification failed: " + effectiveDataPath);
            InstallPlaintextCapture(harmony);
            WriteMetadata();
            Logger.LogInfo("Official capture environment active");
            Logger.LogInfo("persistentDataPath => " + IsolatedDataDirectory);
            Logger.LogInfo("isolated GameData => " + IsolatedGameDataDirectory);
            Logger.LogInfo("capture => " + CaptureDirectory);
        }
        catch (Exception ex)
        {
            Logger.LogError("Capture environment setup failed: " + ex);
            MarkCaptureIncomplete("capture environment setup failed", ex);
        }
    }

    private void OnApplicationQuit()
    {
        StopWriter();
    }

    private sealed class PendingRequest
    {
        public long Id;
        public string Type;
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

    private static void EnsurePrivateGameDataDirectory()
    {
        foreach (string path in new[]
        {
            IsolatedDataDirectory,
            Path.Combine(IsolatedDataDirectory, "Data"),
            IsolatedGameDataDirectory
        })
        {
            if (Directory.Exists(path) &&
                (new DirectoryInfo(path).Attributes & FileAttributes.ReparsePoint) != 0)
                throw new InvalidOperationException("Isolated GameData path is linked: " + path);
            Directory.CreateDirectory(path);
        }
    }

    private static void InstallStorageIsolation(Harmony harmony)
    {
        PropertyInfo persistent = typeof(Application).GetProperty(
            "persistentDataPath", BindingFlags.Static | BindingFlags.Public);
        MethodInfo getter = persistent?.GetGetMethod();
        if (getter == null)
            throw new MissingMethodException("Application.persistentDataPath getter not found");
        harmony.Patch(getter, prefix: new HarmonyMethod(
            typeof(Plugin), nameof(PersistentDataPathPrefix)));

        var names = new HashSet<string>(StringComparer.Ordinal)
        {
            "GetString", "SetString", "GetInt", "SetInt", "GetFloat", "SetFloat",
            "HasKey", "DeleteKey"
        };
        foreach (MethodInfo method in typeof(PlayerPrefs).GetMethods(
                     BindingFlags.Static | BindingFlags.Public))
        {
            ParameterInfo[] parameters = method.GetParameters();
            if (!names.Contains(method.Name) || parameters.Length == 0 ||
                parameters[0].ParameterType != typeof(string))
                continue;
            harmony.Patch(method, prefix: new HarmonyMethod(
                typeof(Plugin), nameof(PlayerPrefsKeyPrefix)));
        }

        MethodInfo deleteAll = typeof(PlayerPrefs).GetMethod(
            "DeleteAll", BindingFlags.Static | BindingFlags.Public,
            null, Type.EmptyTypes, null);
        if (deleteAll != null)
            harmony.Patch(deleteAll, prefix: new HarmonyMethod(
                typeof(Plugin), nameof(BlockPlayerPrefsDeleteAll)));
    }

    private static bool PersistentDataPathPrefix(ref string __result)
    {
        __result = IsolatedDataDirectory;
        return false;
    }

    private static void PlayerPrefsKeyPrefix(ref string key)
    {
        if (key != null && !key.StartsWith(PlayerPrefsPrefix, StringComparison.Ordinal))
            key = PlayerPrefsPrefix + key;
    }

    private static bool BlockPlayerPrefsDeleteAll()
    {
        Log?.LogWarning("Blocked PlayerPrefs.DeleteAll to protect the primary client profile");
        return false;
    }

    private static void InstallPlaintextCapture(Harmony harmony)
    {
        Type manager = FindType("BDNetwork.NetworkManager");
        MethodInfo gameDataPath = manager?.GetMethod("GetPachedGameDataPath",
            BindingFlags.Instance | BindingFlags.Public, null, Type.EmptyTypes, null);
        if (gameDataPath == null || gameDataPath.ReturnType != typeof(string))
            throw new MissingMethodException("NetworkManager.GetPachedGameDataPath not found");
        harmony.Patch(gameDataPath, prefix: new HarmonyMethod(
            typeof(Plugin), nameof(GameDataPathPrefix)));
        Log?.LogInfo("Private installed GameData path hook installed");

        MethodInfo send = manager?.GetMethods(BindingFlags.Instance | BindingFlags.Public)
            .FirstOrDefault(method => method.Name == "Send" &&
                method.ReturnType == typeof(void) &&
                method.GetParameters().Length == 6 &&
                typeof(IMessage).IsAssignableFrom(method.GetParameters()[0].ParameterType));
        if (send != null)
        {
            harmony.Patch(send, prefix: new HarmonyMethod(
                typeof(Plugin), nameof(GameSendPrefix)));
            Log?.LogInfo("Plaintext request capture installed");
        }
        else
        {
            Log?.LogWarning("NetworkManager.Send plaintext hook not found");
        }

        Type responseData = FindType("BDNetwork.ResponseData");
        MethodInfo responseCheck = manager?.GetMethods(
                BindingFlags.Instance | BindingFlags.NonPublic)
            .SingleOrDefault(method =>
            {
                ParameterInfo[] parameters = method.GetParameters();
                return method.ReturnType == typeof(bool) && parameters.Length == 4 &&
                    parameters[0].ParameterType == typeof(string) &&
                    parameters[2].ParameterType == responseData &&
                    parameters[3].ParameterType == typeof(byte[]);
            });
        if (responseCheck != null)
        {
            harmony.Patch(responseCheck, prefix: new HarmonyMethod(
                typeof(Plugin), nameof(ResponseCheckPrefix)));
            Log?.LogInfo("Path-correlated plaintext response capture installed");
        }
        else
        {
            Log?.LogWarning("NetworkManager.ResponseCheck plaintext hook not found");
        }
    }

    private static bool GameDataPathPrefix(ref string __result)
    {
        __result = IsolatedGameDataDirectory;
        return false;
    }

    private static void GameSendPrefix(IMessage __0)
    {
        try
        {
            if (__0 == null) return;
            byte[] body = __0.ToByteArray();
            string type = __0.GetType().FullName ?? __0.GetType().Name;
            string path = RequestPath(__0.GetType().Name);
            long id = Interlocked.Increment(ref Sequence);
            EnqueueRequest(path, new PendingRequest { Id = id, Type = type });
            QueueCapture(id, "request", path, type, body);
        }
        catch (Exception ex)
        {
            Log?.LogWarning("plaintext request capture failed: " + ex.Message);
        }
    }

    private static void ResponseCheckPrefix(string __0, byte[] __3)
    {
        try
        {
            string path = NormalizePath(__0);
            PendingRequest request = DequeueRequest(path);
            long id = request?.Id ?? Interlocked.Increment(ref Sequence);
            string type = ResponseType(request?.Type, path);
            QueueCapture(id, "response", path, type, __3);
        }
        catch (Exception ex)
        {
            Log?.LogWarning("plaintext response capture failed: " + ex.Message);
        }
    }

    private static string RequestPath(string typeName)
    {
        const string suffix = "Request";
        if (typeName != null && typeName.EndsWith(suffix, StringComparison.Ordinal))
            typeName = typeName.Substring(0, typeName.Length - suffix.Length);
        return NormalizePath(typeName);
    }

    private static string NormalizePath(string path)
    {
        if (string.IsNullOrWhiteSpace(path)) return "/unknown";
        return path[0] == '/' ? path : "/" + path;
    }

    private static string ResponseType(string requestType, string path)
    {
        const string suffix = "Request";
        if (!string.IsNullOrEmpty(requestType) &&
            requestType.EndsWith(suffix, StringComparison.Ordinal))
            return requestType.Substring(0, requestType.Length - suffix.Length) + "Response";
        return "Proto.Net." + path.TrimStart('/') + "Response";
    }

    private static void EnqueueRequest(string path, PendingRequest request)
    {
        lock (CorrelationLock)
        {
            if (!PendingRequests.TryGetValue(path, out Queue<PendingRequest> requests))
            {
                requests = new Queue<PendingRequest>();
                PendingRequests.Add(path, requests);
            }
            requests.Enqueue(request);
        }
    }

    private static PendingRequest DequeueRequest(string path)
    {
        lock (CorrelationLock)
        {
            if (PendingRequests.TryGetValue(path, out Queue<PendingRequest> requests) &&
                requests.Count != 0)
            {
                PendingRequest request = requests.Dequeue();
                if (requests.Count == 0) PendingRequests.Remove(path);
                return request;
            }
        }
        return null;
    }

    private static void QueueCapture(long id, string direction, string path,
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
            MarkCaptureIncomplete(closedReason, null);
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
        MarkCaptureIncomplete(reason, null);
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

    private static void StopWriter()
    {
        if (Interlocked.Exchange(ref StopRequested, 1) != 0) return;
        CompleteWriterQueue();
        if (WriterThread == null || !WriterThread.IsAlive) return;
        if (WriterThread.Join(TimeSpan.FromSeconds(WriterShutdownSeconds))) return;
        MarkCaptureIncomplete(
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
                    MarkCaptureIncomplete(
                        "capture writer failed while writing packet " + record.Id +
                        " " + record.Direction + " " + record.Path, ex);
                    return;
                }
            }
        }
        catch (Exception ex)
        {
            MarkCaptureIncomplete("capture writer failed to initialize or finalize", ex);
        }
    }

    private static void CompleteWriterQueue()
    {
        if (WriteQueue.IsAddingCompleted) return;
        try { WriteQueue.CompleteAdding(); }
        catch (InvalidOperationException) { }
    }

    private static void MarkCaptureIncomplete(string reason, Exception error)
    {
        if (Interlocked.CompareExchange(ref WriterFailed, 1, 0) != 0) return;
        CompleteWriterQueue();
        string detail = DateTime.Now.ToString("O") + Environment.NewLine + reason;
        if (error != null) detail += Environment.NewLine + error;
        detail += Environment.NewLine;
        try
        {
            if (!string.IsNullOrEmpty(CaptureDirectory))
            {
                Directory.CreateDirectory(CaptureDirectory);
                lock (FailureFileLock)
                {
                    File.WriteAllText(
                        Path.Combine(CaptureDirectory, "INCOMPLETE.txt"),
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
        if (dot >= 0) type = type.Substring(dot + 1);
        string safe = new string(type.Select(ch => char.IsLetterOrDigit(ch) ? ch : '_')
            .ToArray()).Trim('_');
        if (safe.Length == 0) safe = "protobuf";
        string name = record.Id.ToString("D6") + "_" + record.Direction +
            "_" + safe + ".pb";
        File.WriteAllBytes(Path.Combine(CaptureDirectory, "bodies", name), record.Body);
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
        return string.Format("{0,-33} {1,6:D6}  {2,-4}  {3,-40} {4,-50} {5,8}  {6}",
            record.Timestamp.ToString("O"), record.Id, direction,
            Truncate(record.Path, 40), Truncate(record.Type, 50), record.Length,
            bodyFile ?? record.Note ?? "-");
    }

    private static string Truncate(string value, int width)
    {
        value ??= "";
        return value.Length <= width ? value : value.Substring(0, width - 1) + "…";
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
                        escaped.Append("\\u").Append(((int)character).ToString("x4"));
                    else
                        escaped.Append(character);
                    break;
            }
        }
        return escaped.ToString();
    }

    private static Type FindType(string fullName)
    {
        foreach (Assembly assembly in AppDomain.CurrentDomain.GetAssemblies())
        {
            Type type = assembly.GetType(fullName, false);
            if (type != null) return type;
        }
        try { return Assembly.Load("Assembly-CSharp")?.GetType(fullName, false); }
        catch { return null; }
    }

    private static void WriteMetadata()
    {
        File.WriteAllText(Path.Combine(CaptureDirectory, "README.txt"),
            "BD2 " + Bd2Build.Versions.Client + " official API capture.\r\n" +
            "All NetworkManager plaintext protobuf requests and responses are recorded.\r\n" +
            "capture.jsonl is machine-readable; capture.log is the aligned human-readable index.\r\n" +
            "If INCOMPLETE.txt exists, the writer rejected or could not flush part of the capture.\r\n" +
            "No Cookie/Authorization headers or URL query strings are recorded.\r\n" +
            "Raw protobuf/account bodies can still contain private account data. Do not share this directory.\r\n" +
            "clientVersion=" + Application.version + "\r\n" +
            "persistentDataPath=" + IsolatedDataDirectory + "\r\n" +
            "gameDataPath=" + IsolatedGameDataDirectory + "\r\n",
            new UTF8Encoding(false));
    }
}
