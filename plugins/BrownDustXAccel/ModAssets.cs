using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using Spine.Unity;
using Unity.Collections;
using UnityEngine;
using UnityObject = UnityEngine.Object;

namespace Bd2BrownDustXAccel;

/// <summary>Error text shown in BrownDustX's "Mods Errors" dialog, kept identical to the original messages.</summary>
internal sealed class ModLoadException : Exception
{
    public ModLoadException()
    {
    }

    public ModLoadException(string message) : base(message)
    {
    }

    public ModLoadException(string message, Exception innerException) : base(message, innerException)
    {
    }

    internal static ModLoadException MissingTextures(BrownDustXApi api, string folder) =>
        new(api.ShortPath(folder) + " <color=#FFFF4C>(Missing or invalid textures)</color>");
}

/// <summary>Only Spine character assets are accelerated; intro and other mod types keep BrownDustX's own loader.</summary>
internal static class ModNames
{
    private static readonly string[] CharacterPrefixes = ["char", "npc", "cutscene", "illust", "specialillust", "storypack"];

    internal static bool IsCharacter(string name)
    {
        if (string.IsNullOrEmpty(name))
        {
            return false;
        }
        foreach (string prefix in CharacterPrefixes)
        {
            if (name.StartsWith(prefix, StringComparison.OrdinalIgnoreCase))
            {
                return true;
            }
        }
        return false;
    }
}

internal static class AtlasPages
{
    /// <summary>Page names exactly as BrownDustX reads them: every line but the last that ends in ".png".</summary>
    internal static List<string> Parse(string atlasText)
    {
        string[] lines = atlasText.Replace("\r", "", StringComparison.Ordinal).Split('\n');
        var pages = new List<string>();
        for (int i = 0; i < lines.Length - 1; i++)
        {
            string line = lines[i].Trim();
            if (line.EndsWith(".png", StringComparison.Ordinal))
            {
                pages.Add(line.Replace(".png", "", StringComparison.Ordinal));
            }
        }
        return pages;
    }
}

internal static class ModAssets
{
    /// <summary>Binary skeleton TextAsset built the same way as BrownDustX's CreateBytesTextAssetFromFile.</summary>
    internal static TextAsset SkeletonFromBytes(byte[] bytes, string name)
    {
        var asset = new TextAsset(new string(' ', bytes.Length)) { name = name };
        NativeArray<byte> data = asset.GetData<byte>();
        if (!data.IsCreated || data.Length != bytes.Length)
        {
            throw new ModLoadException(FormattableString.Invariant($"Text Creation Error: {data.Length} vs {bytes.Length}"));
        }
        NativeArray<byte>.Copy(bytes, data);
        return asset;
    }

    /// <summary>Uploads decoded pages and builds the runtime atlas on the main thread; every page result is released.</summary>
    internal static SpineAtlasAsset BuildAtlas(BrownDustXApi api, string folder, string atlasText, string[] textureNames, DecodeJob[] jobs,
        string[] paths, bool mipmaps, bool compress, List<Texture2D> created)
    {
        var materials = new Material[jobs.Length];
        try
        {
            foreach (DecodeJob job in jobs)
            {
                if (job.TryClaim())
                {
                    job.Run();
                }
            }
            for (int i = 0; i < jobs.Length; i++)
            {
                long waitStarted = Stopwatch.GetTimestamp();
                PageResult page;
                try
                {
                    page = (PageResult)jobs[i].Wait() ?? (PageResult)new DecodeJob(PageResult.Work(paths[i], mipmaps, null, 0)).Wait();
                }
                catch (Exception error) when (error is IOException or UnauthorizedAccessException)
                {
                    throw ModLoadException.MissingTextures(api, folder);
                }
                long uploadStarted = Stopwatch.GetTimestamp();
                LoadStats.WaitTicks += uploadStarted - waitStarted;
                try
                {
                    if (page.Missing)
                    {
                        throw ModLoadException.MissingTextures(api, folder);
                    }
                    Texture2D texture = CreateTexture(api, folder, page, mipmaps, compress);
                    texture.name = textureNames[i];
                    created.Add(texture);
                    materials[i] = new Material(api.SpineShader) { mainTexture = texture };
                }
                finally
                {
                    page.Release();
                    LoadStats.UploadTicks += Stopwatch.GetTimestamp() - uploadStarted;
                }
            }
        }
        finally
        {
            foreach (DecodeJob job in jobs)
            {
                job.Cancel();
            }
        }
        return SpineAtlasAsset.CreateRuntimeInstance(new TextAsset(atlasText), materials, false, null);
    }

    private static Texture2D CreateTexture(BrownDustXApi api, string folder, PageResult page, bool mipmaps, bool compress)
    {
        Texture2D texture;
        try
        {
            if (page.Image != null)
            {
                DecodedImage image = page.Image;
                texture = new Texture2D(image.Width, image.Height, TextureFormat.RGBA32, mipmaps);
                if (texture.mipmapCount == image.MipCount)
                {
                    for (int level = 0; level < image.MipCount; level++)
                    {
                        texture.SetPixelData(image.Pixels, level, image.MipOffset(level));
                    }
                    texture.Apply(false, !compress);
                }
                else
                {
                    texture.SetPixelData(image.Pixels, 0, 0);
                    texture.Apply(true, !compress);
                }
            }
            else
            {
                texture = new Texture2D(1, 1, TextureFormat.RGBA32, mipmaps);
                texture.LoadImage(page.RawPng, !compress);
            }
            if (compress)
            {
                Texture2D resized = api.ResizeToMultipleOf4(texture);
                if (resized != texture)
                {
                    UnityObject.Destroy(texture);
                    texture = resized;
                }
                texture.Compress(true);
                texture.Apply(false, true);
            }
        }
        catch (Exception error) when (error is not ModLoadException)
        {
            throw ModLoadException.MissingTextures(api, folder);
        }
        return texture;
    }

    internal static long TextureBytes(Texture2D texture) =>
        (long)texture.width * texture.height * 4 * (texture.mipmapCount > 1 ? 4 : 3) / 3;
}
