using System;
using System.Collections;
using System.Collections.Generic;
using System.Reflection;
using BepInEx.Configuration;
using HarmonyLib;
using Spine.Unity;
using UnityEngine;

namespace Bd2BrownDustXAccel;

/// <summary>Reflection view of the BrownDustX members the loader replaces; binding fails as a whole when any is missing.</summary>
internal sealed class BrownDustXApi
{
    internal const string PluginId = "lynesth.bd2.browndustx";
    internal const string AssemblyName = "lynesth.bd2.browndustx";

    internal Type PreloadModData { get; private set; }
    internal Type PreparedRuntime { get; private set; }
    internal MethodInfo LoadModDataByName { get; private set; }
    internal MethodInfo LoadModDataPreloaded { get; private set; }
    internal MethodInfo PrepareModForLoading { get; private set; }
    internal Assembly Assembly { get; private set; }
    internal MethodInfo AddModFolder { get; private set; }
    internal MethodInfo GetMods { get; private set; }
    internal MethodInfo CheckModFolders { get; private set; }
    internal MethodInfo Cleanup { get; private set; }
    internal MethodInfo PrepareRuntimeAsync { get; private set; }
    internal MethodInfo BuildModDataFromPrepared { get; private set; }

    private Type modData;
    private Func<string, string> shortPath;
    private Func<Texture2D, Texture2D> resizeToMultipleOf4;
    private FieldInfo spineShader;
    private FieldInfo watcherChangesDetected;
    private FieldInfo modsDirectoryPath;
    private FieldInfo preloadAllMods;
    private PropertyInfo shouldCompressTextures;
    private FieldInfo modfileFolder;
    private FieldInfo preloadName;
    private FieldInfo preloadSkeleton;
    private FieldInfo preloadAtlasFile;
    private FieldInfo preloadAtlasTextures;
    private FieldInfo preloadModfile;
    private FieldInfo modDataSkeleton;
    private FieldInfo modDataAtlas;
    private FieldInfo preparedFolder;
    private FieldInfo preparedSkeleton;
    private FieldInfo preparedAtlasText;
    private FieldInfo preparedTexturePaths;

    internal static BrownDustXApi Bind(Assembly assembly)
    {
        Type modManager = RequireType(assembly, "BrownDustX.ModManager");
        Type main = RequireType(assembly, "BrownDustX.BrownDustX");
        Type conf = RequireType(assembly, "BrownDustX.Conf");
        Type utils = RequireType(assembly, "BrownDustX.Utils");
        Type spineReplacer = RequireType(assembly, "BrownDustX.HarmonyPatches.SpineReplacer");
        Type modfile = RequireType(assembly, "BrownDustX.ModManager+Modfile");
        var api = new BrownDustXApi
        {
            PreloadModData = RequireType(assembly, "BrownDustX.ModManager+PreloadModData"),
            PreparedRuntime = RequireType(assembly, "BrownDustX.HarmonyPatches.SpineReplacer+PreparedRuntime"),
            modData = RequireType(assembly, "BrownDustX.ModManager+ModData"),
        };
        Type invalidList = typeof(List<string>);
        api.LoadModDataByName = RequireMethod(modManager, "LoadModData", typeof(string), modfile, invalidList);
        api.LoadModDataPreloaded = RequireMethod(modManager, "LoadModData", api.PreloadModData);
        api.PrepareModForLoading = RequireMethod(modManager, "PrepareModForLoading", typeof(string), modfile, invalidList);
        api.Assembly = assembly;
        api.AddModFolder = RequireMethod(modManager, "AddModFolder", typeof(string), invalidList, invalidList, typeof(bool));
        api.GetMods = RequireMethod(modManager, "GetMods", main);
        api.CheckModFolders = RequireMethod(modManager, "CheckModFolders", typeof(string), typeof(int), invalidList, invalidList);
        api.Cleanup = RequireMethod(main, "Cleanup", typeof(bool));
        api.PrepareRuntimeAsync = RequireMethod(spineReplacer, "PrepareRuntimeAsync", api.PreloadModData);
        api.BuildModDataFromPrepared = RequireMethod(spineReplacer, "BuildModDataFromPrepared", api.PreparedRuntime);
        if (api.LoadModDataByName.ReturnType != api.modData || api.PrepareModForLoading.ReturnType != api.PreloadModData
            || typeof(IEnumerator) != api.GetMods.ReturnType || typeof(IEnumerator) != api.CheckModFolders.ReturnType || typeof(IEnumerator) != api.Cleanup.ReturnType)
        {
            throw new MissingMethodException("BrownDustX mod loader signatures changed");
        }
        api.shortPath = (Func<string, string>)RequireMethod(modManager, "GetShortPath", typeof(string)).CreateDelegate(typeof(Func<string, string>));
        api.resizeToMultipleOf4 = (Func<Texture2D, Texture2D>)RequireMethod(utils, "ResizeToNearestMultipleOf4", typeof(Texture2D)).CreateDelegate(typeof(Func<Texture2D, Texture2D>));
        api.spineShader = RequireField(modManager, "SpineShader");
        api.watcherChangesDetected = RequireField(main, "WatcherChangesDetected");
        api.modsDirectoryPath = RequireField(main, "ModsDirectoryPath");
        api.preloadAllMods = RequireField(conf, "PreloadAllMods");
        api.shouldCompressTextures = AccessTools.Property(conf, "ShouldCompressTextures") ?? throw new MissingMemberException("BrownDustX.Conf", "ShouldCompressTextures");
        PropertyInfo useBytesPngs = AccessTools.Property(conf, "UseBytesPNGs") ?? throw new MissingMemberException("BrownDustX.Conf", "UseBytesPNGs");
        if ((bool)useBytesPngs.GetValue(null))
        {
            throw new NotSupportedException("BrownDustX LZ4 texture mode is not supported");
        }
        api.modfileFolder = RequireField(modfile, "folder");
        api.preloadName = RequireField(api.PreloadModData, "name");
        api.preloadSkeleton = RequireField(api.PreloadModData, "skeletonDataAsset");
        api.preloadAtlasFile = RequireField(api.PreloadModData, "atlasFile");
        api.preloadAtlasTextures = RequireField(api.PreloadModData, "atlasTexturesFiles");
        api.preloadModfile = RequireField(api.PreloadModData, "modfile");
        api.modDataSkeleton = RequireField(api.modData, "skeletonDataAsset");
        api.modDataAtlas = RequireField(api.modData, "spineAtlasAsset");
        api.preparedFolder = RequireField(api.PreparedRuntime, "folder");
        api.preparedSkeleton = RequireField(api.PreparedRuntime, "skeletonDataAsset");
        api.preparedAtlasText = RequireField(api.PreparedRuntime, "atlasText");
        api.preparedTexturePaths = RequireField(api.PreparedRuntime, "texturePaths");
        return api;
    }

