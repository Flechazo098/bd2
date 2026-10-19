using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Runtime.CompilerServices;
using System.Threading;
using Cysharp.Threading.Tasks;
using Spine.Unity;
using UnityEngine;

namespace Bd2BrownDustXAccel;

/// <summary>
/// Replacements for BrownDustX's character mod loading. Each hook returns false to fall through to the original
/// method (non-character mods, acceleration off); otherwise it reproduces the original result and error messages.
/// </summary>
internal static class ModLoader
{
    private static readonly ConditionalWeakTable<object, DeferredSkeleton> DeferredSkeletons = [];
    private static readonly ConditionalWeakTable<object, LazyWork> PreparedWork = [];
    private static MethodInfo prepareRuntimeMethod;

    internal static BrownDustXApi Api { get; set; }

    /// <summary>ModManager.LoadModData(string, Modfile, List&lt;string&gt;): startup preload ("Preload all Mods" = Yes).</summary>
    internal static bool LoadModDataByName(string modname, object modfile, List<string> invalidMods, out object result)
    {
        result = null;
        if (!ModNames.IsCharacter(modname))
        {
            return false;
        }
        long started = Stopwatch.GetTimestamp();
        ModPrefetch prefetch = null;
        try
        {
            string folder = Api.ModfileFolder(modfile);
            if (string.IsNullOrEmpty(folder))
            {
                throw new ModLoadException(modname + " <color=#FF4C4C>(Unknown Error)</color>");
            }
            if (Api.SpineShader == null)
            {
                throw new ModLoadException(Api.ShortPath(folder) + " <color=#FF4C4C>(Couldn't initialize Shader)</color>");
            }
            prefetch = PreloadSession.Current?.Take(folder);
            TextAsset skeleton = null;
            string json = Directory.GetFiles(folder, "*.json").FirstOrDefault();
            if (json != null)
            {
                if (Path.GetFileNameWithoutExtension(json).Contains(".skel", StringComparison.Ordinal))
                {
                    throw new ModLoadException(Api.ShortPath(folder) + " <color=#FF4C4C>(Invalid .json file)</color>");
                }
                skeleton = new TextAsset(prefetch?.TakeSkeleton(json)?.Text ?? File.ReadAllText(json)) { name = Path.GetFileName(json) };
            }
            else
            {
                string skel = Directory.GetFiles(folder, "*.skel").FirstOrDefault();
                if (skel != null)
                {
                    if (Path.GetFileNameWithoutExtension(skel).Contains(".json", StringComparison.Ordinal))
                    {
                        throw new ModLoadException(Api.ShortPath(folder) + " <color=#FF4C4C>(Invalid .skel file)</color>");
                    }
                    skeleton = BinarySkeleton(folder, skel, prefetch?.TakeSkeleton(skel)?.Bytes);
                }
            }
            SpineAtlasAsset atlas = null;
            string atlasPath = Directory.GetFiles(folder, "*.atlas").FirstOrDefault();
            if (atlasPath == null)
            {
                if (Directory.EnumerateFiles(folder, modname + "*.png").Any())
                {
                    throw new ModLoadException(Api.ShortPath(folder) + " <color=#4CFF4C>(Missing .atlas file)</color>");
                }
            }
            else
            {
                string atlasText = prefetch?.TakeAtlasText(atlasPath) ?? File.ReadAllText(atlasPath);
                List<string> pages = AtlasPages.Parse(atlasText);
                if (pages.Count > 0)
                {
                    string[] paths = pages.Select(page => Path.Combine(folder, page + ".png")).ToArray();
                    DecodeJob[] jobs = prefetch?.TakePageJobs(atlasPath, pages);
                    if (jobs != null)
                    {
                        LoadStats.PrefetchedPages += jobs.Length;
                    }
                    jobs ??= StartPages(paths, true);
                    atlas = ModAssets.BuildAtlas(Api, folder, atlasText, [.. pages], jobs, paths, true, Api.ShouldCompressTextures, []);
                }
            }
            LoadStats.Mods++;
            if (skeleton == null && atlas == null)
            {
                return true;
            }
            result = Api.NewModData(skeleton, atlas);
            return true;
        }
        catch (Exception error)
        {
            invalidMods?.Add(error.Message ?? "");
            result = null;
            return true;
        }
        finally
        {
            prefetch?.Cancel();
            if (AccelSettings.VerboseTiming)
            {
                Plugin.Log.LogInfo(FormattableString.Invariant($"Preloaded {modname} in {LoadStats.Milliseconds(Stopwatch.GetTimestamp() - started)} ms"));
            }
        }
    }

