using System;
using System.Collections;
using System.Diagnostics;
using System.Globalization;
using System.Reflection;
using System.Text;
using BD2.GameNames;
using BepInEx.Logging;
using gamfs;
using HarmonyLib;
using Proto.Design.common;
using Proto.Net;
using UnityEngine;
using UnityEngine.AI;
using UnityEngine.SceneManagement;

namespace Bd2LocalIdentity;

internal static class FieldTransitionDiagnostics
{
    private static ManualLogSource Log;
    private static float CaptureUntil;
    private static int Sequence, PackId, QuestId;
    private static string Phase;

    internal static void Install(Harmony harmony, ManualLogSource logger)
    {
        Log = logger;
        Patch(harmony, Game.Method(() => QuestPacket.SendQuestClearRequest(0)), nameof(QuestClearSend));
        Patch(harmony, Game.Method(() => QuestPacket.RecvQuestClearResponse(null, 0, 0, null)), nameof(QuestClearReceive));
        Patch(harmony, Game.Method(() => QuestInfo.PlayQuestCinemaTimeline(0, false, null, null)), nameof(QuestCinema));
        Patch(harmony, Game.Method(() => QuestInfo.PlayQuestCinemaTimelineAfterMoveScene(0, false, 0, null, null)), nameof(QuestCinemaAfterMove));
        Patch(harmony, Game.Method<GameFieldManager>(field => field.SetCharPosition(default)), nameof(PositionBefore), nameof(PositionAfter));
        Patch(harmony, Game.Method<GameFieldManager>(field => field.MoveMap(default(MapPositionData))), nameof(MovePosition));
        Patch(harmony, Game.Method<GameFieldManager>(field => field.MoveMap(default(GateSpotData))), nameof(MoveGate));
        Patch(harmony, Game.Method<GateSpotData>(gate => gate.SetStartPosition()), nameof(GateBefore), nameof(GateAfter));
        Patch(harmony, Game.Method<GameFieldManager>(field => field.MoveMapNpcPosition(0, null)), nameof(MoveNpc));
        Patch(harmony, Game.Method<TimelineSignalManager>(signal => signal.EndTimeline()), nameof(TimelineEnd));
        Patch(harmony, Game.Method<GameFieldManager>(field => field.MoveSafeMapAtInvalidPosition()), nameof(Recovery));
        Patch(harmony, Private(typeof(GameFieldManager), "TryGetNpcAutoMoveLandingPosition",
            typeof(Vector3), typeof(Vector3), typeof(Vector3).MakeByRefType()), null, nameof(NpcLanding));
        Patch(harmony, Private(typeof(TimelinePlayManager), "EndTimelineRestorePlayerLocation", typeof(Action)), nameof(Restore));
        Patch(harmony, typeof(global::SceneManager).GetGameMethod(nameof(global::SceneManager.ClearAndLoadMapScene),
            BindingFlags.Public | BindingFlags.Instance, null, [typeof(MapTable), typeof(bool), typeof(bool)], null), nameof(LoadScene));
        UnityEngine.SceneManagement.SceneManager.sceneLoaded += SceneLoaded;
        Log.LogInfo("Field transition diagnostics active: quest cinema, endpoint, scene, placement, gate, NPC landing and recovery");
    }

    private static MethodInfo Private(Type type, string name, params Type[] parameters) =>
        type.GetGameMethod(name, BindingFlags.NonPublic | BindingFlags.Instance, null, parameters, null)
        ?? throw new MissingMethodException(type.FullName, name);

    private static void Patch(Harmony harmony, MethodInfo method, string prefix, string postfix = null)
    {
        if (method == null) throw new MissingMethodException("Field transition diagnostic target missing");
        harmony.Patch(method,
            prefix: prefix == null ? null : new HarmonyMethod(typeof(FieldTransitionDiagnostics), prefix),
            postfix: postfix == null ? null : new HarmonyMethod(typeof(FieldTransitionDiagnostics), postfix));
    }

    private static bool Local => !string.IsNullOrEmpty(Environment.GetEnvironmentVariable("BD2_CLIENT_SERVER_ORIGIN"));
    private static bool Capturing => Local && Time.realtimeSinceStartup <= CaptureUntil && Sequence != 0;

    private static void Begin(int questId, bool accept)
    {
        if (!Local) return;
        string phase = accept ? "accept" : "complete";
        int packId = SG.Pack?.PlayingPackID ?? 0;
        if (!Capturing || PackId != packId || QuestId != questId || Phase != phase) Sequence++;
        PackId = packId;
        QuestId = questId;
        Phase = phase;
        CaptureUntil = Time.realtimeSinceStartup + 180;
        QuestDTO quest = QuestInfo.GetQuest(questId);
        Record("quest-cinema", "cinema=" + (accept ? quest?.AcceptCinemaName : quest?.CompleteCinemaName) +
            " quest_map=" + quest?.MapId + " timeline_start_map=" + quest?.TimelineStartMapId);
    }

