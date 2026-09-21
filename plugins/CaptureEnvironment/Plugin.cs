using System;
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
using UnityEngine.Networking;

namespace Bd2CaptureEnvironment;

[BepInPlugin(Guid, Name, Version)]
public sealed class Plugin : BaseUnityPlugin
{
    public const string Guid = "bd2.capture.environment";
    public const string Name = "BD2 Capture Environment";
    public const string Version = "0.1.1";

    private const int MaxBodyBytes = 16 * 1024 * 1024;
    private const string PlayerPrefsPrefix = "BD2OfficialCapture23413:";
    private static readonly object FileLock = new object();
    private static ManualLogSource Log;
    private static string CaptureDirectory;
    private static string JsonlPath;
    private static string IsolatedDataDirectory;
    private static string SharedGameDataDirectory;
    private static long Sequence;
    private static long LastApiRequest;

    private void Awake()
    {
        try
        {
            Log = Logger;
            string root = Paths.GameRootPath;
            IsolatedDataDirectory = Path.GetFullPath(Path.Combine(root, "IsolatedUserData"));
            SharedGameDataDirectory = ReadRequiredArgument("-bd2SharedGameData");
            if (!Directory.Exists(SharedGameDataDirectory))
                throw new DirectoryNotFoundException(
                    "Shared GameData directory not found: " + SharedGameDataDirectory);
            CaptureDirectory = Path.GetFullPath(Path.Combine(
                root, "Capture", DateTime.Now.ToString("yyyyMMdd-HHmmss")));
            JsonlPath = Path.Combine(CaptureDirectory, "capture.jsonl");
            Directory.CreateDirectory(IsolatedDataDirectory);
            Directory.CreateDirectory(Path.Combine(CaptureDirectory, "bodies"));

            var harmony = new Harmony(Guid);
            InstallStorageIsolation(harmony);
            string effectiveDataPath = Application.persistentDataPath;
            if (!string.Equals(
                    Path.GetFullPath(effectiveDataPath).TrimEnd(Path.DirectorySeparatorChar),
                    IsolatedDataDirectory.TrimEnd(Path.DirectorySeparatorChar),
                    StringComparison.OrdinalIgnoreCase))
                throw new InvalidOperationException(
                    "persistentDataPath isolation verification failed: " + effectiveDataPath);
            InstallRawCapture(harmony);
            InstallPlaintextCapture(harmony);
            WriteMetadata();
            Logger.LogInfo("Official capture environment active");
            Logger.LogInfo("persistentDataPath => " + IsolatedDataDirectory);
            Logger.LogInfo("shared GameData => " + SharedGameDataDirectory);
            Logger.LogInfo("capture => " + CaptureDirectory);
        }
        catch (Exception ex)
        {
            Logger.LogError("Capture environment setup failed: " + ex);
        }
    }

