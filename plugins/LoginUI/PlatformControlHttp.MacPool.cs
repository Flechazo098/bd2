using System;
using System.Collections.Generic;
using System.Threading;

namespace Bd2LoginUI;

internal static partial class PlatformControlHttp
{
    private static readonly Dictionary<string, CurlPool> CurlPools = new Dictionary<string, CurlPool>();
    private sealed class CurlPool
    {
        internal readonly Stack<IntPtr> Idle = new Stack<IntPtr>();
        internal int Users;
        internal DateTime LastUsed;
        internal bool Retired;
    }
    private static IntPtr AcquireCurl(Uri uri, NativeProxySettings proxy, CancellationToken token, out CurlPool pool)
    {
        pool = null;
        token.ThrowIfCancellationRequested();
        string key = uri.GetLeftPart(UriPartial.Authority) + "|" + proxy.Key;
        lock (PoolLock)
        {
            if (shuttingDown) throw new OperationCanceledException();
            List<string> stale = new List<string>();
            foreach (KeyValuePair<string, CurlPool> entry in CurlPools)
                if (entry.Value.Users == 0 && (DateTime.UtcNow - entry.Value.LastUsed > TimeSpan.FromMinutes(2) || CurlPools.Count >= 16)) stale.Add(entry.Key);
            foreach (string old in stale)
            {
                CurlPool retired = CurlPools[old]; retired.Retired = true;
                while (retired.Idle.Count != 0) curl_easy_cleanup(retired.Idle.Pop());
                CurlPools.Remove(old);
            }
            if (!CurlPools.TryGetValue(key, out pool)) { pool = new CurlPool(); CurlPools.Add(key, pool); }
            IntPtr handle = pool.Idle.Count != 0 ? pool.Idle.Pop() : curl_easy_init();
            if (handle == IntPtr.Zero) throw new InvalidOperationException("Could not create system curl request");
            pool.Users++;
            return handle;
        }
    }
    private static void ReleaseCurl(CurlPool pool, IntPtr handle, bool reusable)
    {
        lock (PoolLock)
        {
            pool.Users--;
            pool.LastUsed = DateTime.UtcNow;
            if (shuttingDown || pool.Retired || !reusable || pool.Idle.Count >= 2) curl_easy_cleanup(handle);
            else pool.Idle.Push(handle);
        }
    }
}
