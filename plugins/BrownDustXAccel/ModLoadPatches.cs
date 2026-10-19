using System;
using System.Collections;
using System.Collections.Generic;
using System.Diagnostics;
using System.Linq;
using System.Reflection;
using System.Reflection.Emit;
using HarmonyLib;
using UnityEngine;

namespace Bd2BrownDustXAccel;

internal static class ModLoadPatches
{
    private static BrownDustXApi api;

    internal static void Install(Harmony harmony, BrownDustXApi bound)
    {
        api = bound;
        ModLoader.Api = bound;
        harmony.Patch(bound.GetMods, postfix: new HarmonyMethod(typeof(ModLoadPatches), nameof(GetModsPostfix)));
        harmony.Patch(bound.CheckModFolders, postfix: new HarmonyMethod(typeof(ModLoadPatches), nameof(CheckModFoldersPostfix)));
        if (!AccelSettings.Enabled)
        {
            return;
        }
        LoadingProgress.Install(harmony, bound.Assembly, bound.AddModFolder);
        harmony.Patch(bound.Cleanup, prefix: new HarmonyMethod(typeof(ModLoadPatches), nameof(CleanupPrefix)));
        PrefixInjector.Inject(harmony, bound.LoadModDataByName, nameof(ModLoader.LoadModDataByName));
        PrefixInjector.Inject(harmony, bound.PrepareModForLoading, nameof(ModLoader.PrepareModForLoading));
        PrefixInjector.Inject(harmony, bound.LoadModDataPreloaded, nameof(ModLoader.LoadModDataPreloaded));
        PrefixInjector.Inject(harmony, bound.PrepareRuntimeAsync, nameof(ModLoader.PrepareRuntimeAsync));
        PrefixInjector.Inject(harmony, bound.BuildModDataFromPrepared, nameof(ModLoader.BuildModDataFromPrepared));
    }

    private static void GetModsPostfix(ref IEnumerator __result) => __result = new ModScan(__result);

    private static void CheckModFoldersPostfix(string folder, int depth, ref IEnumerator __result) => __result = new FolderStep(__result, folder, depth);

    /// <summary>BrownDustX runs GC.Collect twice (plus UnloadUnusedAssets on the splash scene) after every compressed mod; one pass at the end is enough.</summary>
    private static bool CleanupPrefix(bool unloadAssets, ref IEnumerator __result)
    {
        if (unloadAssets || !FolderStep.ScanActive)
        {
            return true;
        }
        __result = Enumerable.Empty<object>().GetEnumerator();
        return false;
    }

    /// <summary>Wraps ModManager.GetMods to time the whole (re)load and to run the startup prefetch for its duration.</summary>
    private sealed class ModScan(IEnumerator inner) : IEnumerator
    {
        private long started;
        private bool begun;
        private bool ended;
        private bool prefetching;
        private bool preloadAll;

        public object Current => inner.Current;

        public bool MoveNext()
        {
            if (!begun)
            {
                begun = true;
                Begin();
            }
            bool more;
            try
            {
                more = inner.MoveNext();
            }
            catch
            {
                End();
                throw;
            }
            if (!more)
            {
                End();
            }
            return more;
        }

        public void Reset() => inner.Reset();

        private void Begin()
        {
            started = Stopwatch.GetTimestamp();
            bool fromWatcher = api.WatcherChangesDetected;
            preloadAll = api.PreloadAllMods;
            LoadStats.Reset();
            FolderStep.ScanTicks = 0;
            if (!AccelSettings.Enabled)
            {
                return;
            }
            LoadingProgress.Begin(api.ModsDirectoryPath, fromWatcher);
            if (fromWatcher)
            {
                SceneCache.Clear();
            }
            else if (preloadAll)
            {
                prefetching = true;
                PreloadSession.Start(api.ModsDirectoryPath);
            }
        }

        private void End()
        {
            if (ended)
            {
                return;
            }
            ended = true;
            long total = Stopwatch.GetTimestamp() - started;
            if (prefetching)
            {
                PreloadSession.Stop();
            }
            BufferPool.Trim();
            string mode = preloadAll ? "preload all" : "on demand";
            string head = FormattableString.Invariant($"Mod scan ({mode}, acceleration {(AccelSettings.Enabled ? "on" : "off")}) finished: folder scan {LoadStats.Milliseconds(FolderStep.ScanTicks)} ms, total {LoadStats.Milliseconds(total)} ms");
            Plugin.Log.LogInfo(AccelSettings.Enabled && preloadAll ? head + "; " + LoadStats.Summary() : head);
        }
    }