    private static void QuestClearSend(int __0)
    {
        try { Begin(__0, false); Record("quest-clear-send"); }
        catch (Exception error) { DiagnosticError("quest-clear-send", error); }
    }

    private static void QuestClearReceive(byte[] __0, int __2)
    {
        if (!Local) return;
        try
        {
            QuestClearResponse response = QuestClearResponse.Parser.ParseFrom(__0);
            Begin(response.ClearQuestId, false);
            Record("quest-clear-receive", "error=" + __2 + " successor_quest=" + response.QuestInfo?.Id);
        }
        catch (Exception error) { DiagnosticError("quest-clear-receive", error); }
    }

    private static void QuestCinema(int __0, bool __1, ref Action<bool> __2, ref Action __3)
    {
        try { Begin(__0, __1); WrapCallbacks(ref __2, ref __3); }
        catch (Exception error) { DiagnosticError("quest-cinema", error); }
    }

    private static void QuestCinemaAfterMove(int __0, bool __1, int __2, ref Action<bool> __3, ref Action __4)
    {
        try { Begin(__0, __1); Record("quest-cinema-after-move", "before_map=" + __2); WrapCallbacks(ref __3, ref __4); }
        catch (Exception error) { DiagnosticError("quest-cinema-after-move", error); }
    }

    private static void WrapCallbacks(ref Action<bool> start, ref Action end)
    {
        if (!Capturing) return;
        int sequence = Sequence;
        Action<bool> originalStart = start;
        Action originalEnd = end;
        if (originalStart != null) start = playing =>
        {
            if (sequence == Sequence)
            {
                if (playing) CaptureUntil = Time.realtimeSinceStartup + 900;
                Record("cinema-start-callback", "playing=" + playing);
            }
            originalStart?.Invoke(playing);
        };
        if (originalEnd != null) end = () =>
        {
            if (sequence == Sequence) Record("cinema-end-callback-before");
            try { originalEnd?.Invoke(); }
            finally
            {
                if (sequence == Sequence)
                {
                    CaptureUntil = Time.realtimeSinceStartup + 20;
                    Record("cinema-end-callback-after");
                    try
                    {
                        GameFieldManager field = GameFieldManager.Instance;
                        field?.StartCoroutine(SettledSnapshots(sequence));
                    }
                    catch (Exception error) { DiagnosticError("settled-snapshots", error); }
                }
            }
        };
    }

    private static IEnumerator SettledSnapshots(int sequence)
    {
        yield return null;
        if (sequence == Sequence) Record("cinema-end-next-frame");
        yield return new WaitForSecondsRealtime(1);
        if (sequence == Sequence) Record("cinema-end-after-one-second");
    }

    private static void PositionBefore(MapPositionData __0) =>
        Record("set-position-before", Placement(__0) + " callers=" + Callers());
    private static void PositionAfter(MapPositionData __0) => Record("set-position-after", Placement(__0));
    private static void MovePosition(MapPositionData __0) => Record("move-map-position", Placement(__0) + " callers=" + Callers());
    private static void MoveGate(GateSpotData __0)
    {
        if (Capturing) Record("move-map-gate", Gate(__0) + " callers=" + Callers());
    }
    private static void GateBefore(GateSpotData __instance)
    {
        if (Capturing) Record("gate-start-before", Gate(__instance));
    }
    private static void GateAfter(GateSpotData __instance)
    {
        if (Capturing) Record("gate-start-after", Gate(__instance));
    }
    private static void MoveNpc(int __0) => Record("move-map-npc", "npc=" + __0 + " callers=" + Callers());
    private static void NpcLanding(Vector3 __0, Vector3 __1, Vector3 __2, bool __result) =>
        Record("npc-landing", "npc_position=" + Vector(__0) + " direction=" + Vector(__1) +
            " success=" + __result + " landing=" + Vector(__2));

    private static void LoadScene(MapTable __0, bool __1) => Record("scene-load-request",
        "target_map=" + __0?.Id + " target_scene=" + __0?.MapScenePath + " force=" + __1 + " callers=" + Callers());

    private static void SceneLoaded(Scene scene, LoadSceneMode mode) =>
        Record("scene-loaded", "loaded_scene=" + scene.name + " mode=" + mode);

    private static void TimelineEnd(TimelineSignalManager __instance)
    {
        if (!Capturing) return;
        try
        {
            TimelineMapMoveData endpoint = __instance.MapMoveData;
            Record("timeline-end", "timeline_object=" + __instance.gameObject.name + " skipped=" + __instance.IsTimelineSkipped +
                " already_ended=" + __instance.IsEndTimeline + " endpoint_scene=" + endpoint?.sceneName +
                " endpoint_position=" + (endpoint == null ? "none" : Vector(endpoint.playerPos)));
        }
        catch (Exception error) { DiagnosticError("timeline-end", error); }
    }

