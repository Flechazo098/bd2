using System;
using System.Collections.Generic;
using System.IO;
using System.Reflection;
using BD2.GameNames;
using BepInEx.Logging;
using HarmonyLib;
using UnityEngine;
using static BD2.GameNames.Game;

namespace Bd2CaptureEnvironment;

internal static class CaptureStorageIsolation
{
    private const string PlayerPrefsPrefix = "BD2OfficialCapture:" + Bd2Build.Versions.Game + ":";
    private static ManualLogSource Log;
    internal static string DataDirectory { get; private set; }
    internal static string GameDataDirectory { get; private set; }
    internal static void Initialize(string root, ManualLogSource log)
    {
        Log = log;
        DataDirectory = Path.GetFullPath(Path.Combine(root, "IsolatedUserData"));
        GameDataDirectory = Path.Combine(DataDirectory, "Data", "t");
        EnsurePrivateGameDataDirectory();
    }
    private static void EnsurePrivateGameDataDirectory()
    {
        foreach (string path in new[]
        {
            DataDirectory,
            Path.Combine(DataDirectory, "Data"),
            GameDataDirectory
        })
        {
            if (Directory.Exists(path) &&
                (new DirectoryInfo(path).Attributes & FileAttributes.ReparsePoint) != 0)
                throw new InvalidOperationException("Isolated GameData path is linked: " + path);
            Directory.CreateDirectory(path);
        }
    }

    internal static void Install(Harmony harmony)
    {
        PropertyInfo persistent = typeof(Application).GetGameProperty(
            "persistentDataPath", BindingFlags.Static | BindingFlags.Public);
        MethodInfo getter = persistent?.GetGetMethod();
        if (getter == null)
            throw new MissingMethodException("Application.persistentDataPath getter not found");
        harmony.Patch(getter, prefix: new HarmonyMethod(
            typeof(CaptureStorageIsolation), nameof(PersistentDataPathPrefix)));

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
                typeof(CaptureStorageIsolation), nameof(PlayerPrefsKeyPrefix)));
        }

        MethodInfo deleteAll = typeof(PlayerPrefs).GetGameMethod(
            "DeleteAll", BindingFlags.Static | BindingFlags.Public,
            null, Type.EmptyTypes, null);
        if (deleteAll != null)
            harmony.Patch(deleteAll, prefix: new HarmonyMethod(
                typeof(CaptureStorageIsolation), nameof(BlockPlayerPrefsDeleteAll)));
    }

    private static bool PersistentDataPathPrefix(ref string __result)
    {
        __result = DataDirectory;
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

    internal static void InstallGameDataPath(Harmony harmony)
    {
        MethodInfo gameDataPath = Game.Method<BDNetwork.NetworkManager>(network => network.GetPachedGameDataPath());
        if (gameDataPath == null || gameDataPath.ReturnType != typeof(string))
            throw new MissingMethodException("NetworkManager.GetPachedGameDataPath not found");
        harmony.Patch(gameDataPath, prefix: new HarmonyMethod(
            typeof(CaptureStorageIsolation), nameof(GameDataPathPrefix)));
        Log?.LogInfo("Private installed GameData path hook installed");
    }

    private static bool GameDataPathPrefix(ref string __result)
    {
        __result = GameDataDirectory;
        return false;
    }

}