    /// <summary>
    /// Wraps each CheckModFolders step. The root step times the folder scan; with acceleration on, every step reports its
    /// folder to the loading dialog and yields a frame once the current frame has spent 100 ms loading, so the dialog keeps rendering.
    /// </summary>
    private sealed class FolderStep(IEnumerator inner, string folder, int depth) : IEnumerator
    {
        private static int frame = -1;
        private static float frameStarted;
        private bool entered;
        private bool yielded;
        private long started;

        internal static bool ScanActive { get; private set; }
        internal static long ScanTicks { get; set; }

        public object Current => yielded ? null : inner.Current;

        public bool MoveNext()
        {
            if (!entered)
            {
                entered = true;
                if (depth == 0)
                {
                    ScanActive = true;
                    started = Stopwatch.GetTimestamp();
                    LoadingProgress.ScanStarted();
                }
                else
                {
                    LoadingProgress.Enter(folder);
                }
                if (AccelSettings.Enabled && FrameBudgetSpent())
                {
                    yielded = true;
                    return true;
                }
            }
            yielded = false;
            bool more;
            try
            {
                more = inner.MoveNext();
            }
            catch
            {
                Finish();
                throw;
            }
            if (!more)
            {
                Finish();
            }
            return more;
        }

        public void Reset() => inner.Reset();

        private void Finish()
        {
            if (depth == 0 && ScanActive)
            {
                ScanActive = false;
                LoadingProgress.ScanFinished();
                ScanTicks += Stopwatch.GetTimestamp() - started;
            }
        }

        private static bool FrameBudgetSpent()
        {
            float now = Time.realtimeSinceStartup;
            if (Time.frameCount != frame)
            {
                frame = Time.frameCount;
                frameStarted = now;
                return false;
            }
            return now - frameStarted > 0.1f;
        }
    }
}

/// <summary>
/// Puts a call to a hook in front of a BrownDustX method: <c>if (Hook(args..., out var r)) return (TReturn)r;</c>.
/// Used instead of a plain Harmony prefix because the return types are BrownDustX classes and UniTask&lt;PreparedRuntime&gt;.
/// </summary>
internal static class PrefixInjector
{
    private static readonly Dictionary<RuntimeMethodHandle, MethodInfo> Hooks = [];
    private static readonly FieldInfo LabelsField = AccessTools.Field(typeof(CodeInstruction), "labels");

    internal static void Inject(Harmony harmony, MethodInfo target, string hookName)
    {
        MethodInfo hook = AccessTools.Method(typeof(ModLoader), hookName) ?? throw new MissingMethodException(nameof(ModLoader), hookName);
        if (hook.GetParameters().Length != target.GetParameters().Length + 1)
        {
            throw new MissingMethodException(target.DeclaringType?.FullName, target.Name);
        }
        Hooks[target.MethodHandle] = hook;
        harmony.Patch(target, transpiler: new HarmonyMethod(typeof(PrefixInjector), nameof(Transpile)));
    }

    private static IEnumerable<CodeInstruction> Transpile(IEnumerable<CodeInstruction> instructions, ILGenerator generator, MethodBase original)
    {
        var method = (MethodInfo)original;
        MethodInfo hook = Hooks[method.MethodHandle];
        LocalBuilder result = generator.DeclareLocal(typeof(object));
        Label resume = generator.DefineLabel();
        int arguments = method.GetParameters().Length + (method.IsStatic ? 0 : 1);
        for (int i = 0; i < arguments; i++)
        {
            yield return i switch
            {
                0 => new CodeInstruction(OpCodes.Ldarg_0),
                1 => new CodeInstruction(OpCodes.Ldarg_1),
                2 => new CodeInstruction(OpCodes.Ldarg_2),
                3 => new CodeInstruction(OpCodes.Ldarg_3),
                _ => new CodeInstruction(OpCodes.Ldarg_S, (byte)i),
            };
        }
        yield return new CodeInstruction(OpCodes.Ldloca_S, result);
        yield return new CodeInstruction(OpCodes.Call, hook);
        yield return new CodeInstruction(OpCodes.Brfalse, resume);
        yield return new CodeInstruction(OpCodes.Ldloc, result);
        yield return new CodeInstruction(OpCodes.Unbox_Any, method.ReturnType);
        yield return new CodeInstruction(OpCodes.Ret);
        var entry = new CodeInstruction(OpCodes.Nop);
        // CodeInstruction.labels is typed against mscorlib's Label, which the netstandard facade cannot name.
        ((IList)LabelsField.GetValue(entry)).Add(resume);
        yield return entry;
        foreach (CodeInstruction instruction in instructions)
        {
            yield return instruction;
        }
    }
}
