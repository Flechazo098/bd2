using System;
using System.Collections;
using System.Threading;
using UnityEngine;

using static Bd2LoginUI.LoginRuntime;
using static Bd2LoginUI.SessionRecovery;

namespace Bd2LoginUI;

// Runs native HTTP work and delivers results to Unity coroutines with owner cancellation.
internal static class ControlRequests
{
    internal sealed class ControlProbeResult
    {
        public bool Success;
        public string Body;
        public string Error;
        public int StatusCode;
        public bool RefreshInvalid;
    }

    // Native HTTP avoids UnityTls for owned auth/control endpoints. The coroutine
    // applies completed results on the Unity thread and rejects cancelled generations.
    internal static IEnumerator RequestControlEndpoint(Uri uri, string method, byte[] body,
        string authorization, int timeoutSeconds, Action<ControlProbeResult> completed,
        CancellationToken lifetime, CancellationToken generation)
    {
        try { ValidateOAuthTransport(uri); }
        catch
        {
            if (body != null) Array.Clear(body, 0, body.Length);
            throw;
        }
        using var cancel = CancellationTokenSource.CreateLinkedTokenSource(lifetime, generation);
        byte[] payload = body == null ? null : (byte[])body.Clone();
        if (body != null) Array.Clear(body, 0, body.Length);
        System.Threading.Tasks.Task<PlatformControlHttp.Response> task = null;
        bool responseConsumed = false;
        try
        {
            task = PlatformControlHttp.Send(uri, method, payload, authorization, timeoutSeconds, cancel.Token, generation);
            // Native work may finish after its Unity owner is destroyed. Observe it
            // and clear the upload clone only when transport no longer reads it.
            task.ContinueWith(finished =>
            {
                if (payload != null) Array.Clear(payload, 0, payload.Length);
                if (finished.IsFaulted) { _ = finished.Exception; }
            }, System.Threading.Tasks.TaskScheduler.Default);
            while (!task.IsCompleted && !cancel.IsCancellationRequested)
            {
                yield return null;
            }
            if (cancel.IsCancellationRequested) yield break;
            if (task.IsFaulted || task.IsCanceled)
            {
                completed(new ControlProbeResult { Error = task.IsCanceled ? "Cancelled" : "NativeTransportFailure" });
                yield break;
            }
            PlatformControlHttp.Response result = task.GetAwaiter().GetResult();
            try
            {
                completed(new ControlProbeResult
                {
                    Success = result.Success,
                    StatusCode = result.StatusCode,
                    Body = result.Body,
                    RefreshInvalid = result.RefreshInvalid,
                    Error = result.Error
                });
            }
            finally
            {
                ClearNativeResponse(result);
                responseConsumed = true;
            }
        }
        finally
        {
            cancel.Cancel();
            if (task == null && payload != null) Array.Clear(payload, 0, payload.Length);
            if (task != null && !responseConsumed)
            {
                task.ContinueWith(finished =>
                {
                    if (finished.Status == System.Threading.Tasks.TaskStatus.RanToCompletion) ClearNativeResponse(finished.Result);
                    if (finished.IsFaulted) { _ = finished.Exception; }
                }, System.Threading.Tasks.TaskScheduler.Default);
            }
        }
    }

    private static void ClearNativeResponse(PlatformControlHttp.Response response)
    {
        if (response == null) return;
        if (response.Data != null) Array.Clear(response.Data, 0, response.Data.Length);
        response.Data = null;
        response.Body = null;
    }

    [System.Diagnostics.CodeAnalysis.SuppressMessage("Design", "CA1001", Justification = "Unity OnDestroy cancels and disposes Lifetime.")]
    public sealed class AuthenticationRequestLifetime : MonoBehaviour
    {
        internal readonly CancellationTokenSource Lifetime = new();
        [System.Diagnostics.CodeAnalysis.SuppressMessage("Style", "IDE0051", Justification = "Unity invokes this instance lifecycle callback.")]
        private void OnDestroy()
        {
            Lifetime.Cancel();
            Lifetime.Dispose();
        }
    }

    internal static IEnumerator AuthRequest(object introUI, Uri uri, string method, byte[] body,
        string authorization, Action<ControlProbeResult> completed)
    {
        var intro = introUI as MonoBehaviour;
        if (intro == null)
        {
            if (body != null) Array.Clear(body, 0, body.Length);
            yield break;
        }
        AuthenticationRequestLifetime owner = intro.GetComponent<AuthenticationRequestLifetime>() ??
            intro.gameObject.AddComponent<AuthenticationRequestLifetime>();
        int expectedGeneration = Volatile.Read(ref RecoveryGeneration);
        CancellationToken lifetime = owner.Lifetime.Token;
        ControlProbeResult result = null;
        yield return RequestControlEndpoint(uri, method, body, authorization, 15, response => result = response,
            lifetime, ControlProbeCancellation.Token);
        if (result != null && intro != null && !lifetime.IsCancellationRequested &&
            expectedGeneration == Volatile.Read(ref RecoveryGeneration)) completed(result);
    }

}
