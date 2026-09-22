using System;
using System.IO;
using System.Text;
using BepInEx;

namespace Bd2LocalIdentity;

// The server's UpdateAgeGate response is intentionally empty.  Once that
// response succeeds, the only client-side fact needed on the next login is
// that this local installation has completed the age-gate flow.  Do not keep
// the date of birth: it is not consumed by the client after the request and
// retaining it would add unnecessary personal data to a local plugin file.
internal static class AgeGateState
{
    private const string FileName = "bd2.localidentity.age-gate.state";
    private const string Content = "format=1\nconfirmed=true\n";
    private static readonly object Gate = new object();
    private static bool? confirmed;

    internal static bool IsConfirmed()
    {
        lock (Gate)
        {
            if (confirmed.HasValue)
            {
                return confirmed.Value;
            }

            try
            {
                string path = StatePath();
                confirmed = File.Exists(path) &&
                    string.Equals(File.ReadAllText(path, Encoding.UTF8), Content, StringComparison.Ordinal);
            }
            catch (Exception ex) when (ex is IOException || ex is UnauthorizedAccessException)
            {
                confirmed = false;
                Plugin.LogWarning("Could not read local age-gate state: " + ex.Message);
            }

            return confirmed.Value;
        }
    }

    internal static void MarkConfirmed()
    {
        lock (Gate)
        {
            if (confirmed == true)
            {
                return;
            }

            string path = StatePath();
            string directory = Path.GetDirectoryName(path);
            if (string.IsNullOrEmpty(directory))
            {
                throw new IOException("BepInEx configuration directory is unavailable.");
            }

            Directory.CreateDirectory(directory);
            string temporaryPath = path + "." + Guid.NewGuid().ToString("N") + ".tmp";
            try
            {
                WriteThrough(temporaryPath, Content);
                if (File.Exists(path))
                {
                    // Both files are in BepInEx/config, so replacement is an
                    // atomic same-volume operation on the supported Windows client.
                    File.Replace(temporaryPath, path, null);
                }
                else
                {
                    File.Move(temporaryPath, path);
                }

                confirmed = true;
            }
            finally
            {
                if (File.Exists(temporaryPath))
                {
                    File.Delete(temporaryPath);
                }
            }
        }
    }

    private static string StatePath()
    {
        return Path.Combine(Paths.ConfigPath, FileName);
    }

    private static void WriteThrough(string path, string content)
    {
        byte[] bytes = new UTF8Encoding(false).GetBytes(content);
        using FileStream stream = new FileStream(
            path,
            FileMode.CreateNew,
            FileAccess.Write,
            FileShare.None,
            4096,
            FileOptions.WriteThrough);
        stream.Write(bytes, 0, bytes.Length);
        stream.Flush(true);
    }
}
