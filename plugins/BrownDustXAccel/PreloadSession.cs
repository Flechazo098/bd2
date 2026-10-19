using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text.RegularExpressions;
using System.Threading;

namespace Bd2BrownDustXAccel;

/// <summary>
/// Walks the mods folder in the same order as BrownDustX's CheckModFolders and queues every character mod's
/// skeleton and atlas pages for background decoding, so the main thread only uploads finished textures.
/// </summary>
internal sealed class PreloadSession
{
    private static readonly Regex SpecialType = new("\"type\"\\s*:\\s*\"special\"", RegexOptions.IgnoreCase | RegexOptions.CultureInvariant);
    private readonly ConcurrentDictionary<string, ModPrefetch> byFolder = new(StringComparer.OrdinalIgnoreCase);
    private readonly List<ModPrefetch> ordered = [];
    private readonly PrefetchBudget budget = new(AccelSettings.PrefetchBudgetBytes);
    private volatile bool closed;
    private int releasedUpTo;

    internal static PreloadSession Current { get; private set; }

    internal static void Start(string root)
    {
        var session = new PreloadSession();
        Current = session;
        new Thread(() => session.Produce(root)) { IsBackground = true, Name = "BrownDustXAccel scan", Priority = ThreadPriority.BelowNormal }.Start();
    }

    internal static void Stop()
    {
        PreloadSession session = Current;
        Current = null;
        if (session == null)
        {
            return;
        }
        session.closed = true;
        session.budget.Close();
        lock (session.ordered)
        {
            foreach (ModPrefetch mod in session.ordered)
            {
                mod.Cancel();
            }
        }
    }

    /// <summary>Hands a mod's prefetched work to the consumer and drops skipped mods queued before it (conflicts, invalid mods).</summary>
    internal ModPrefetch Take(string folder)
    {
        if (folder == null || !byFolder.TryGetValue(folder, out ModPrefetch mod) || mod.Taken)
        {
            return null;
        }
        mod.Taken = true;
        budget.Demand(mod.Sequence);
        lock (ordered)
        {
            for (; releasedUpTo < mod.Sequence; releasedUpTo++)
            {
                if (!ordered[releasedUpTo].Taken)
                {
                    ordered[releasedUpTo].Cancel();
                }
            }
        }
        return mod;
    }

    private void Produce(string root)
    {
        try
        {
            Walk(root, 0);
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException)
        {
            Plugin.Log.LogWarning("Character mod prefetch stopped: " + error.Message);
        }
    }

    private void Walk(string folder, int depth)
    {
        if (closed || depth > 5)
        {
            return;
        }
        if (depth != 0)
        {
            var modfiles = Directory.EnumerateFiles(folder, "*.modfile")
                .Where(f => !string.Equals(Path.GetFileName(f), "intro.modfile", StringComparison.OrdinalIgnoreCase)).Take(2).ToList();
            if (modfiles.Count == 1)
            {
                Plan(folder, modfiles[0]);
                return;
            }
        }
        foreach (string child in Directory.EnumerateDirectories(folder))
        {
            Walk(child, depth + 1);
        }
    }

    /// <summary>Runs on the single producer thread, so sequence numbers follow the order mods are added.</summary>
    private void Plan(string folder, string modfile)
    {
        string name = Path.GetFileNameWithoutExtension(modfile);
        if (!ModNames.IsCharacter(name) || name.Contains("_atlas_", StringComparison.OrdinalIgnoreCase) || SpecialType.IsMatch(File.ReadAllText(modfile)))
        {
            return;
        }
        var mod = new ModPrefetch { Folder = folder, Sequence = ordered.Count };
        string json = Directory.GetFiles(folder, "*.json").FirstOrDefault();
        string skeleton = json ?? Directory.GetFiles(folder, "*.skel").FirstOrDefault();
        if (skeleton != null)
        {
            mod.SkeletonPath = skeleton;
            mod.SkeletonJob = new DecodeJob(SkeletonResult.Work(skeleton, json != null, budget, mod.Sequence));
        }
        string atlas = Directory.GetFiles(folder, "*.atlas").FirstOrDefault();
        if (atlas != null)
        {
            mod.AtlasPath = atlas;
            mod.AtlasText = File.ReadAllText(atlas);
            mod.PageNames = AtlasPages.Parse(mod.AtlasText);
            mod.PageJobs = mod.PageNames
                .Select(page => new DecodeJob(PageResult.Work(Path.Combine(folder, page + ".png"), true, budget, mod.Sequence)))
                .ToArray();
        }
        lock (ordered)
        {
            if (closed)
            {
                return;
            }
            ordered.Add(mod);
        }
        byFolder[folder] = mod;
        if (mod.SkeletonJob != null)
        {
            DecodeScheduler.Enqueue(mod.SkeletonJob, false);
        }
        foreach (DecodeJob job in mod.PageJobs ?? [])
        {
            DecodeScheduler.Enqueue(job, false);
        }
    }
}

internal sealed class ModPrefetch
{
    internal int Sequence { get; set; }
    internal string Folder { get; set; }
    internal bool Taken { get; set; }
    internal string SkeletonPath { get; set; }
    internal DecodeJob SkeletonJob { get; set; }
    internal string AtlasPath { get; set; }
    internal string AtlasText { get; set; }
    internal List<string> PageNames { get; set; }
    internal DecodeJob[] PageJobs { get; set; }

    internal SkeletonResult TakeSkeleton(string path)
    {
        if (SkeletonJob == null || !string.Equals(SkeletonPath, path, StringComparison.OrdinalIgnoreCase))
        {
            return null;
        }
        var result = (SkeletonResult)SkeletonJob.Wait();
        result?.Release();
        return result;
    }

    internal string TakeAtlasText(string path) =>
        string.Equals(AtlasPath, path, StringComparison.OrdinalIgnoreCase) ? AtlasText : null;

    internal DecodeJob[] TakePageJobs(string path, List<string> pages) =>
        PageJobs != null && string.Equals(AtlasPath, path, StringComparison.OrdinalIgnoreCase) && PageNames.SequenceEqual(pages) ? PageJobs : null;

    internal void Cancel()
    {
        SkeletonJob?.Cancel();
        foreach (DecodeJob job in PageJobs ?? [])
        {
            job.Cancel();
        }
    }
}
