using System;
using System.Diagnostics;
using System.Threading;

namespace Bd2BrownDustXAccel;

/// <summary>Counters for the startup preload summary; decode figures are written from worker threads.</summary>
internal static class LoadStats
{
    private static long decodeTicks;
    private static long decodedBytes;
    private static int decodedPages;
    private static int fallbackPages;

    internal static int Mods { get; set; }
    internal static int PrefetchedPages { get; set; }
    internal static long UploadTicks { get; set; }
    internal static long WaitTicks { get; set; }

    internal static void AddDecode(long ticks, long bytes, bool fallback)
    {
        Interlocked.Add(ref decodeTicks, ticks);
        Interlocked.Add(ref decodedBytes, bytes);
        Interlocked.Increment(ref fallback ? ref fallbackPages : ref decodedPages);
    }

    internal static void Reset()
    {
        Interlocked.Exchange(ref decodeTicks, 0);
        Interlocked.Exchange(ref decodedBytes, 0);
        Interlocked.Exchange(ref decodedPages, 0);
        Interlocked.Exchange(ref fallbackPages, 0);
        Mods = 0;
        PrefetchedPages = 0;
        UploadTicks = 0;
        WaitTicks = 0;
    }

    internal static string Summary() => FormattableString.Invariant(
        $"{Mods} character mods, {Volatile.Read(ref decodedPages) + Volatile.Read(ref fallbackPages)} pages ({Volatile.Read(ref decodedBytes) / (1024 * 1024)} MB pixels, {PrefetchedPages} prefetched, {Volatile.Read(ref fallbackPages)} Unity-decoded); main thread upload {Milliseconds(UploadTicks)} ms, waiting {Milliseconds(WaitTicks)} ms; decode CPU {Milliseconds(Volatile.Read(ref decodeTicks))} ms on {DecodeScheduler.WorkerCount} threads");

    internal static long Milliseconds(long ticks) => ticks * 1000 / Stopwatch.Frequency;
}
