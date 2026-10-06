using System;
using System.Reflection;
using BD2.GameNames;
using HarmonyLib;
using static Bd2LoginUI.LoginRuntime;

namespace Bd2LoginUI;

// A gate quest may end its cinema in a different scene. The gate's final
// camera coroutine must use that scene's position, rather than the old gate.
internal static class GateTimelinePosition
{
    private const BindingFlags All = BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic;
    private static GameFieldManager PendingField;
    private static GameFieldManager TransitionField;
    private static MapPositionData PendingPosition;
    private static int PendingGateMap;
    private static long Transition;
    private static bool PositionApplied;

    internal static void Install(Harmony harmony)
    {
        try
        {
            MethodInfo timelineMove = typeof(TimelineSignalManager).GetGameMethod("CheckForMoveMap", All);
            MethodInfo complete = typeof(GameFieldManager).GetGameMethod("MoveMapComplete", All);
            MethodInfo gateMove = Game.Method<GameFieldManager>(field => field.MoveMap(default(GateSpotData)));
            MethodInfo startMove = Game.Method<GameFieldManager>(field => field.MoveStartMap());
            MethodInfo setPosition = Game.Method<GameFieldManager>(field => field.SetCharPosition(default(MapPositionData)));
            if (timelineMove == null || timelineMove.GetParameters().Length != 1 ||
                complete == null || complete.GetParameters().Length != 3) throw new MissingMethodException();
            harmony.Patch(timelineMove, prefix: new HarmonyMethod(typeof(GateTimelinePosition), nameof(TimelineMovePrefix)));
            harmony.Patch(complete, prefix: new HarmonyMethod(typeof(GateTimelinePosition), nameof(CompletePrefix)));
            harmony.Patch(gateMove, prefix: new HarmonyMethod(typeof(GateTimelinePosition), nameof(BeginPrefix)));
            harmony.Patch(startMove, prefix: new HarmonyMethod(typeof(GateTimelinePosition), nameof(BeginPrefix)));
            harmony.Patch(setPosition, postfix: new HarmonyMethod(typeof(GateTimelinePosition), nameof(PositionPostfix)));
            Log?.LogInfo("Gate timeline final position patch installed");
        }
        catch (Exception error) { Log?.LogError("Gate timeline final position patch failed: " + error); }
    }

    private static void BeginPrefix(GameFieldManager __instance)
    {
        Transition++;
        TransitionField = __instance;
        PendingField = null;
        PositionApplied = false;
    }

    private static void TimelineMovePrefix(TimelineSignalManager __instance, TimelineMapMoveData __0)
    {
        PendingField = null;
        PositionApplied = false;
        GameFieldManager field = GameFieldManager.Instance;
        if (field == null || field != TransitionField || !field.IsMoveMapProcessing || !field.IsMoveMapByGateSpot ||
            __instance.IsTimelineEndToBattle || __0 == null || string.IsNullOrEmpty(__0.sceneName)) return;
        var target = MapInfo.FindMapBySceneName(__0.sceneName);
        if (target == null || field.CurrentMapInfo == null) return;
        PendingGateMap = field.CurrentMapInfo.Id;
        PendingPosition = new MapPositionData { MapId = target.Id, PlayerPosition = __0.playerPos };
        PendingField = field;
        Log?.LogInfo("Gate timeline final position recorded: transition=" + Transition + " gate_map=" + PendingGateMap +
            " final_map=" + PendingPosition.MapId + " scene=" + __0.sceneName);
    }

    private static void PositionPostfix(GameFieldManager __instance, MapPositionData __0)
    {
        if (PendingField != __instance || !__instance.IsMoveMapByGateSpot || !__instance.IsMoveMapProcessing ||
            !__instance.IsLoadedField || __instance.CurrentMapInfo == null ||
            __instance.CurrentMapInfo.Id != PendingPosition.MapId || __0.MapId != PendingPosition.MapId ||
            __0.PlayerPosition != PendingPosition.PlayerPosition) return;
        PositionApplied = true;
    }

    private static void CompletePrefix(GameFieldManager __instance, ref MapPositionData __0)
    {
        if (PendingField != __instance) return;
        PendingField = null;
        TransitionField = null;
        if (!PositionApplied || !__instance.IsMoveMapProcessing || !__instance.IsMoveMapByGateSpot ||
            __instance.CurrentMapInfo == null || __instance.CurrentMapInfo.Id != PendingPosition.MapId ||
            __0.MapId != PendingGateMap || __0.MapId == PendingPosition.MapId ||
            (TimelineSignalManager.Instance != null && TimelineSignalManager.Instance.IsTimelineEndToBattle)) return;
        int original = __0.MapId;
        __0 = PendingPosition;
        Log?.LogInfo("Gate timeline final position applied: transition=" + Transition + " gate_map=" + original + " final_map=" + __0.MapId);
    }
}
