using System;
using System.Collections;
using System.Collections.Generic;
using System.IO;
using System.Reflection;
using System.Reflection.Emit;
using System.Security.Cryptography;
using System.Text;
using System.Threading;
using BD2.GameNames;
using Bd2.Client;
using BepInEx;
using BepInEx.Logging;
using HarmonyLib;
using Newtonsoft.Json;
using UnityEngine;

namespace Bd2LocalIdentity;

internal static class ClientInstanceIsolation
{
    private static string PersistentPath;
    private static string TemporaryPath;
    private static string BundlePath;
    private static Mutex InstanceMutex;
    private static ManualLogSource Log;
    private static bool Active;
    private static readonly HashSet<string> SharedBundlePaths = new(StringComparer.OrdinalIgnoreCase);
    private static MethodInfo AddReadOnlyCache;

    internal static void Install(Harmony harmony, ManualLogSource log)
    {
        Log = log;
        try
        {
            ClientInstance instance = ClientSettings.CurrentInstance;
            if (instance == null) return;
            string id = instance.ID;
            string root = instance.Root;

            using (var sha = SHA256.Create())
            {
                bool windows = Environment.OSVersion.Platform == PlatformID.Win32NT;
                string identity = windows ? root.ToUpperInvariant() : root;
                string digest = BitConverter.ToString(sha.ComputeHash(Encoding.UTF8.GetBytes(identity))).Replace("-", string.Empty);
                InstanceMutex = new Mutex(false, (windows ? "Global\\" : string.Empty) + "BD2.LocalClient.Instance." + digest, out bool created);
                if (!created) throw new InvalidOperationException("This client instance is already running");
            }

            string nativePersistentPath = Application.persistentDataPath;
            var resourceQuality = new Dictionary<string, int>(StringComparer.Ordinal);
            foreach (string key in new[] { "Resolution", "SoundQuality" })
                if (PlayerPrefs.HasKey(key)) resourceQuality.Add(key, PlayerPrefs.GetInt(key));
            CaptureSharedBundlePaths();
            AddReadOnlyCache = AccessTools.DeclaredMethod(typeof(Caching), "AddCache", [typeof(string), typeof(bool)]);
            if (AddReadOnlyCache == null || AddReadOnlyCache.ReturnType != typeof(Cache))
                throw new MissingMethodException("Unity read-only asset bundle cache API was not found");

            PersistentPath = Path.Combine(root, "unity", "persistent");
            TemporaryPath = Path.Combine(root, "unity", "cache");
            BundlePath = Path.Combine(root, "unity", "bundles");
            Directory.CreateDirectory(PersistentPath);
            Directory.CreateDirectory(TemporaryPath);
            Directory.CreateDirectory(BundlePath);
            InstancePlayerPreferences.Initialize(Path.Combine(root, "unity", "playerprefs.json"));
            foreach (KeyValuePair<string, int> quality in resourceQuality)
                if (!InstancePlayerPreferences.HasKey(quality.Key)) InstancePlayerPreferences.SetInt(quality.Key, quality.Value);

            Replace(harmony, AccessTools.PropertyGetter(typeof(Application), nameof(Application.persistentDataPath)), nameof(GetPersistentPath));
            Replace(harmony, AccessTools.PropertyGetter(typeof(Application), nameof(Application.temporaryCachePath)), nameof(GetTemporaryPath));
            foreach (string name in new[] { "GetInt", "GetFloat", "GetString", "SetInt", "SetFloat", "SetString", "HasKey", "DeleteKey", "DeleteAll", "Save" })
            {
                MethodInfo replacement = AccessTools.DeclaredMethod(typeof(InstancePlayerPreferences), name);
                Type[] arguments = Array.ConvertAll(replacement.GetParameters(), parameter => parameter.ParameterType);
                Replace(harmony, AccessTools.DeclaredMethod(typeof(PlayerPrefs), name, arguments), replacement);
            }

            MethodInfo gamePath = Game.Method(() => gamfs.GameUtil.GetPath(default));
            harmony.Patch(gamePath, prefix: new HarmonyMethod(typeof(ClientInstanceIsolation), nameof(GamePathPrefix)));
            FieldInfo tempDirectory = typeof(gamfs.GameUtil).GetGameField("tempDirectory", BindingFlags.Static | BindingFlags.NonPublic);
            if (tempDirectory == null || tempDirectory.FieldType != typeof(string))
                throw new MissingFieldException("GameUtil.tempDirectory was not found");
            tempDirectory.SetValue(null, Path.Combine(PersistentPath, ".tmp"));

            Patch(harmony, Game.Method(() => AppManager.TryCheckDuplicatedExecution()), nameof(CheckInstancePrefix));
            Patch(harmony, Game.Method<AppManager>(app => app.GetSharedCacheFolderPath(default)), nameof(BundlePathPrefix));
            Patch(harmony, Game.Method<AppManager>(app => app.TryChangeCaching(default)), nameof(ChangeCachePrefix));
            Patch(harmony, Game.Method<AppManager>(app => app.MarkCacheAsUsed(default)), nameof(MarkCachePrefix));
            Patch(harmony, Game.Method<AppManager>(app => app.RevertInvalidCaching()), nameof(RevertCachePrefix));

            Active = true;
            AppDomain.CurrentDomain.ProcessExit += delegate { SaveOnExit(); };
            log.LogInfo("Client instance isolation active: id=" + id + " root=" + root);
            log.LogInfo("Instance PlayerPrefs imports only missing graphics/sound quality selections; credentials remain isolated");
            log.LogInfo("Native persistent asset source: " + nativePersistentPath);
            log.LogInfo("Unity native startup preferences precede plugin initialization; managed preferences and paths are isolated from this point");
        }
        catch (Exception ex)
        {
            StopInstance("Client instance isolation initialization failed", ex);
        }
    }

