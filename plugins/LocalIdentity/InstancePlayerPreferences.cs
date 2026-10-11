using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using Newtonsoft.Json;

namespace Bd2LocalIdentity;

internal static class InstancePlayerPreferences
{
    private static readonly object Gate = new();
    private static string FilePath;
    private static PreferenceValues Values;
    private static bool Dirty;

    internal static void Initialize(string path)
    {
        FilePath = path;
        Values = File.Exists(path)
            ? JsonConvert.DeserializeObject<PreferenceValues>(File.ReadAllText(path, Encoding.UTF8))
            : new PreferenceValues();
        if (Values == null || Values.Version != 1 || Values.Integers == null || Values.Floats == null || Values.Strings == null)
            throw new InvalidDataException("Instance PlayerPrefs file has an unsupported or invalid format");
    }

    public static int GetInt(string key, int defaultValue)
    {
        lock (Gate) return Values.Integers.TryGetValue(key, out int value) ? value : defaultValue;
    }

    public static float GetFloat(string key, float defaultValue)
    {
        lock (Gate) return Values.Floats.TryGetValue(key, out float value) ? value : defaultValue;
    }

    public static string GetString(string key, string defaultValue)
    {
        lock (Gate) return Values.Strings.TryGetValue(key, out string value) ? value : defaultValue;
    }

    public static void SetInt(string key, int value)
    {
        lock (Gate) { DeleteKey(key); Values.Integers[key] = value; Dirty = true; }
    }

    public static void SetFloat(string key, float value)
    {
        lock (Gate) { DeleteKey(key); Values.Floats[key] = value; Dirty = true; }
    }

    public static void SetString(string key, string value)
    {
        lock (Gate) { DeleteKey(key); Values.Strings[key] = value ?? string.Empty; Dirty = true; }
    }

    public static bool HasKey(string key)
    {
        lock (Gate) return Values.Integers.ContainsKey(key) || Values.Floats.ContainsKey(key) || Values.Strings.ContainsKey(key);
    }

    public static void DeleteKey(string key)
    {
        lock (Gate)
        {
            bool changed = Values.Integers.Remove(key);
            changed |= Values.Floats.Remove(key);
            changed |= Values.Strings.Remove(key);
            Dirty |= changed;
        }
    }

    public static void DeleteAll()
    {
        lock (Gate) { Values = new PreferenceValues(); Dirty = true; }
    }

    public static void Save()
    {
        lock (Gate)
        {
            if (!Dirty) return;
            string temporary = FilePath + "." + Guid.NewGuid().ToString("N") + ".tmp";
            try
            {
                byte[] bytes = new UTF8Encoding(false).GetBytes(JsonConvert.SerializeObject(Values));
                using (var stream = new FileStream(temporary, FileMode.CreateNew, FileAccess.Write, FileShare.None,
                    4096, FileOptions.WriteThrough))
                {
                    stream.Write(bytes, 0, bytes.Length);
                    stream.Flush(true);
                }
                if (File.Exists(FilePath)) File.Replace(temporary, FilePath, null);
                else File.Move(temporary, FilePath);
                Dirty = false;
            }
            finally
            {
                if (File.Exists(temporary)) File.Delete(temporary);
            }
        }
    }

    private sealed class PreferenceValues
    {
        public int Version { get; set; } = 1;
        public Dictionary<string, int> Integers { get; set; } = new(StringComparer.Ordinal);
        public Dictionary<string, float> Floats { get; set; } = new(StringComparer.Ordinal);
        public Dictionary<string, string> Strings { get; set; } = new(StringComparer.Ordinal);
    }
}
