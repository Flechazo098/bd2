using System;
using System.Diagnostics;
using System.Globalization;
using System.Reflection;
using BD2.GameNames;
using BDNetwork;
using HarmonyLib;
using UnityEngine;

using static Bd2LoginUI.LoginRuntime;
using static Bd2LoginUI.SessionRecovery;

namespace Bd2LoginUI;

// Diagnostics never log response bodies, cookies, credentials or URL queries.
internal static class SessionDiagnostics
{
    private const BindingFlags All = BindingFlags.Instance | BindingFlags.Static | BindingFlags.Public | BindingFlags.NonPublic;
    private static string LastFailure;
    private static float LastFailureTime;
    private static string LastResponseError;
    private static float LastResponseErrorTime;

    internal static void Install(Harmony harmony)
    {
        // Keep diagnostic binding failures separate from functional login hooks.
        TryPatch(harmony, typeof(NetworkManager), "ResponseCheck", nameof(ResponsePrefix), 4);
        TryPatch(harmony, typeof(NetworkManager), "ServerNetworkError", nameof(ServerErrorPrefix), 2);
        TryPatch(harmony, typeof(NetworkManager), "OpenPacketErrorPopup", nameof(PacketPopupPrefix), 3);
        TryPatch(harmony, typeof(AppManager), "AppReStart", nameof(RestartPrefix), 0);
    }

    private static void TryPatch(Harmony harmony, Type type, string name, string callback, int parameters)
    {
        try
        {
            MethodInfo target = type.GetGameMethod(name, All);
            if (target == null || target.GetParameters().Length != parameters) throw new MissingMethodException();
            harmony.Patch(target, prefix: new HarmonyMethod(typeof(SessionDiagnostics), callback));
        }
        catch (Exception error)
        {
            Log?.LogWarning("Session diagnostic binding unavailable: target=" + type.Name + "." + name + " exception=" + error.GetType().Name);
        }
    }

    internal static void Record(string failure)
    {
        try
        {
            LastFailure = failure;
            LastFailureTime = Time.realtimeSinceStartup;
            Log?.LogWarning("Session diagnostic: " + failure + Context(false));
        }
        catch { /* A diagnostic must never interrupt the client's error handling. */ }
    }

    internal static string Context(bool includeFailure = true)
    {
        try
        {
            string recent = LastFailure ?? "none";
            string age = LastFailure == null ? "none" : Math.Max(0, Time.realtimeSinceStartup - LastFailureTime).ToString("F1", CultureInfo.InvariantCulture);
            return " origin=" + (ServerRoot?.GetLeftPart(UriPartial.Authority) ?? "unset") +
                " established=" + EstablishedGameSession + " recovering=" + (SessionRecoveryInProgress != 0) +
                " generation=" + RecoveryGeneration +
                (LastResponseError == null ? "" : " recent_response_error_age_s=" + Math.Max(0, Time.realtimeSinceStartup - LastResponseErrorTime).ToString("F1", CultureInfo.InvariantCulture) + " recent_response_error={" + LastResponseError + "}") +
                (includeFailure ? " recent_failure_age_s=" + age + " recent_failure={" + recent + "}" : "");
        }
        catch { return " diagnostic_context=unavailable"; }
    }

    private static object Value(object target, string name)
    {
        if (target == null) return null;
        Type type = target.GetType();
        return type.GetGameField(name, All)?.GetValue(target) ?? type.GetGameProperty(name, All)?.GetValue(target, null);
    }

    private static void ResponsePrefix(string __0, object __1, object __2)
    {
        try
        {
            object code = Value(__2, "errorType");
            if (code == null || Convert.ToInt32(code, CultureInfo.InvariantCulture) == 0) return;
            LastResponseError = "path=" + Path(__0) + " sequence=" + Value(__1, "Sequence") +
                " packet_code=" + Value(__2, "packetCode") + " error_code=" + code + " reason=" + ErrorReason(code);
            LastResponseErrorTime = Time.realtimeSinceStartup;
            Record("event=game-response-error " + LastResponseError);
        }
        catch (Exception error) { Log?.LogWarning("Session response diagnostic unavailable: exception=" + error.GetType().Name); }
    }

    internal static string Code(object value)
    {
        try { return Convert.ToInt32(value, CultureInfo.InvariantCulture).ToString(CultureInfo.InvariantCulture); }
        catch { return "unknown"; }
    }

    internal static string Path(string value) => (value ?? "unknown").Split('?', '#')[0].Replace('\r', ' ').Replace('\n', ' ');

    private static void ServerErrorPrefix(object __0) => Record("event=server-network-error error_code=" + Code(__0) + " reason=" + ErrorReason(__0));

    private static void PacketPopupPrefix(int __0, int __1, bool __2) => Record(
        "event=packet-error-popup packet_code=" + __0 + " error_code=" + __1 +
        " text_id=" + (100000L + __0 * 100L + __1) + " restart_on_confirm=" + __2);

    internal static string ErrorReason(object error)
    {
        try
        {
            if (error == null) return "unknown";
            int code = Convert.ToInt32(error, CultureInfo.InvariantCulture);
            // Resolve enum labels through the shared name table, including renamed fields.
            foreach (FieldInfo field in typeof(EErrorType).GetFields(BindingFlags.Public | BindingFlags.Static))
                if (Convert.ToInt32(field.GetRawConstantValue(), CultureInfo.InvariantCulture) == code)
                {
                    foreach (string name in new[] { "SESSION_DST", "DB_CLOSE", "QUERY_ERROR", "WAIT_TIMEOUT", "REQUEST_FAILED", "NOT_PINGCHECK_SESSION_DST", "WAIT_PROCESS_REDIRECT", "RPE_NEON_API_ACCESS_TOKEN_EXPIRE", "PRE_NETWORK_ERROR_NEON_API", "CLINET_NET_TIMEOUT", "CLIENT_LOGIC_ERROR", "RPE_UPDATE_MAJOR_CLIENT_DOWN", "RPE_CDN_ASSET_BUNDLE_UPDATE", "RPE_PURCHASED_DISABLED", "RPE_BLOCK_IP", "PRE_BLOCK_NETWORK_TYPE", "RPE_REQUEST_MISSING_PARAM", "RPE_XML_DATA_NOT_FOUND", "RPE_PROTOBUF_ENCODING_ERROR" })
                        if (field.Name == Game.MemberName(typeof(EErrorType), name, GameMemberKind.Field)) return name;
                }
            return "unclassified";
        }
        catch { return "unclassified"; }
    }

    private static void RestartPrefix()
    {
        try
        {
            // Method identities only; stack arguments and exception text may contain credentials.
            var callers = new System.Text.StringBuilder();
            foreach (StackFrame frame in new StackTrace(false).GetFrames() ?? [])
            {
                MethodBase method = frame.GetMethod();
                if (method?.DeclaringType == null || method.DeclaringType == typeof(SessionDiagnostics)) continue;
                if (callers.Length > 0) callers.Append(" <- ");
                callers.Append(method.DeclaringType.FullName).Append('.').Append(method.Name);
                if (callers.Length > 1200) break;
            }
            Log?.LogWarning("Client restart requested:" + Context() + " callers=" + callers);
        }
        catch { /* Preserve the original restart even if stack inspection fails. */ }
    }
}
