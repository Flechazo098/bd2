using System;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Threading;
using HarmonyLib;
using UnityEngine;

namespace Bd2BrownDustXAccel;

/// <summary>
/// Draws a progress bar and the mod folder being loaded under BrownDustX's "Loading mods" dialog. The total is counted
/// on a background thread with the same folder rules as CheckModFolders; reloads from the file watcher only process
/// changed folders, so they show the running count without a total.
/// </summary>
internal static class LoadingProgress
{
    private static FieldInfo currentModal;
    private static FieldInfo windowRect;
    private static string root;
    private static object modal;
    private static volatile int total = -1;
    private static int loaded;
    private static string current = "";
    private static GUIStyle labelStyle;

    internal static void Install(Harmony harmony, Assembly assembly, MethodInfo addModFolder)
    {
        Type modalType = assembly.GetType("BrownDustX.Tools.Modal", false);
        MethodInfo windowFunc = modalType == null ? null : AccessTools.Method(modalType, "WindowFunc", [typeof(int)]);
        currentModal = modalType == null ? null : AccessTools.Field(modalType, "CurrentModal");
        windowRect = modalType == null ? null : AccessTools.Field(modalType, "windowRect");
        if (windowFunc == null || currentModal == null || windowRect == null || windowRect.FieldType != typeof(Rect))
        {
            Plugin.Log.LogWarning("BrownDustX loading dialog not found; progress bar disabled");
            return;
        }
        harmony.Patch(addModFolder, prefix: new HarmonyMethod(typeof(LoadingProgress), nameof(AddModFolderPrefix)));
        harmony.Patch(windowFunc, postfix: new HarmonyMethod(typeof(LoadingProgress), nameof(WindowFuncPostfix)));
    }

    /// <summary>Called when GetMods starts, before BrownDustX's one-second wait, so the count is usually ready in time.</summary>
    internal static void Begin(string modsRoot, bool fromWatcher)
    {
        root = modsRoot;
        loaded = 0;
        current = "";
        total = -1;
        if (fromWatcher || currentModal == null)
        {
            return;
        }
        new Thread(() =>
        {
            try
            {
                total = CountMods(modsRoot, 0);
            }
            catch (Exception error) when (error is IOException or UnauthorizedAccessException)
            {
            }
        })
        { IsBackground = true, Name = "BrownDustXAccel count", Priority = System.Threading.ThreadPriority.BelowNormal }.Start();
    }

    /// <summary>The root CheckModFolders step starts right after the dialog is shown; that dialog is the one to decorate.</summary>
    internal static void ScanStarted() => modal = currentModal?.GetValue(null);

    internal static void ScanFinished() => modal = null;

    internal static void Enter(string folder)
    {
        if (modal == null || root == null || folder == null || !folder.StartsWith(root, StringComparison.OrdinalIgnoreCase))
        {
            return;
        }
        current = folder[root.Length..].TrimStart('\\', '/').Replace('\\', '/');
    }

    private static void AddModFolderPrefix(bool allowIntro)
    {
        if (modal != null && !allowIntro)
        {
            loaded++;
        }
    }

    private static void WindowFuncPostfix(object __instance)
    {
        if (modal == null || !ReferenceEquals(__instance, modal))
        {
            return;
        }
        var window = (Rect)windowRect.GetValue(__instance);
        labelStyle ??= new GUIStyle(GUI.skin.label) { fontSize = 16, alignment = TextAnchor.MiddleCenter, clipping = TextClipping.Clip, wordWrap = false };
        labelStyle.normal.textColor = new Color(0.85f, 0.85f, 0.85f, 1f);
        int count = total;
        float width = window.width - 50f;
        var bar = new Rect(25f, window.height - 62f, width, 22f);
        string label = count > 0
            ? FormattableString.Invariant($"{Math.Min(loaded, count)} / {count}  ({Math.Min(loaded, count) * 100 / count}%)")
            : loaded.ToString(System.Globalization.CultureInfo.InvariantCulture);
        GUI.Label(new Rect(25f, window.height - 98f, width, 30f), current, labelStyle);
        Color tint = GUI.color;
        GUI.color = new Color(1f, 1f, 1f, 0.15f);
        GUI.DrawTexture(bar, Texture2D.whiteTexture);
        GUI.color = new Color(0.35f, 0.65f, 1f, 1f);
        float fraction = count > 0 ? Mathf.Clamp01((float)loaded / count) : (Time.realtimeSinceStartup * 0.5f) % 1f;
        GUI.DrawTexture(new Rect(bar.x, bar.y, bar.width * fraction, bar.height), Texture2D.whiteTexture);
        GUI.color = tint;
        GUI.Label(bar, label, labelStyle);
    }

    /// <summary>Same rules as ModManager.CheckModFolders: below the root, a folder with exactly one non-intro .modfile is a mod.</summary>
    private static int CountMods(string folder, int depth)
    {
        if (depth > 5)
        {
            return 0;
        }
        if (depth != 0 && Directory.EnumerateFiles(folder, "*.modfile")
            .Count(f => !string.Equals(Path.GetFileName(f), "intro.modfile", StringComparison.OrdinalIgnoreCase)) == 1)
        {
            return 1;
        }
        return Directory.EnumerateDirectories(folder).Sum(child => CountMods(child, depth + 1));
    }
}