    /// <summary>ModManager.PrepareModForLoading: on-demand mode registration; the skeleton is read only when the mod is first shown.</summary>
    internal static bool PrepareModForLoading(string modname, object modfile, List<string> invalidMods, out object result)
    {
        result = null;
        if (!ModNames.IsCharacter(modname))
        {
            return false;
        }
        try
        {
            string folder = Api.ModfileFolder(modfile);
            if (string.IsNullOrEmpty(folder))
            {
                throw new ModLoadException(modname + " <color=#FF4C4C>(Unknown Error)</color>");
            }
            if (Api.SpineShader == null)
            {
                throw new ModLoadException(Api.ShortPath(folder) + " <color=#FF4C4C>(Couldn't initialize Shader)</color>");
            }
            string atlas = Directory.GetFiles(folder, "*.atlas").FirstOrDefault();
            string[] textures = null;
            if (atlas == null)
            {
                if (Directory.EnumerateFiles(folder, modname + "*.png").Any())
                {
                    throw new ModLoadException(Api.ShortPath(folder) + " <color=#4CFF4C>(Missing .atlas file)</color>");
                }
            }
            else
            {
                List<string> pages = AtlasPages.Parse(File.ReadAllText(atlas));
                if (pages.Count <= 0)
                {
                    throw new ModLoadException(Api.ShortPath(folder) + " <color=#4CFF4C>(Invalid .atlas file)</color>");
                }
                textures = new string[pages.Count];
                for (int i = 0; i < pages.Count; i++)
                {
                    textures[i] = Path.Combine(folder, pages[i] + ".png");
                    if (!File.Exists(textures[i]))
                    {
                        throw ModLoadException.MissingTextures(Api, folder);
                    }
                }
            }
            DeferredSkeleton skeleton = null;
            string json = Directory.GetFiles(folder, "*.json").FirstOrDefault();
            if (json != null)
            {
                if (Path.GetFileNameWithoutExtension(json).Contains(".skel", StringComparison.Ordinal))
                {
                    throw new ModLoadException(Api.ShortPath(folder) + " <color=#FF4C4C>(Invalid .json file)</color>");
                }
                skeleton = new DeferredSkeleton(json, true);
            }
            else
            {
                string skel = Directory.GetFiles(folder, "*.skel").FirstOrDefault();
                if (skel != null)
                {
                    if (Path.GetFileNameWithoutExtension(skel).Contains(".json", StringComparison.Ordinal))
                    {
                        throw new ModLoadException(Api.ShortPath(folder) + " <color=#FF4C4C>(Invalid .skel file)</color>");
                    }
                    skeleton = new DeferredSkeleton(skel, false);
                }
            }
            if (skeleton == null && atlas == null)
            {
                return true;
            }
            result = Api.NewPreload(modname, modfile, atlas, textures);
            if (skeleton != null)
            {
                DeferredSkeletons.Add(result, skeleton);
            }
            return true;
        }
        catch (Exception error)
        {
            invalidMods?.Add(error.Message ?? "");
            result = null;
            return true;
        }
    }

    /// <summary>ModManager.LoadModData(PreloadModData): synchronous on-demand load (Spine Initialize hooks, intro UI).</summary>
    internal static bool LoadModDataPreloaded(object preload, out object result)
    {
        result = null;
        if (preload == null || !ModNames.IsCharacter(Api.PreloadName(preload)) || Api.PreloadModfile(preload) == null)
        {
            return false;
        }
        result = SceneCache.TryGet(preload) ?? Finish(StartLazy(preload), true);
        return true;
    }

    /// <summary>SpineReplacer.PrepareRuntimeAsync: decodes every page in parallel off the main thread.</summary>
    internal static bool PrepareRuntimeAsync(object preload, out object result)
    {
        result = null;
        if (preload == null || !ModNames.IsCharacter(Api.PreloadName(preload)))
        {
            return false;
        }
        prepareRuntimeMethod ??= typeof(ModLoader).GetMethod(nameof(PrepareRuntime), BindingFlags.Static | BindingFlags.NonPublic).MakeGenericMethod(Api.PreparedRuntime);
        result = prepareRuntimeMethod.Invoke(null, [preload]);
        return true;
    }

    /// <summary>SpineReplacer.BuildModDataFromPrepared: uploads the pages decoded by <see cref="PrepareRuntimeAsync"/>.</summary>
    internal static bool BuildModDataFromPrepared(object prepared, out object result)
    {
        result = null;
        if (prepared == null || !PreparedWork.TryGetValue(prepared, out LazyWork work))
        {
            return false;
        }
        PreparedWork.Remove(prepared);
        result = work.Cached ?? Finish(work, false);
        return true;
    }

    /// <summary>Generic body of the PrepareRuntimeAsync replacement, closed over SpineReplacer.PreparedRuntime via reflection.</summary>
    internal static UniTask<T> PrepareRuntime<T>(object preload) where T : class
    {
        var prepared = (T)Activator.CreateInstance(typeof(T));
        object modfile = Api.PreloadModfile(preload);
        Api.FillPrepared(prepared, modfile == null ? null : Api.ModfileFolder(modfile), Api.PreloadSkeleton(preload), null, Api.PreloadAtlasTextures(preload));
        object cached = SceneCache.TryGet(preload);
        if (cached != null)
        {
            PreparedWork.Add(prepared, new LazyWork { Preload = preload, Cached = cached });
            return UniTask.FromResult(prepared);
        }
        return UniTask.RunOnThreadPool(() =>
        {
            LazyWork work = StartLazy(preload);
            work.Skeleton?.Complete();
            foreach (DecodeJob job in work.Pages ?? [])
            {
                job.Complete();
            }
            PreparedWork.Add(prepared, work);
            return prepared;
        }, true, CancellationToken.None);
    }

