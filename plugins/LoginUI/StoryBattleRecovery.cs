using System;
using System.Collections;
using System.Reflection;
using BD2.GameNames;
using HarmonyLib;
using UnityEngine;

using static Bd2LoginUI.LoginRuntime;
using static Bd2LoginUI.SessionRecovery;

namespace Bd2LoginUI;

internal static class StoryBattleRecovery
{
    private const BindingFlags InstanceFlags = BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic;
    private static readonly FieldInfo Replay = typeof(TimelineSignalManager).GetGameField("_isReplayTimeline", InstanceFlags);
    private static readonly FieldInfo PlayType = typeof(TimelineSignalManager).GetGameField("_timelinePlayType", InstanceFlags);
    private static readonly FieldInfo Monster = typeof(TimelineSignalManager).GetGameField("_battleMonsterId", InstanceFlags);
    private static readonly FieldInfo EndCallback = typeof(TimelineSignalManager).GetGameField("_callbackTimelineEnd", InstanceFlags);
    private static readonly FieldInfo Director = typeof(TimelineSignalManager).GetGameField("_playableDirector", InstanceFlags);
    private static readonly FieldInfo Balloon = typeof(TimelineSignalManager).GetGameField("_balloonUI", InstanceFlags);
    private static readonly FieldInfo Selection = typeof(TimelineSignalManager).GetGameField("_cinemaSelectUI", InstanceFlags);
    private static readonly FieldInfo Skip = typeof(TimelineSignalManager).GetGameField("_skipUI", InstanceFlags);
    private static readonly MethodInfo EndProcess = typeof(TimelineSignalManager).GetGameMethod("EndTimelineProcess", InstanceFlags);
    private static readonly MethodInfo CheckEntry = typeof(GameFieldManager).GetGameMethod("CheckEnterPackQuest", InstanceFlags);
    private static TimelineSignalManager Recovering;
    private static bool ReturningToInn;

    internal static void Install(Harmony harmony)
    {
        if (Replay == null || PlayType == null || Monster == null || EndCallback == null || Director == null || Balloon == null ||
            Selection == null || Skip == null || EndProcess == null || CheckEntry == null)
            throw new MissingMemberException("Story battle recovery members are unavailable");
        harmony.Patch(Game.Method<TimelineSignalManager>(signal => signal.EndTimeline()),
            prefix: new HarmonyMethod(typeof(StoryBattleRecovery), nameof(BeforeEnd)) { priority = Priority.First });
        harmony.Patch(CheckEntry,
            prefix: new HarmonyMethod(typeof(StoryBattleRecovery), nameof(BeforeEntry)) { priority = Priority.First });
        harmony.Patch(typeof(AppManager).GetGameMethod("AppReStart", InstanceFlags, null, Type.EmptyTypes, null),
            prefix: new HarmonyMethod(typeof(StoryBattleRecovery), nameof(Reset)));
    }

    private static void Reset()
    {
        Recovering = null;
        ReturningToInn = false;
    }

    private static bool BeforeEntry() => !ReturningToInn;

    private static bool BeforeEnd(TimelineSignalManager __instance)
    {
        if (ReferenceEquals(Recovering, __instance)) return false;
        if (ServerRoot == null || !EstablishedGameSession || __instance.IsEndTimeline ||
            (bool)Replay.GetValue(__instance) || (ETimelinePlayType)PlayType.GetValue(__instance) != ETimelinePlayType.Quest ||
            !BDNetwork.CommonPacket.IsAllDeadCharactersInDeck)
            return true;
        if (__instance.MapMoveData != null && !string.IsNullOrEmpty(__instance.MapMoveData.sceneName)) return true;
        PackManager pack = FindUnitySingleton<PackManager>();
        if (pack == null || pack.EnterPackDTO == null ||
            pack.EnterPackDTO.PackType != (int)Proto.Net.Define_PackType.PackStory &&
            pack.EnterPackDTO.PackType != (int)Proto.Net.Define_PackType.PackTutorial)
            return true;
        GameFieldManager field = FindUnitySingleton<GameFieldManager>();
        if (field == null || field.GetFieldMonsterObject((int)Monster.GetValue(__instance)) == null) return true;
        Recovering = __instance;
        StartStoryCoroutine(Recover(__instance, field));
        return false;
    }

    private static IEnumerator Recover(TimelineSignalManager timeline, GameFieldManager field)
    {
        Log?.LogInfo("Story battle waiting for exhausted combat party recovery");
        bool received = false;
        BDNetwork.CommonPacket.SendDeckCharAutoReviveRequest(BDNetwork.CommonPacket.UserInfo.AutoReviveCastingCharInvenIndex,
            _ => received = true);
        float deadline = Time.realtimeSinceStartup + 45f;
        while (!received && ReferenceEquals(Recovering, timeline) && !ApplicationQuitting && timeline != null)
        {
            if (Time.realtimeSinceStartup >= deadline)
            {
                Recovering = null;
                ShowStoryFailure("combat party recovery was not acknowledged within 45 seconds");
                yield break;
            }
            yield return null;
        }
        if (!ReferenceEquals(Recovering, timeline) || ApplicationQuitting || timeline == null || field == null) yield break;
        if (!BDNetwork.CommonPacket.IsAllDeadCharactersInDeck)
        {
            Recovering = null;
            timeline.EndTimeline();
            yield break;
        }

        Log?.LogInfo("Story battle deferred until native inn recovery completes");
        ReturningToInn = true;
        StoryCheckpoint.ResetSession();
        // Abandon this presentation without acknowledging its persisted checkpoint.
        EndCallback.SetValue(timeline, null);
        timeline.OnClickAutoTimeline(false, EStoryAutoState.auto);
        ((TimelineBalloonUI)Balloon.GetValue(timeline))?.SetActive(false);
        ((CinemaSelectUI)Selection.GetValue(timeline))?.SetActive(false);
        ((StorySkipUI)Skip.GetValue(timeline))?.SetActive(false);
        object director = Director.GetValue(timeline);
        director?.GetType().GetMethod("Pause", Type.EmptyTypes)?.Invoke(director, null);
        yield return (IEnumerator)EndProcess.Invoke(timeline, null);
        if (!ReferenceEquals(Recovering, timeline) || ApplicationQuitting || field == null) yield break;
        yield return field.MoveMotelByFieldAllDied();
        if (!ReferenceEquals(Recovering, timeline) || ApplicationQuitting) yield break;
        ReturningToInn = false;
        Recovering = null;
        if (field != null && EstablishedGameSession && !BDNetwork.CommonPacket.IsAllDeadCharactersInDeck)
            CheckEntry.Invoke(field, null);
    }
}
