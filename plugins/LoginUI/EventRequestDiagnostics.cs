using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.Reflection;
using System.Text;
using BD2.GameNames;
using HarmonyLib;
using gamfs;
using Proto.Net;
using UnityEngine;
using static Bd2LoginUI.LoginRuntime;
using static Bd2LoginUI.SessionRecovery;

namespace Bd2LoginUI;

// Read-only method identities and schedule IDs; never packet bodies or credentials.
internal static class EventRequestDiagnostics
{
    private const BindingFlags All = BindingFlags.Public | BindingFlags.NonPublic | BindingFlags.Instance | BindingFlags.Static;
    private sealed class Sample { internal int Count; internal float Last, Logged; internal bool Burst; }
    private static readonly Dictionary<string, Sample> Samples = [];

    internal static void Install(Harmony harmony)
    {
        Patch(harmony, typeof(MiniGameRoulettePacket), "SendMiniGameRouletteInfoRequest", nameof(SendPrefix), null, 2);
        Patch(harmony, typeof(MiniGameRoulettePacket), "RecvMiniGameRouletteInfoResponse", null, nameof(ReceivePostfix), 3);
        Patch(harmony, typeof(MiniGameRouletteUI), "DayReset", nameof(DayPrefix), null, 0);
        Patch(harmony, typeof(EventUI), "OpenEventUI", nameof(OpenPrefix), null, 3);
    }

    private static void Patch(Harmony harmony, Type type, string name, string prefix, string postfix, int count)
    {
        try
        {
            MethodInfo method = type.GetGameMethod(name, All);
            if (method == null || method.GetParameters().Length != count) throw new MissingMethodException();
            harmony.Patch(method, prefix: prefix == null ? null : new HarmonyMethod(typeof(EventRequestDiagnostics), prefix),
                postfix: postfix == null ? null : new HarmonyMethod(typeof(EventRequestDiagnostics), postfix));
        }
        catch (Exception error) { Log?.LogWarning("Event diagnostic binding unavailable: target=" + type.Name + "." + name + " exception=" + error.GetType().Name); }
    }

    private static bool Enabled => ServerRoot != null && EstablishedGameSession;
    private static bool ShouldLog(string key, out string sample)
    {
        sample = "";
        if (!Enabled) return false;
        lock (Samples)
        {
            float now = Time.realtimeSinceStartup;
            if (!Samples.TryGetValue(key, out Sample state)) Samples[key] = state = new Sample();
            float gap = state.Count == 0 ? -1 : now - state.Last;
            bool burst = state.Count > 0 && gap < 1;
            bool log = state.Count < 3 || (burst && !state.Burst) || now - state.Logged >= 10;
            state.Count++; state.Last = now; state.Burst = burst;
            if (!log) return false;
            state.Logged = now;
            sample = " count=" + state.Count + " gap_s=" + gap.ToString("F3", CultureInfo.InvariantCulture) + " burst=" + burst;
            return true;
        }
    }

    private static string Identity(Delegate callback) => callback == null ? "none" : callback.Method.DeclaringType?.FullName + "." + callback.Method.Name;
    private static string Callers()
    {
        var text = new StringBuilder();
        foreach (StackFrame frame in new StackTrace(false).GetFrames() ?? [])
        {
            MethodBase method = frame.GetMethod();
            if (method?.DeclaringType == null || method.DeclaringType == typeof(EventRequestDiagnostics)) continue;
            if (text.Length > 0) text.Append(" <- ");
            text.Append(method.DeclaringType.FullName).Append('.').Append(method.Name);
            if (text.Length >= 1400) break;
        }
        return text.ToString();
    }

    private static string Clock()
    {
        try { return " day_remain_ms=" + SG.Schedule.DayResetStateRemain().ToString("F0", CultureInfo.InvariantCulture) + " server_utc=" + SG.Time.Now().ToString("O", CultureInfo.InvariantCulture); }
        catch { return " clock=unavailable"; }
    }

    private static void SendPrefix(IList<int> __0, Action __1)
    {
        try { if (ShouldLog("send", out string sample)) Log?.LogInfo("Event diagnostic: roulette-send" + sample + " schedules=" + (__0 == null ? "null" : string.Join(",", __0)) + " callback=" + Identity(__1) + Clock() + " callers=" + Callers()); }
        catch { }
    }

    private static void ReceivePostfix(byte[] __0, bool __result, int __2)
    {
        try
        {
            if (!ShouldLog("receive", out string sample)) return;
            MiniGameRouletteInfoResponse data = MiniGameRouletteInfoResponse.Parser.ParseFrom(__0);
            var ids = new List<string>();
            foreach (MiniGameRouletteDBInfo row in data.RouletteInfo) { if (ids.Count == 32) break; ids.Add(row.EventScheduleId.ToString(CultureInfo.InvariantCulture)); }
            Log?.LogInfo("Event diagnostic: roulette-receive" + sample + " success=" + __result + " error_code=" + __2 + " info_count=" + data.RouletteInfo.Count + " schedules=" + string.Join(",", ids));
        }
        catch { }
    }

    private static void DayPrefix(MiniGameRouletteUI __instance)
    {
        try { if (ShouldLog("day", out string sample)) Log?.LogInfo("Event diagnostic: roulette-day-reset" + sample + " instance=" + __instance.GetInstanceID() + Clock() + " callers=" + Callers()); }
        catch { }
    }

    private static void OpenPrefix(EventUI __instance, int __1, int __2)
    {
        try { if (ShouldLog("open", out string sample)) Log?.LogInfo("Event diagnostic: event-open" + sample + " instance=" + __instance.GetInstanceID() + " event_type=" + __1 + " event_id=" + __2 + Clock() + " callers=" + Callers()); }
        catch { }
    }
}