    private static LazyWork StartLazy(object preload)
    {
        object modfile = Api.PreloadModfile(preload);
        string folder = modfile == null ? null : Api.ModfileFolder(modfile);
        if (string.IsNullOrEmpty(folder))
        {
            throw new ModLoadException(Api.PreloadName(preload) + " <color=#FF4C4C>(Unknown Error)</color>");
        }
        var work = new LazyWork { Preload = preload, Name = Api.PreloadName(preload), Folder = folder, Started = Stopwatch.GetTimestamp() };
        if (Api.PreloadSkeleton(preload) == null && DeferredSkeletons.TryGetValue(preload, out DeferredSkeleton skeleton))
        {
            work.SkeletonSource = skeleton;
            work.Skeleton = new DecodeJob(SkeletonResult.Work(skeleton.Path, skeleton.Json, null, 0));
            DecodeScheduler.Enqueue(work.Skeleton, true);
        }
        string atlas = Api.PreloadAtlasFile(preload);
        if (atlas != null)
        {
            work.AtlasText = File.ReadAllText(atlas);
            work.Paths = Api.PreloadAtlasTextures(preload);
            work.Pages = StartPages(work.Paths, false);
        }
        return work;
    }

    private static object Finish(LazyWork work, bool nullWhenEmpty)
    {
        if (Api.SpineShader == null)
        {
            throw new ModLoadException(Api.ShortPath(work.Folder) + " <color=#FF4C4C>(Couldn't initialize Shader)</color>");
        }
        long mainStarted = Stopwatch.GetTimestamp();
        TextAsset skeleton = Api.PreloadSkeleton(work.Preload);
        if (skeleton == null && work.Skeleton != null)
        {
            DeferredSkeleton source = work.SkeletonSource;
            var read = (SkeletonResult)work.Skeleton.Wait();
            string name = Path.GetFileName(source.Path);
            skeleton = source.Json
                ? new TextAsset(read?.Text ?? File.ReadAllText(source.Path)) { name = name }
                : BinarySkeleton(work.Folder, source.Path, read?.Bytes);
            Api.SetPreloadSkeleton(work.Preload, skeleton);
        }
        SpineAtlasAsset atlas = null;
        var textures = new List<Texture2D>();
        if (work.AtlasText != null)
        {
            string[] names = work.Paths.Select(Path.GetFileNameWithoutExtension).ToArray();
            atlas = ModAssets.BuildAtlas(Api, work.Folder, work.AtlasText, names, work.Pages, work.Paths, false, false, textures);
        }
        if (nullWhenEmpty && skeleton == null && atlas == null)
        {
            return null;
        }
        object data = Api.NewModData(skeleton, atlas);
        if (textures.Count > 0)
        {
            SceneCache.Add(work.Preload, data, textures);
        }
        long finished = Stopwatch.GetTimestamp();
        Plugin.Log.LogInfo(FormattableString.Invariant(
            $"On-demand load {work.Name}: {textures.Count} pages in {LoadStats.Milliseconds(finished - work.Started)} ms, main thread {LoadStats.Milliseconds(finished - mainStarted)} ms"));
        return data;
    }

    private static DecodeJob[] StartPages(string[] paths, bool mipmaps)
    {
        DecodeJob[] jobs = paths.Select(path => new DecodeJob(PageResult.Work(path, mipmaps, null, 0))).ToArray();
        foreach (DecodeJob job in jobs.Skip(1))
        {
            DecodeScheduler.Enqueue(job, true);
        }
        return jobs;
    }

    private static TextAsset BinarySkeleton(string folder, string path, byte[] prefetched)
    {
        try
        {
            return ModAssets.SkeletonFromBytes(prefetched ?? File.ReadAllBytes(path), Path.GetFileName(path));
        }
        catch (Exception error)
        {
            throw new ModLoadException(Api.ShortPath(folder) + " <color=#FF4C4C>" + error.Message + "</color>", error);
        }
    }

    private sealed class DeferredSkeleton(string path, bool json)
    {
        internal string Path { get; } = path;
        internal bool Json { get; } = json;
    }

    private sealed class LazyWork
    {
        internal object Preload { get; set; }
        internal object Cached { get; set; }
        internal string Name { get; set; }
        internal string Folder { get; set; }
        internal long Started { get; set; }
        internal DeferredSkeleton SkeletonSource { get; set; }
        internal DecodeJob Skeleton { get; set; }
        internal string AtlasText { get; set; }
        internal string[] Paths { get; set; }
        internal DecodeJob[] Pages { get; set; }
    }
}
