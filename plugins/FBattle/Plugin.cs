using System;
using BepInEx;
using BepInEx.Logging;
using HarmonyLib;

namespace Bd2FBattle;

[BepInPlugin(PluginId, Name, Version)]
[BepInDependency("bd2.localidentity", BepInDependency.DependencyFlags.HardDependency)]
public sealed class Plugin : BaseUnityPlugin
{
    public const string PluginId = "bd2.fbattle";
    public const string Name = "BD2 FBattle";
    public const string Version = Bd2Build.Versions.Plugin;
    internal static ManualLogSource Log;

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0051", Justification = "Unity invokes this lifecycle callback.")]
    private void Awake()
    {
        Log = Logger;
#pragma warning disable CA2000
        var harmony = new Harmony(PluginId);
#pragma warning restore CA2000
        try
        {
            PresetPlans.Install(harmony);
            BattleExecution.Install(harmony);
            PresetEditorEntry.Install(harmony);
            Logger.LogInfo("Preset action editor and first-turn battle plans installed");
        }
        catch (Exception error)
        {
            harmony.UnpatchSelf();
            Logger.LogError("FBattle initialization failed: " + error);
        }
    }
}
