using System;
using BepInEx.Configuration;

namespace Bd2BrownDustXAccel;

internal static class AccelSettings
{
    private const long Megabyte = 1024 * 1024;

    internal static bool Enabled { get; private set; }
    internal static int DecodeThreads { get; private set; }
    internal static long PrefetchBudgetBytes { get; private set; }
    internal static long SceneCacheBytes { get; private set; }
    internal static bool VerboseTiming { get; private set; }

    internal static void Bind(ConfigFile config)
    {
        Enabled = config.Bind("General", "Enabled", true,
            "Accelerate BrownDustX character (Spine) mod loading. When false only load timings are logged, for comparison. Requires restart.").Value;
        DecodeThreads = config.Bind("Performance", "DecodeThreads", 0,
            "Background PNG decode threads. 0 uses the processor count minus one, capped at 8. Requires restart.").Value;
        PrefetchBudgetBytes = Megabyte * Math.Max(64, config.Bind("Performance", "PrefetchBudgetMB", 1024,
            "Decoded-but-not-yet-uploaded texture memory allowed while preloading all mods at startup. Requires restart.").Value);
        SceneCacheBytes = Megabyte * Math.Max(0, config.Bind("Performance", "SceneCacheMB", 512,
            "Texture memory kept across scene changes when BrownDustX loads mods on demand (Preload all Mods = No). 0 disables. Requires restart.").Value);
        VerboseTiming = config.Bind("Logging", "VerboseTiming", false,
            "Log one timing line per character mod during the startup preload.").Value;
    }
}
