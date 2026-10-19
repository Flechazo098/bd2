using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Runtime.ExceptionServices;
using System.Threading;

namespace Bd2BrownDustXAccel;

/// <summary>Background threads that run file reads and PNG decodes; on-demand loads jump ahead of startup prefetch.</summary>
internal static class DecodeScheduler
{
    private static readonly object Gate = new();
    private static readonly Queue<DecodeJob> Urgent = new();
    private static readonly Queue<DecodeJob> Background = new();
    private static bool started;

    internal static int WorkerCount { get; } = AccelSettings.DecodeThreads > 0
        ? Math.Min(AccelSettings.DecodeThreads, 32)
        : Math.Max(1, Math.Min(8, Environment.ProcessorCount - 1));

    internal static void Enqueue(DecodeJob job, bool urgent)
    {
        lock (Gate)
        {
            if (!started)
            {
                started = true;
                for (int i = 0; i < WorkerCount; i++)
                {
                    new Thread(Work) { IsBackground = true, Name = "BrownDustXAccel decode " + i, Priority = ThreadPriority.BelowNormal }.Start();
                }
            }
            (urgent ? Urgent : Background).Enqueue(job);
            Monitor.Pulse(Gate);
        }
    }

    private static void Work()
    {
        while (true)
        {
            DecodeJob job;
            lock (Gate)
            {
                while (Urgent.Count == 0 && Background.Count == 0)
                {
                    Monitor.Wait(Gate);
                }
                job = Urgent.Count > 0 ? Urgent.Dequeue() : Background.Dequeue();
            }
            if (job.TryClaim())
            {
                job.Run();
            }
        }
    }
}

/// <summary>One unit of background work whose result is owned by whoever calls <see cref="Wait"/>; abandoned results are released.</summary>
internal sealed class DecodeJob(Func<DecodeJob, IReleasable> work)
{
    private const int Pending = 0, Running = 1, Finished = 2, Cancelled = 3;
    private readonly object sync = new();
    private int state;
    private int released;
    private volatile bool cancelRequested;
    private IReleasable result;
    private Exception error;

    internal bool CancelRequested => cancelRequested;

    internal bool TryClaim() => Interlocked.CompareExchange(ref state, Running, Pending) == Pending;

    internal void Run()
    {
        try
        {
            result = work(this);
        }
        catch (Exception failure)
        {
            error = failure;
        }
        lock (sync)
        {
            state = Finished;
            Monitor.PulseAll(sync);
        }
        if (cancelRequested)
        {
            ReleaseResult();
        }
    }

    /// <summary>Runs the job inline when no worker has started it, otherwise waits for it to finish.</summary>
    internal void Complete()
    {
        if (TryClaim())
        {
            Run();
        }
        lock (sync)
        {
            while (state is Pending or Running)
            {
                Monitor.Wait(sync);
            }
        }
    }

    /// <summary>Completes the job; the caller owns the returned result.</summary>
    internal IReleasable Wait()
    {
        Complete();
        if (state == Cancelled)
        {
            return null;
        }
        if (error != null)
        {
            ExceptionDispatchInfo.Capture(error).Throw();
        }
        return Interlocked.Exchange(ref released, 1) == 0 ? result : null;
    }

    internal void Cancel()
    {
        cancelRequested = true;
        if (Interlocked.CompareExchange(ref state, Cancelled, Pending) == Pending)
        {
            lock (sync)
            {
                Monitor.PulseAll(sync);
            }
            return;
        }
        if (Volatile.Read(ref state) == Finished)
        {
            ReleaseResult();
        }
    }

    private void ReleaseResult()
    {
        if (Interlocked.Exchange(ref released, 1) == 0)
        {
            result?.Release();
        }
    }
}

internal interface IReleasable
{
    void Release();
}

/// <summary>Caps decoded-but-unconsumed prefetch memory; the mod being consumed always proceeds so the loader cannot stall.</summary>
internal sealed class PrefetchBudget(long limit)
{
    private readonly object gate = new();
    private long used;
    private int demanded = -1;
    private bool closed;

