using System;
using System.Collections.Generic;
using System.Linq;
using System.Reflection;
using System.Threading;
using BD2.GameNames;
using BepInEx.Logging;
using Google.Protobuf;
using HarmonyLib;
using static BD2.GameNames.Game;

namespace Bd2CaptureEnvironment;

internal static class PacketCapture
{
    private static readonly object CorrelationLock = new object();
    private static readonly Dictionary<string, Queue<PendingRequest>> PendingRequests =
        new Dictionary<string, Queue<PendingRequest>>(StringComparer.Ordinal);
    private static ManualLogSource Log;
    private static long Sequence;
    private sealed class PendingRequest
    {
        public long Id;
        public string Type;
    }

    internal static void Install(Harmony harmony, ManualLogSource log)
    {
        Log = log;
        Type manager = typeof(BDNetwork.NetworkManager);
        MethodInfo send = manager?.GetMethods(BindingFlags.Instance | BindingFlags.Public)
            .SingleOrDefault(method => method.IsGameMethod("Send") &&
                method.ReturnType == typeof(void) &&
                method.GetParameters().Length == 6 &&
                typeof(IMessage).IsAssignableFrom(method.GetParameters()[0].ParameterType));
        if (send != null)
        {
            harmony.Patch(send, prefix: new HarmonyMethod(
                typeof(PacketCapture), nameof(GameSendPrefix)));
            Log?.LogInfo("Plaintext request capture installed");
        }
        else
        {
            Log?.LogWarning("NetworkManager.Send plaintext hook not found");
        }

        Type responseData = typeof(BDNetwork.ResponseData);
        MethodInfo responseCheck = manager?.GetMethods(
                BindingFlags.Instance | BindingFlags.NonPublic)
            .SingleOrDefault(method =>
            {
                ParameterInfo[] parameters = method.GetParameters();
                return method.IsGameMethod("ResponseCheck") && method.ReturnType == typeof(bool) && parameters.Length == 4 &&
                    parameters[0].ParameterType == typeof(string) &&
                    parameters[2].ParameterType == responseData &&
                    parameters[3].ParameterType == typeof(byte[]);
            });
        if (responseCheck != null)
        {
            harmony.Patch(responseCheck, prefix: new HarmonyMethod(
                typeof(PacketCapture), nameof(ResponseCheckPrefix)));
            Log?.LogInfo("Path-correlated plaintext response capture installed");
        }
        else
        {
            Log?.LogWarning("NetworkManager.ResponseCheck plaintext hook not found");
        }
    }

    private static void GameSendPrefix(IMessage __0)
    {
        try
        {
            if (__0 == null) return;
            byte[] body = __0.ToByteArray();
            string type = __0.GetType().FullName ?? __0.GetType().Name;
            string path = RequestPath(__0.GetType().Name);
            long id = Interlocked.Increment(ref Sequence);
            EnqueueRequest(path, new PendingRequest { Id = id, Type = type });
            CaptureWriter.QueueCapture(id, "request", path, type, body);
        }
        catch (Exception ex)
        {
            Log?.LogWarning("plaintext request capture failed: " + ex.Message);
        }
    }

    private static void ResponseCheckPrefix(string __0, byte[] __3)
    {
        try
        {
            string path = NormalizePath(__0);
            PendingRequest request = DequeueRequest(path);
            long id = request?.Id ?? Interlocked.Increment(ref Sequence);
            string type = ResponseType(request?.Type, path);
            CaptureWriter.QueueCapture(id, "response", path, type, __3);
        }
        catch (Exception ex)
        {
            Log?.LogWarning("plaintext response capture failed: " + ex.Message);
        }
    }

    private static string RequestPath(string typeName)
    {
        const string suffix = "Request";
        if (typeName != null && typeName.EndsWith(suffix, StringComparison.Ordinal))
            typeName = typeName.Substring(0, typeName.Length - suffix.Length);
        return NormalizePath(typeName);
    }

    private static string NormalizePath(string path)
    {
        if (string.IsNullOrWhiteSpace(path)) return "/unknown";
        return path[0] == '/' ? path : "/" + path;
    }

    private static string ResponseType(string requestType, string path)
    {
        const string suffix = "Request";
        if (!string.IsNullOrEmpty(requestType) &&
            requestType.EndsWith(suffix, StringComparison.Ordinal))
            return requestType.Substring(0, requestType.Length - suffix.Length) + "Response";
        return "Proto.Net." + path.TrimStart('/') + "Response";
    }

    private static void EnqueueRequest(string path, PendingRequest request)
    {
        lock (CorrelationLock)
        {
            if (!PendingRequests.TryGetValue(path, out Queue<PendingRequest> requests))
            {
                requests = new Queue<PendingRequest>();
                PendingRequests.Add(path, requests);
            }
            requests.Enqueue(request);
        }
    }

    private static PendingRequest DequeueRequest(string path)
    {
        lock (CorrelationLock)
        {
            if (PendingRequests.TryGetValue(path, out Queue<PendingRequest> requests) &&
                requests.Count != 0)
            {
                PendingRequest request = requests.Dequeue();
                if (requests.Count == 0) PendingRequests.Remove(path);
                return request;
            }
        }
        return null;
    }

}
