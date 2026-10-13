using System;
using System.Globalization;
using System.IO;
using System.IO.Compression;
using System.Security.Cryptography;
using BepInEx.Logging;
using UnityEngine;

namespace Bd2LocalIdentity;

internal static class UnityLogArchive
{
    internal static void ArchivePrevious(ManualLogSource log)
    {
        string current = Application.consoleLogPath;
        if (string.IsNullOrEmpty(current) || !Path.IsPathFullyQualified(current)) return;
        string directory = Path.GetDirectoryName(Path.GetFullPath(current));
        string previous = Path.Combine(directory, "Player-prev.log");
        Archive(previous, log);
        foreach (string pending in Directory.EnumerateFiles(directory, "bd2-previous.*.Player-prev.log", SearchOption.TopDirectoryOnly))
            Archive(pending, log);
    }

    private static void Archive(string previous, ManualLogSource log)
    {
        if (!File.Exists(previous)) return;
        string archive = null;
        bool created = false, verified = false;
        try
        {
            using (var source = new FileStream(previous, FileMode.Open, FileAccess.Read, FileShare.None))
            {
                DateTime timestamp = File.GetLastWriteTime(previous);
                string stem = timestamp.ToString("yyyy-MM-dd_HH-mm-ss.fff", CultureInfo.InvariantCulture);
                string directory = Path.GetDirectoryName(previous);
                string entry;
                FileStream destination;
                for (int suffix = 0; ; suffix++)
                {
                    string name = stem + (suffix == 0 ? string.Empty : "_" + suffix.ToString(CultureInfo.InvariantCulture));
                    archive = Path.Combine(directory, name + ".Player.log.zip");
                    entry = name + ".Player.log";
                    try { destination = new FileStream(archive, FileMode.CreateNew, FileAccess.ReadWrite, FileShare.None); break; }
                    catch (IOException error) when (File.Exists(archive) &&
                        error.HResult is unchecked((int)0x80070050) or unchecked((int)0x800700B7))
                    { }
                }
                created = true;
                using (destination)
                using (var zip = new ZipArchive(destination, ZipArchiveMode.Create))
                {
                    ZipArchiveEntry item = zip.CreateEntry(entry, System.IO.Compression.CompressionLevel.Optimal);
                    if (timestamp.Year is >= 1980 and <= 2107) item.LastWriteTime = timestamp;
                    using Stream compressed = item.Open();
                    source.CopyTo(compressed);
                }
                source.Position = 0;
                using var sha = SHA256.Create();
                byte[] original = sha.ComputeHash(source);
                using var saved = new FileStream(archive, FileMode.Open, FileAccess.Read, FileShare.Read);
                using var verify = new ZipArchive(saved, ZipArchiveMode.Read);
                if (verify.Entries.Count != 1 || verify.Entries[0].FullName != entry || verify.Entries[0].Length != source.Length)
                    throw new InvalidDataException("Unity log archive entry did not match the previous log");
                using Stream restored = verify.Entries[0].Open();
                byte[] copied = sha.ComputeHash(restored);
                if (!Equal(original, copied)) throw new InvalidDataException("Unity log archive checksum did not match the previous log");
                verified = true;
            }
            File.Delete(previous);
            log.LogInfo("Archived previous Unity log: " + archive);
        }
        catch (Exception error)
        {
            log.LogWarning("Previous Unity log retained: " + Path.GetFileName(previous) + " exception=" + error.GetType().Name);
        }
        finally
        {
            if (created && !verified)
            {
                try { File.Delete(archive); }
                catch (Exception error) when (error is IOException or UnauthorizedAccessException)
                { log.LogWarning("Incomplete Unity log archive cleanup failed: " + error.GetType().Name); }
            }
        }
    }

    private static bool Equal(byte[] first, byte[] second)
    {
        if (first.Length != second.Length) return false;
        for (int index = 0; index < first.Length; index++) if (first[index] != second[index]) return false;
        return true;
    }
}