    internal string ShortPath(string folder) => shortPath(folder);
    internal Texture2D ResizeToMultipleOf4(Texture2D texture) => resizeToMultipleOf4(texture);
    internal Shader SpineShader => (Shader)spineShader.GetValue(null);
    internal bool WatcherChangesDetected => (bool)watcherChangesDetected.GetValue(null);
    internal string ModsDirectoryPath => (string)modsDirectoryPath.GetValue(null);
    internal bool PreloadAllMods => ((ConfigEntry<string>)preloadAllMods.GetValue(null)).Value == "Yes";
    internal bool ShouldCompressTextures => (bool)shouldCompressTextures.GetValue(null);

    internal string ModfileFolder(object modfile) => (string)modfileFolder.GetValue(modfile);

    internal string PreloadName(object preload) => (string)preloadName.GetValue(preload);
    internal object PreloadModfile(object preload) => preloadModfile.GetValue(preload);
    internal TextAsset PreloadSkeleton(object preload) => (TextAsset)preloadSkeleton.GetValue(preload);
    internal void SetPreloadSkeleton(object preload, TextAsset skeleton) => preloadSkeleton.SetValue(preload, skeleton);
    internal string PreloadAtlasFile(object preload) => (string)preloadAtlasFile.GetValue(preload);
    internal string[] PreloadAtlasTextures(object preload) => (string[])preloadAtlasTextures.GetValue(preload);

    internal object NewPreload(string name, object modfile, string atlasFile, string[] atlasTextures)
    {
        object preload = Activator.CreateInstance(PreloadModData);
        preloadAtlasFile.SetValue(preload, atlasFile);
        preloadAtlasTextures.SetValue(preload, atlasTextures);
        preloadName.SetValue(preload, name);
        preloadModfile.SetValue(preload, modfile);
        return preload;
    }

    internal object NewModData(TextAsset skeleton, SpineAtlasAsset atlas)
    {
        object data = Activator.CreateInstance(modData);
        modDataSkeleton.SetValue(data, skeleton);
        modDataAtlas.SetValue(data, atlas);
        return data;
    }

    internal void FillPrepared(object prepared, string folder, TextAsset skeleton, string atlasText, string[] texturePaths)
    {
        preparedFolder.SetValue(prepared, folder);
        preparedSkeleton.SetValue(prepared, skeleton);
        preparedAtlasText.SetValue(prepared, atlasText);
        preparedTexturePaths.SetValue(prepared, texturePaths);
    }

    private static Type RequireType(Assembly assembly, string name) =>
        assembly.GetType(name, false) ?? throw new TypeLoadException("BrownDustX type missing: " + name);

    private static MethodInfo RequireMethod(Type type, string name, params Type[] parameters) =>
        AccessTools.Method(type, name, parameters) ?? throw new MissingMethodException(type.FullName, name);

    private static FieldInfo RequireField(Type type, string name) =>
        AccessTools.Field(type, name) ?? throw new MissingFieldException(type.FullName, name);
}