    private static void Restore() => Record("restore-timeline-position", "callers=" + Callers());
    private static void Recovery()
    {
        if (!Local) return;
        // A recovery outside the cinema window still needs its native caller and placement.
        if (!Capturing) { Sequence++; Phase = "recovery"; PackId = SG.Pack?.PlayingPackID ?? 0; QuestId = 0; }
        CaptureUntil = Time.realtimeSinceStartup + 20;
        Record("recover-invalid-position", "callers=" + Callers());
    }

    private static string Placement(MapPositionData position) => "requested_map=" + position.MapId +
        " requested_position=" + Vector(position.PlayerPosition);

    private static string Gate(GateSpotData gate)
    {
        if (gate == null) return "gate=none";
        try
        {
            // MapPositionData may select a random spawn; inspect its data without invoking that getter.
            return "gate_id=" + gate.TableId + " gate_map=" + gate.MapID + " before_map=" + gate.BeforeMapID +
                " after_map=" + gate.AfterMapID + " gate_position=" + Vector(gate.transform.position) +
                " before_spawn=" + Vector(gate.QuestBeforeMapData.movePlayerPosition) +
                " after_spawn=" + Vector(gate.QuestAfterMapData.movePlayerPosition) +
                " selected_spawn=" + Vector(gate.CurrentMapData.movePlayerPosition);
        }
        catch (Exception error) { return "gate_id=" + gate.TableId + " gate_data_unavailable=" + error.GetType().Name; }
    }

    private static void Record(string stage, string detail = "")
    {
        if (!Capturing) return;
        try
        {
            GameFieldManager field = GameFieldManager.Instance;
            PlayerController player = field?.PlayerController;
            PlayerMoveController movement = player?.PlayerMoveController;
            Camera camera = GameCameraManager.Instance?.MainCamera;
            TimelinePlayManager timeline = SG.TimelinePlay;
            string navigation = "none";
            NavMeshAgent agent = movement?.Navagent;
            if (agent != null)
            {
                bool onMesh = agent.enabled && agent.isOnNavMesh;
                bool sample = NavMesh.SamplePosition(player.transform.position, out NavMeshHit hit, 1, agent.areaMask);
                navigation = "enabled:" + agent.enabled + ",on_mesh:" + onMesh + ",sample_1m:" + sample +
                    ",sample_position:" + (sample ? Vector(hit.position) : "none");
                if (onMesh) navigation += ",next:" + Vector(agent.nextPosition) + ",has_path:" + agent.hasPath + ",path:" + agent.pathStatus;
            }
            Log.LogInfo("Field transition: seq=" + Sequence + " stage=" + stage + " pack=" + PackId +
                " quest=" + QuestId + " phase=" + Phase + " frame=" + Time.frameCount +
                " elapsed_s=" + Time.realtimeSinceStartup.ToString("F3", CultureInfo.InvariantCulture) +
                " map=" + field?.CurrentMapInfo?.Id + " field_scene=" + field?.CurrentMapScenePath +
                " active_scene=" + UnityEngine.SceneManagement.SceneManager.GetActiveScene().name +
                " player_scene=" + (player == null ? "none" : player.gameObject.scene.name) +
                " loaded=" + field?.IsLoadedField + " moving_map=" + field?.IsMoveMapProcessing +
                " player=" + (player == null ? "none" : Vector(player.transform.position)) +
                " camera=" + (camera == null ? "none" : Vector(camera.transform.position) + "/" + Vector(camera.transform.eulerAngles)) +
                " nav={" + navigation + "} start={" + Gate(field?.StartSpotData) + "}" +
                " cinema=" + timeline?.IsPlayCinemaTimeline + " restore_allowed=" + timeline?.IsAvailableRestoreAfterTimeline +
                " timeline_start_map=" + timeline?.MapIdWhenTimelineStarting +
                " timeline_start_position=" + (timeline == null ? "none" : Vector(timeline.PlayerPositionWhenTimelineStarting)) + " " + detail);
        }
        catch (Exception error) { DiagnosticError(stage, error); }
    }

    private static string Vector(Vector3 value) => string.Format(CultureInfo.InvariantCulture,
        "({0:F3},{1:F3},{2:F3})", value.x, value.y, value.z);

    private static string Callers()
    {
        if (!Capturing) return "";
        var callers = new StringBuilder();
        foreach (StackFrame frame in new StackTrace(false).GetFrames() ?? [])
        {
            MethodBase method = frame.GetMethod();
            if (method?.DeclaringType == null || method.DeclaringType == typeof(FieldTransitionDiagnostics)) continue;
            if (callers.Length > 0) callers.Append(" <- ");
            callers.Append(method.DeclaringType.FullName).Append('.').Append(method.Name);
            if (callers.Length >= 1600) break;
        }
        return callers.ToString();
    }

    private static void DiagnosticError(string stage, Exception error) =>
        Log?.LogWarning("Field transition diagnostic unavailable: stage=" + stage + " exception=" + error.GetType().Name);
}