    private static void CaptureSharedBundlePaths()
    {
        string drive = Path.GetPathRoot(Paths.GameRootPath);
        SharedBundlePaths.Add(Path.Combine(drive, Application.companyName + "_" + Application.productName));
        string savedPaths = PlayerPrefs.GetString("StandaloneCachingPathList", string.Empty);
        if (string.IsNullOrEmpty(savedPaths)) return;
        try
        {
            List<string> paths = JsonConvert.DeserializeObject<List<string>>(savedPaths);
            if (paths == null) return;
            foreach (string path in paths)
                if (!string.IsNullOrEmpty(path) && Path.IsPathFullyQualified(path))
                    SharedBundlePaths.Add(Path.GetFullPath(path));
        }
        catch (Exception ex)
        {
            Log.LogWarning("Could not read native asset cache path metadata: " + ex.Message);
        }
    }

    private static void Patch(Harmony harmony, MethodInfo original, string prefix)
    {
        if (original == null) throw new MissingMethodException("Instance isolation target was not found: " + prefix);
        harmony.Patch(original, prefix: new HarmonyMethod(typeof(ClientInstanceIsolation), prefix));
    }

    private static void Replace(Harmony harmony, MethodInfo original, string replacement) =>
        Replace(harmony, original, AccessTools.DeclaredMethod(typeof(ClientInstanceIsolation), replacement));

    private static readonly Dictionary<MethodBase, MethodInfo> Replacements = [];

    private static void Replace(Harmony harmony, MethodInfo original, MethodInfo replacement)
    {
        if (original == null || replacement == null) throw new MissingMethodException("Instance isolation replacement target was not found");
        Replacements.Add(original, replacement);
        // Unity InternalCall methods have no managed body, so they require a replacement transpiler.
        harmony.Patch(original, transpiler: new HarmonyMethod(typeof(ClientInstanceIsolation), nameof(ReplaceBody)));
    }

