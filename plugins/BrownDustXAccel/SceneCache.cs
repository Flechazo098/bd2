using System.Collections.Generic;
using System.Runtime.CompilerServices;
using UnityEngine;

namespace Bd2BrownDustXAccel;

/// <summary>
/// LRU of on-demand character mods. BrownDustX drops its loaded mods on every scene change; keeping recent ones
/// alive (up to a texture budget) turns a re-decode into a dictionary hit. Main thread only.
/// </summary>
internal static class SceneCache
{
    private static readonly Dictionary<object, LinkedListNode<Entry>> Entries = new(ReferenceComparer.Instance);
    private static readonly LinkedList<Entry> Recent = new();
    private static long total;

    internal static object TryGet(object preload)
    {
        if (!Entries.TryGetValue(preload, out LinkedListNode<Entry> node))
        {
            return null;
        }
        foreach (Texture2D texture in node.Value.Textures)
        {
            if (texture == null)
            {
                Remove(node);
                return null;
            }
        }
        Recent.Remove(node);
        Recent.AddFirst(node);
        return node.Value.ModData;
    }

    internal static void Add(object preload, object modData, List<Texture2D> textures)
    {
        long bytes = 0;
        foreach (Texture2D texture in textures)
        {
            bytes += ModAssets.TextureBytes(texture);
        }
        if (bytes > AccelSettings.SceneCacheBytes)
        {
            return;
        }
        if (Entries.TryGetValue(preload, out LinkedListNode<Entry> existing))
        {
            Remove(existing);
        }
        LinkedListNode<Entry> node = Recent.AddFirst(new Entry(preload, modData, [.. textures], bytes));
        Entries[preload] = node;
        total += bytes;
        while (total > AccelSettings.SceneCacheBytes && Recent.Last != null)
        {
            Remove(Recent.Last);
        }
    }

    internal static void Clear()
    {
        Entries.Clear();
        Recent.Clear();
        total = 0;
    }

    private static void Remove(LinkedListNode<Entry> node)
    {
        Entries.Remove(node.Value.Preload);
        Recent.Remove(node);
        total -= node.Value.Bytes;
    }

    private sealed class Entry(object preload, object modData, Texture2D[] textures, long bytes)
    {
        internal object Preload { get; } = preload;
        internal object ModData { get; } = modData;
        internal Texture2D[] Textures { get; } = textures;
        internal long Bytes { get; } = bytes;
    }

    private sealed class ReferenceComparer : IEqualityComparer<object>
    {
        internal static readonly ReferenceComparer Instance = new();

        public new bool Equals(object x, object y) => ReferenceEquals(x, y);

        public int GetHashCode(object obj) => RuntimeHelpers.GetHashCode(obj);
    }
}
