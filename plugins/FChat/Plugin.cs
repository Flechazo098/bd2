using System;
using BepInEx;
using BepInEx.Logging;
using HarmonyLib;

namespace Bd2FChat;

[BepInPlugin(PluginId, Name, Version)]
[BepInDependency("bd2.login.ui", BepInDependency.DependencyFlags.HardDependency)]
public sealed class Plugin : BaseUnityPlugin
{
    public const string PluginId = "bd2.fchat";
    public const string Name = "BD2 FChat";
    public const string Version = Bd2Build.Versions.Plugin;
    internal static ManualLogSource Log;

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0051", Justification = "Unity invokes this instance lifecycle callback.")]
    private void Awake()
    {
        Log = Logger;
        try
        {
            // Harmony.Dispose removes its patches; these hooks live until process exit.
#pragma warning disable CA2000
            var harmony = new Harmony(PluginId);
#pragma warning restore CA2000
            NativeChatTranslation.Install(harmony);
            TranslationSettingsUI.Install(harmony);
            ServerTextPolicy.Install(harmony, Logger);
            FriendPresence.Install(harmony);
            Logger.LogInfo("Client chat translation and server text policy installed");
        }
        catch (Exception error)
        {
            Logger.LogError("FChat patch failed: " + error);
        }
    }

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Performance", "CA1822", Justification = "Unity invokes this instance lifecycle callback.")]
    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0051", Justification = "Unity invokes this instance lifecycle callback.")]
    private void OnApplicationQuit() => NativeChatTranslation.Reset(true);
}