    internal bool Acquire(long bytes, int sequence, DecodeJob job)
    {
        lock (gate)
        {
            while (!closed && !job.CancelRequested && used > 0 && used + bytes > limit && sequence > demanded)
            {
                Monitor.Wait(gate, 200);
            }
            if (closed || job.CancelRequested)
            {
                return false;
            }
            used += bytes;
            return true;
        }
    }

    internal void Release(long bytes)
    {
        lock (gate)
        {
            used -= bytes;
            Monitor.PulseAll(gate);
        }
    }

    internal void Demand(int sequence)
    {
        lock (gate)
        {
            demanded = Math.Max(demanded, sequence);
            Monitor.PulseAll(gate);
        }
    }

    internal void Close()
    {
        lock (gate)
        {
            closed = true;
            Monitor.PulseAll(gate);
        }
    }
}

/// <summary>A decoded atlas page, or the raw PNG when only Unity can decode it.</summary>
internal sealed class PageResult : IReleasable
{
    private PrefetchBudget budget;
    private long charged;

    internal string Path { get; private set; }
    internal bool Missing { get; private set; }
    internal DecodedImage Image { get; private set; }
    internal byte[] RawPng { get; private set; }

    internal static Func<DecodeJob, IReleasable> Work(string path, bool mipmaps, PrefetchBudget budget, int sequence) =>
        job => Load(job, path, mipmaps, budget, sequence);

    public void Release()
    {
        if (Image != null)
        {
            BufferPool.Return(Image.Pixels);
            Image = null;
        }
        RawPng = null;
        if (budget != null)
        {
            budget.Release(charged);
            budget = null;
        }
    }

    private static PageResult Load(DecodeJob job, string path, bool mipmaps, PrefetchBudget budget, int sequence)
    {
        if (!File.Exists(path))
        {
            return new PageResult { Path = path, Missing = true };
        }
        using var stream = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read, 1 << 16, FileOptions.SequentialScan);
        int length = checked((int)stream.Length);
        byte[] file = BufferPool.Rent(length);
        try
        {
            for (int read = 0; read < length;)
            {
                int n = stream.Read(file, read, length - read);
                if (n <= 0)
                {
                    throw new EndOfStreamException(path);
                }
                read += n;
            }
            long estimate = PngDecoder.EstimateBytes(file, length, mipmaps);
            if (budget != null && !budget.Acquire(estimate, sequence, job))
            {
                return null;
            }
            var page = new PageResult { Path = path, budget = budget, charged = estimate };
            long started = Stopwatch.GetTimestamp();
            if (PngDecoder.TryDecode(file, length, mipmaps, out DecodedImage image))
            {
                page.Image = image;
                LoadStats.AddDecode(Stopwatch.GetTimestamp() - started, image.Length, false);
            }
            else
            {
                page.RawPng = new byte[length];
                Buffer.BlockCopy(file, 0, page.RawPng, 0, length);
                LoadStats.AddDecode(0, 0, true);
            }
            return page;
        }
        finally
        {
            BufferPool.Return(file);
        }
    }
}

/// <summary>Skeleton (.json text or .skel bytes) read off the main thread.</summary>
internal sealed class SkeletonResult : IReleasable
{
    private PrefetchBudget budget;
    private long charged;

    internal string Path { get; private set; }
    internal string Text { get; private set; }
    internal byte[] Bytes { get; private set; }

    internal static Func<DecodeJob, IReleasable> Work(string path, bool json, PrefetchBudget budget, int sequence) => job =>
    {
        long size = new FileInfo(path).Length;
        if (budget != null && !budget.Acquire(size, sequence, job))
        {
            return null;
        }
        var result = new SkeletonResult { Path = path, budget = budget, charged = size };
        if (json)
        {
            result.Text = File.ReadAllText(path);
        }
        else
        {
            result.Bytes = File.ReadAllBytes(path);
        }
        return result;
    };

    public void Release()
    {
        if (budget != null)
        {
            budget.Release(charged);
            budget = null;
        }
    }
}