    private static string ReadRequiredArgument(string name)
    {
        string[] arguments = Environment.GetCommandLineArgs();
        for (int i = 0; i + 1 < arguments.Length; i++)
        {
            if (string.Equals(arguments[i], name, StringComparison.OrdinalIgnoreCase) &&
                !string.IsNullOrWhiteSpace(arguments[i + 1]))
                return Path.GetFullPath(arguments[i + 1]);
        }
        throw new ArgumentException("Required command-line argument is missing: " + name);
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

    private static void InstallRawCapture(Harmony harmony)
    {
        MethodInfo send = typeof(UnityWebRequest).GetMethod(
            "SendWebRequest", BindingFlags.Instance | BindingFlags.Public,
            null, Type.EmptyTypes, null);
        if (send == null)
            throw new MissingMethodException("UnityWebRequest.SendWebRequest not found");
        harmony.Patch(send,
            prefix: new HarmonyMethod(typeof(Plugin), nameof(SendWebRequestPrefix)),
            postfix: new HarmonyMethod(typeof(Plugin), nameof(SendWebRequestPostfix)));
    }

    private sealed class RequestState
    {
        public long Id;
        public string Method;
        public string Host;
        public string Path;
        public bool Capture;
    }

    private static void SendWebRequestPrefix(UnityWebRequest __instance, out RequestState __state)
    {
        __state = null;
        try
        {
            if (__instance == null || !TrySafeUri(__instance.url, out Uri uri) ||
                !IsGameApi(uri, __instance.method))
                return;
            var state = new RequestState
            {
                Id = Interlocked.Increment(ref Sequence),
                Method = __instance.method ?? "",
                Host = uri.Host,
                Path = uri.AbsolutePath,
                Capture = true
            };
            __state = state;
            Interlocked.Exchange(ref LastApiRequest, state.Id);
            byte[] body = null;
            try { body = __instance.uploadHandler?.data; } catch { }
            string file = SaveBody(state.Id, "req", state.Path, body);
            Emit("req", state.Id, state.Method, state.Host, state.Path, 0,
                body?.Length ?? 0, file, null, null);
        }
        catch (Exception ex)
        {
            Log?.LogWarning("request capture failed: " + ex.Message);
        }
    }

    private static void SendWebRequestPostfix(
        UnityWebRequest __instance, UnityWebRequestAsyncOperation __result,
        RequestState __state)
    {
        if (__state == null || !__state.Capture || __result == null)
            return;
        __result.completed += delegate
        {
            try
            {
                byte[] body = null;
                string error = null;
                long responseCode = 0;
                string result = null;
                try { body = __instance.downloadHandler?.data; }
                catch (Exception ex) { error = AppendError(error, ex.Message); }
                try { responseCode = (long)__instance.responseCode; }
                catch (Exception ex) { error = AppendError(error, ex.Message); }
                try { result = __instance.result.ToString(); }
                catch (Exception ex) { error = AppendError(error, ex.Message); }
                try { error = AppendError(error, __instance.error); }
                catch (Exception ex) { error = AppendError(error, ex.Message); }
                string file = SaveBody(__state.Id, "resp", __state.Path, body);
                Emit("resp", __state.Id, __state.Method, __state.Host, __state.Path,
                    responseCode, body?.Length ?? 0, file, result, error);
            }
            catch (Exception ex)
            {
                Log?.LogWarning("response capture failed: " + ex.Message);
            }
        };
    }

    private static string AppendError(string current, string next)
    {
        if (string.IsNullOrWhiteSpace(next)) return current;
        return string.IsNullOrWhiteSpace(current) ? next : current + "; " + next;
    }

    private static void InstallPlaintextCapture(Harmony harmony)
    {
        Type manager = FindType("BDNetwork.NetworkManager");
        MethodInfo gameDataPath = manager?.GetMethod("GetPachedGameDataPath",
            BindingFlags.Instance | BindingFlags.Public);
        if (gameDataPath == null)
            throw new MissingMethodException("NetworkManager.GetPachedGameDataPath not found");
        harmony.Patch(gameDataPath, prefix: new HarmonyMethod(
            typeof(Plugin), nameof(GameDataPathPrefix)));
        Log?.LogInfo("Shared GameData path hook installed");

        MethodInfo send = manager?.GetMethods(BindingFlags.Instance | BindingFlags.Public)
            .FirstOrDefault(method => method.Name == "Send" &&
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

        MethodInfo decrypt = manager?.GetMethod("AESDecrypt256",
            BindingFlags.Instance | BindingFlags.Public);
        if (decrypt != null)
        {
            harmony.Patch(decrypt, postfix: new HarmonyMethod(
                typeof(Plugin), nameof(DecryptPostfix)));
            Log?.LogInfo("Plaintext response capture installed");
        }
        else
        {
            Log?.LogWarning("NetworkManager.AESDecrypt256 hook not found");
        }
    }

    private static bool GameDataPathPrefix(ref string __result)
    {
        __result = SharedGameDataDirectory;
        return false;
    }

    private static void GameSendPrefix(IMessage __0)
    {
        try
        {
            if (__0 == null) return;
            byte[] body = __0.ToByteArray();
            long id = Interlocked.Increment(ref Sequence);
            string type = __0.GetType().FullName ?? __0.GetType().Name;
            string file = SaveBody(id, "proto_req", type, body);
            Emit("proto_req", id, "", "", "", 0, body.Length, file, null, type);
        }
        catch (Exception ex)
        {
            Log?.LogWarning("plaintext request capture failed: " + ex.Message);
        }
    }

    private static void DecryptPostfix(string __result)
    {
        try
        {
            if (string.IsNullOrEmpty(__result)) return;
            byte[] body = Convert.FromBase64String(__result);
            long id = Interlocked.Read(ref LastApiRequest);
            string file = SaveBody(id, "proto_resp", "protobuf", body);
            Emit("proto_resp", id, "", "", "", 0, body.Length, file, null, null);
        }
        catch (FormatException) { }
        catch (Exception ex)
        {
            Log?.LogWarning("plaintext response capture failed: " + ex.Message);
        }
    }

    private static bool IsGameApi(Uri uri, string method)
    {
        if (uri.Scheme != Uri.UriSchemeHttp && uri.Scheme != Uri.UriSchemeHttps)
            return false;
        if (string.Equals(method, "GET", StringComparison.OrdinalIgnoreCase))
            return false;
        string host = uri.Host ?? "";
        return host.Equals("bd2.pmang.cloud", StringComparison.OrdinalIgnoreCase) ||
               host.EndsWith(".bd2.pmang.cloud", StringComparison.OrdinalIgnoreCase);
    }

    private static bool TrySafeUri(string value, out Uri uri)
    {
        return Uri.TryCreate(value, UriKind.Absolute, out uri);
    }

    private static string SaveBody(long id, string phase, string path, byte[] body)
    {
        if (body == null || body.Length == 0 || body.Length > MaxBodyBytes)
            return null;
        string safe = new string((path ?? "body")
            .Select(ch => char.IsLetterOrDigit(ch) ? ch : '_').ToArray()).Trim('_');
        if (safe.Length == 0) safe = "body";
        if (safe.Length > 70) safe = safe.Substring(safe.Length - 70);
        string stem = id.ToString("D6") + "_" + phase + "_" + safe;
        string name;
        lock (FileLock)
        {
            name = stem + ".bin";
            int duplicate = 2;
            while (File.Exists(Path.Combine(CaptureDirectory, "bodies", name)))
                name = stem + "_" + duplicate++ + ".bin";
            File.WriteAllBytes(Path.Combine(CaptureDirectory, "bodies", name), body);
        }
        return "bodies/" + name;
    }

    private static void Emit(string ev, long id, string method, string host,
        string path, long status, int length, string rawFile, string result,
        string note)
    {
        string json = "{" +
            "\"ts\":\"" + Escape(DateTime.Now.ToString("O")) + "\"," +
            "\"ev\":\"" + Escape(ev) + "\"," +
            "\"id\":" + id + "," +
            "\"method\":\"" + Escape(method) + "\"," +
            "\"host\":\"" + Escape(host) + "\"," +
            "\"path\":\"" + Escape(path) + "\"," +
            "\"status\":" + status + "," +
            "\"length\":" + length + "," +
            "\"raw_file\":" + JsonString(rawFile) + "," +
            "\"result\":" + JsonString(result) + "," +
            "\"note\":" + JsonString(note) + "}";
        lock (FileLock)
            File.AppendAllText(JsonlPath, json + Environment.NewLine, new UTF8Encoding(false));
    }

    private static string JsonString(string value)
    {
        return value == null ? "null" : "\"" + Escape(value) + "\"";
    }

    private static string Escape(string value)
    {
        if (value == null) return "";
        return value.Replace("\\", "\\\\").Replace("\"", "\\\"")
            .Replace("\r", "\\r").Replace("\n", "\\n");
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
            "BD2 2.34.13 official API capture.\r\n" +
            "No Cookie/Authorization headers or URL query strings are recorded.\r\n" +
            "Raw protobuf/account bodies can still contain private account data. Do not share this directory.\r\n" +
            "persistentDataPath=" + IsolatedDataDirectory + "\r\n",
            new UTF8Encoding(false));
    }
}
