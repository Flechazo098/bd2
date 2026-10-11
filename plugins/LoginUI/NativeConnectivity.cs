using System;
using System.Threading;
using System.Threading.Tasks;
using System.Reflection;
using BD2.GameNames;
using Cysharp.Threading.Tasks;
using HarmonyLib;

namespace Bd2LoginUI;

internal static class NativeConnectivity
{
    private static readonly object Sync = new();
    private static Task<bool> pending;
    private static string pendingOrigin;
    private static DateTime started;
    private static int consecutiveFailures;
    private static DateTime lastSuccess;

    internal static void Install(Harmony harmony)
    {
        harmony.Patch(Game.Method<NetworkConnectivityMonitor>(monitor => monitor.IsHostAvailable(null, 0, 0)),
            prefix: new HarmonyMethod(typeof(NativeConnectivity), nameof(ProbePrefix)));
        MethodInfo batch = typeof(NetworkConnectivityMonitor).GetGameMethod("CheckEndpointsAvailability",
            BindingFlags.Instance | BindingFlags.NonPublic, null, [typeof(UniTask<bool>[]), typeof(int)], null)
            ?? throw new MissingMethodException("Native connectivity batch method was not found");
        harmony.Patch(batch, prefix: new HarmonyMethod(typeof(NativeConnectivity), nameof(ProbePrefix)));
    }

    private static bool ProbePrefix(ref UniTask<bool> __result)
    {
        Uri server = ClientSession.ServerOrigin;
        if (server == null || !PlatformControlHttp.IsSupported) return true;
        lock (Sync)
        {
            string origin = server.GetLeftPart(UriPartial.Authority);
            if (pendingOrigin != origin) { consecutiveFailures = 0; lastSuccess = DateTime.MinValue; }
            if (pending == null || pendingOrigin != origin || pending.IsCompleted && DateTime.UtcNow - started > TimeSpan.FromSeconds(1))
            {
                pendingOrigin = origin;
                started = DateTime.UtcNow;
                pending = Probe(server);
            }
            __result = pending.AsUniTask(false);
        }
        return false;
    }

    private static async Task<bool> Probe(Uri server)
    {
        PlatformControlHttp.Response response = null;
        try
        {
            response = await PlatformControlHttp.Send(new Uri(server, "/readyz"), "GET", null, null, 3,
                CancellationToken.None, CancellationToken.None, 4096).ConfigureAwait(false);
            return RecordProbe(response.Error == null && (response.StatusCode == 200 || response.StatusCode == 503));
        }
        catch (Exception) { return RecordProbe(false); }
        finally
        {
            if (response?.Data != null) Array.Clear(response.Data, 0, response.Data.Length);
            if (response != null) response.Body = null;
        }
    }

    private static bool RecordProbe(bool available)
    {
        lock (Sync)
        {
            if (available) { consecutiveFailures = 0; lastSuccess = DateTime.UtcNow; return true; }
            consecutiveFailures++;
            return consecutiveFailures < 3 && DateTime.UtcNow - lastSuccess < TimeSpan.FromSeconds(20);
        }
    }
}
