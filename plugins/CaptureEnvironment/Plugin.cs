using System;
using System.IO;
using System.Text;
using BepInEx;
using HarmonyLib;
using UnityEngine;

namespace Bd2CaptureEnvironment;

[BepInPlugin(PluginId, Name, Version)]
public sealed class Plugin : BaseUnityPlugin
{
    public const string PluginId = "bd2.capture.environment";
    public const string Name = "BD2 Capture Environment";
    public const string Version = Bd2Build.Versions.Plugin;

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0051", Justification = "Unity invokes this instance lifecycle callback.")]
    private void Awake()
    {
        try
        {
            CaptureStorageIsolation.Initialize(Paths.GameRootPath, Logger);
            CaptureWriter.Initialize(Paths.GameRootPath, Logger);

            // Harmony.Dispose calls UnpatchSelf; these hooks live until process exit.
#pragma warning disable CA2000
            var harmony = new Harmony(PluginId);
#pragma warning restore CA2000
            CaptureStorageIsolation.Install(harmony);
            string effectiveDataPath = Application.persistentDataPath;
            if (!string.Equals(
                    Path.GetFullPath(effectiveDataPath).TrimEnd(Path.DirectorySeparatorChar),
                    CaptureStorageIsolation.DataDirectory.TrimEnd(Path.DirectorySeparatorChar),
                    StringComparison.OrdinalIgnoreCase))
                throw new InvalidOperationException(
                    "persistentDataPath isolation verification failed: " + effectiveDataPath);
            CaptureStorageIsolation.InstallGameDataPath(harmony);
            PacketCapture.Install(harmony, Logger);
            WriteMetadata();
            Logger.LogInfo("Official capture environment active");
            Logger.LogInfo("persistentDataPath => " + CaptureStorageIsolation.DataDirectory);
            Logger.LogInfo("isolated GameData => " + CaptureStorageIsolation.GameDataDirectory);
            Logger.LogInfo("capture => " + CaptureWriter.DirectoryPath);
        }
        catch (Exception ex)
        {
            Logger.LogError("Capture environment setup failed: " + ex);
            CaptureWriter.MarkIncomplete("capture environment setup failed", ex);
        }
    }

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Performance", "CA1822", Justification = "Unity invokes this instance lifecycle callback.")]
    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0051", Justification = "Unity invokes this instance lifecycle callback.")]
    private void OnApplicationQuit()
    {
        CaptureWriter.Stop();
    }

    private static void WriteMetadata()
    {
        File.WriteAllText(Path.Combine(CaptureWriter.DirectoryPath, "README.txt"),
            "BD2 " + Bd2Build.Versions.Game + " official API capture.\r\n" +
            "All NetworkManager plaintext protobuf requests and responses are recorded.\r\n" +
            "capture.jsonl is machine-readable; capture.log is the aligned human-readable index.\r\n" +
            "If INCOMPLETE.txt exists, the writer rejected or could not flush part of the capture.\r\n" +
            "No Cookie/Authorization headers or URL query strings are recorded.\r\n" +
            "Raw protobuf/account bodies can still contain private account data. Do not share this directory.\r\n" +
            "clientVersion=" + Application.version + "\r\n" +
            "persistentDataPath=" + CaptureStorageIsolation.DataDirectory + "\r\n" +
            "gameDataPath=" + CaptureStorageIsolation.GameDataDirectory + "\r\n",
            new UTF8Encoding(false));
    }
}