    private static IEnumerable<CodeInstruction> ReplaceBody(MethodBase __originalMethod)
    {
        MethodInfo replacement = Replacements[__originalMethod];
        for (int index = 0; index < replacement.GetParameters().Length; index++)
            yield return new CodeInstruction(OpCodes.Ldarg, (short)index);
        yield return new CodeInstruction(OpCodes.Call, replacement);
        yield return new CodeInstruction(OpCodes.Ret);
    }

    private static string GetPersistentPath() => PersistentPath;
    private static string GetTemporaryPath() => TemporaryPath;

    private static bool GamePathPrefix(gamfs.GameUtil.PathType __0, ref string __result)
    {
        if (__0 != gamfs.GameUtil.PathType.Persistent) return true;
        __result = PersistentPath;
        return false;
    }

    private static bool CheckInstancePrefix(ref bool __result)
    {
        __result = InstanceMutex != null;
        return false;
    }

    private static bool BundlePathPrefix(ref string __result)
    {
        __result = BundlePath;
        return false;
    }

    private static void ChangeCachePrefix(ref string __0) => __0 = BundlePath;

    private static void MarkCachePrefix(ref Cache __0) => __0 = InstanceCache();

    private static bool RevertCachePrefix()
    {
        ActivateCache();
        return false;
    }

    private static Cache InstanceCache()
    {
        try
        {
            Cache cache = Caching.GetCacheByPath(BundlePath);
            if (!cache.valid) cache = Caching.AddCache(BundlePath);
            if (!cache.valid) throw new IOException("Unity could not create the instance asset bundle cache");
            return cache;
        }
        catch (Exception ex)
        {
            StopInstance("Client instance bundle cache initialization failed", ex);
            throw;
        }
    }

    internal static IEnumerator InitializeCache()
    {
        if (!Active) yield break;
        while (!IsCacheReady()) yield return null;
        MountSharedCaches();
        ActivateCache();
        Log.LogInfo("Instance asset bundle cache active: " + BundlePath);
    }

    private static void MountSharedCaches()
    {
        try
        {
            foreach (string path in SharedBundlePaths)
            {
                if (!Directory.Exists(path) || string.Equals(path, BundlePath, StringComparison.OrdinalIgnoreCase)) continue;
                Cache shared = Caching.GetCacheByPath(path);
                if (shared.valid)
                {
                    if (!shared.readOnly)
                    {
                        Log.LogWarning("Asset cache is already registered writable by Unity; skipping read-only registration: " + path);
                        continue;
                    }
                }
                else shared = (Cache)AddReadOnlyCache.Invoke(null, [path, true]);
                if (!shared.valid || !shared.readOnly)
                    throw new IOException("Unity could not register the shared asset bundle cache read-only: " + path);
                Log.LogInfo("Shared asset bundle cache mounted read-only: " + shared.path);
            }
        }
        catch (Exception ex)
        {
            StopInstance("Could not mount shared Unity bundle caches read-only", ex);
        }
    }

    private static bool IsCacheReady()
    {
        try { return Caching.ready; }
        catch (Exception ex)
        {
            StopInstance("Could not inspect Unity cache readiness", ex);
            throw;
        }
    }

    private static void ActivateCache()
    {
        try
        {
            Cache cache = InstanceCache();
            Caching.currentCacheForWriting = cache;
            if (!string.Equals(Caching.GetCacheAt(0).path, cache.path, StringComparison.OrdinalIgnoreCase))
                Caching.MoveCacheBefore(cache, Caching.GetCacheAt(0));
        }
        catch (Exception ex)
        {
            StopInstance("Could not activate the isolated Unity bundle cache", ex);
        }
    }

    internal static void SaveOnExit()
    {
        if (!Active) return;
        try { InstancePlayerPreferences.Save(); }
        catch (Exception ex) { Log?.LogError("Could not save instance PlayerPrefs: " + ex.Message); }
    }

    private static void StopInstance(string message, Exception exception)
    {
        Log?.LogFatal(message + ": " + exception);
        Environment.Exit(1);
    }
}
