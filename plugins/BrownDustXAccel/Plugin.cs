using System;
using System.Linq;
using BepInEx;
using BepInEx.Logging;
using HarmonyLib;

namespace Bd2BrownDustXAccel;

[BepInPlugin(PluginId, Name, Version)]
[BepInDependency(BrownDustXApi.PluginId, BepInDependency.DependencyFlags.SoftDependency)]
public sealed class Plugin : BaseUnityPlugin
{
    public const string PluginId = "bd2.browndustx.accel";
    public const string Name = "BD2 BrownDustX Accel";
    public const string Version = "0.1.0";
    internal static ManualLogSource Log;

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0051", Justification = "Unity invokes this instance lifecycle callback.")]
    private void Awake()
    {
        Log = Logger;
        AccelSettings.Bind(Config);
        System.Reflection.Assembly assembly = AppDomain.CurrentDomain.GetAssemblies().FirstOrDefault(a => a.GetName().Name == BrownDustXApi.AssemblyName);
        if (assembly == null)
        {
            Logger.LogInfo("BrownDustX is not loaded; character mod acceleration is idle");
            return;
        }
        // Harmony.Dispose removes its patches; these hooks live until process exit.
#pragma warning disable CA2000
        var harmony = new Harmony(PluginId);
#pragma warning restore CA2000
        try
        {
            var api = BrownDustXApi.Bind(assembly);
            ModLoadPatches.Install(harmony, api);
            Logger.LogInfo(AccelSettings.Enabled
                ? FormattableString.Invariant($"Attached to BrownDustX {assembly.GetName().Version}: character mod acceleration on, {DecodeScheduler.WorkerCount} decode threads")
                : FormattableString.Invariant($"Attached to BrownDustX {assembly.GetName().Version}: acceleration off, timing only"));
        }
        catch (Exception error)
        {
            harmony.UnpatchSelf();
            Logger.LogError("BrownDustX acceleration disabled, original loading kept: " + error);
        }
    }
}
