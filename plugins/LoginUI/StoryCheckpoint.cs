using System;
using System.Collections;
using System.Globalization;
using System.Reflection;
using System.Runtime.Serialization;
using System.Threading;
using System.Threading.Tasks;
using BD2.GameNames;
using HarmonyLib;
using UnityEngine;

using static BD2.GameNames.Game;
using static Bd2LoginUI.LoginRuntime;
using static Bd2LoginUI.SessionRecovery;

namespace Bd2LoginUI;

internal static class StoryCheckpoint
{
    [DataContract]
    internal sealed class Checkpoint
    {
        [DataMember(Name = "checkpoint_id")] public string ID;
        [DataMember(Name = "pack_id")] public int PackID;
        [DataMember(Name = "quest_id")] public int QuestID;
        [DataMember(Name = "difficulty")] public int Difficulty;
        [DataMember(Name = "phase")] public string Phase;
        [DataMember(Name = "timeline")] public string Timeline;
        [DataMember(Name = "object_id")] public int ObjectID;
        [DataMember(Name = "map_id")] public int MapID;
        [DataMember(Name = "simple_talk_group_id")] public int TalkGroupID;
    }

    [DataContract]
    internal sealed class Request
    {
        [DataMember(Name = "operation")] public string Operation;
        [DataMember(Name = "checkpoint_id")] public string ID;
        [DataMember(Name = "pack_id")] public int PackID;
        [DataMember(Name = "quest_id")] public int QuestID;
        [DataMember(Name = "difficulty")] public int Difficulty;
        [DataMember(Name = "phase")] public string Phase;
        [DataMember(Name = "timeline")] public string Timeline;
        [DataMember(Name = "object_id")] public int ObjectID;
        [DataMember(Name = "map_id")] public int MapID;
        [DataMember(Name = "simple_talk_group_id")] public int TalkGroupID;
    }

    [DataContract]
    internal sealed class Response
    {
        [DataMember(Name = "checkpoint")] public Checkpoint Pending = null;
        internal bool Conflict;
        internal int RejectedStatus;
    }

    private sealed class Interaction
    {
        internal Checkpoint Checkpoint;
        internal FieldQuestObjectController Controller;
        internal Action Completed;
        internal SimpleBalloonUI TalkUI;
        internal bool TalkFinished;
        internal bool CompletionScheduled;
        internal bool UpdateDeferred;
        internal int Session;
    }

    private const BindingFlags InstanceFlags = BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic;
    private static MethodInfo StartCinema;
    private static MethodInfo CheckEntry;
    private static MethodInfo Interact;
    private static MethodInfo SetLocalInt;
    private static int Session;
    private static MethodInfo InvokingNative;
    private static bool RestoringComplete;
    private static Interaction ActiveInteraction;
    private static int PendingCinemaCompletions;
    private static CancellationTokenSource SessionCancellation = new();
    internal static bool WaitingForEntrySync;
    internal static bool SynchronizedStory;

    private static bool IsStoryReplay => QuicklyQuestHelper.IsOpenUI || StoryReplaySuppoter.Instance?.IsReplayingStory == true;

    internal static void Install(Harmony harmony)
    {
        StartCinema = typeof(TimelinePlayManager).GetGameMethod("StartCinemaTimeline", InstanceFlags, null,
            [typeof(int), typeof(string), typeof(int), typeof(int), typeof(int), typeof(Action), typeof(Action), typeof(ETimelinePlayType), typeof(string)], null);
        CheckEntry = typeof(GameFieldManager).GetGameMethod("CheckEnterPackQuest", InstanceFlags);
        Interact = typeof(FieldQuestObjectController).GetGameMethod("InteractionQuest", InstanceFlags);
        MethodInfo guildIntro = typeof(GameFieldManager).GetGameMethod("CoCheckGuildRaidIntroTimelineAndOpenUI", InstanceFlags, null, Type.EmptyTypes, null);
        SetLocalInt = Game.FindType("gamfs.GameUtil")?.GetGameMethod("SetInt", BindingFlags.Static | BindingFlags.Public | BindingFlags.NonPublic,
            null, [typeof(string), typeof(int), typeof(bool)], null);
        if (guildIntro == null || SetLocalInt == null)
            throw new MissingMethodException("Guild raid introduction recovery members are unavailable");
        harmony.Patch(guildIntro, postfix: new HarmonyMethod(typeof(StoryCheckpoint), nameof(GuildIntroPostfix)));
        harmony.Patch(StartCinema, prefix: new HarmonyMethod(typeof(StoryCheckpoint), nameof(CinemaPrefix)));
        harmony.Patch(CheckEntry, prefix: new HarmonyMethod(typeof(StoryCheckpoint), nameof(EntryPrefix)));
        harmony.Patch(Interact, prefix: new HarmonyMethod(typeof(StoryCheckpoint), nameof(InteractionPrefix)));
        harmony.Patch(typeof(FieldQuestObjectController).GetGameMethod("QuestUpdate", InstanceFlags, null, Type.EmptyTypes, null),
            prefix: new HarmonyMethod(typeof(StoryCheckpoint), nameof(QuestUpdatePrefix)));
        harmony.Patch(typeof(SimpleBalloonUI).GetGameMethod("SetTalk", InstanceFlags),
            prefix: new HarmonyMethod(typeof(StoryCheckpoint), nameof(TalkStartedPrefix)));
        harmony.Patch(typeof(SimpleBalloonUI).GetGameMethod("CloseProcess", InstanceFlags | BindingFlags.DeclaredOnly),
            postfix: new HarmonyMethod(typeof(StoryCheckpoint), nameof(TalkClosedPostfix)));
        harmony.Patch(typeof(TimelineSignalManager).GetGameMethod("Initialize", InstanceFlags),
            postfix: new HarmonyMethod(typeof(StoryCheckpoint), nameof(InitializeTimelinePostfix)));
        harmony.Patch(typeof(AppManager).GetGameMethod("AppReStart", InstanceFlags, null, Type.EmptyTypes, null),
            prefix: new HarmonyMethod(typeof(StoryCheckpoint), nameof(ResetSession)));
        StoryBattleRecovery.Install(harmony);
    }

    internal static void ResetSession()
    {
        ClearStoryStatus();
        Session++;
        ActiveInteraction = null;
        PendingCinemaCompletions = 0;
        RestoringComplete = false;
        WaitingForEntrySync = false;
        SynchronizedStory = false;
        CancellationTokenSource previous = SessionCancellation;
        SessionCancellation = new CancellationTokenSource();
        previous.Cancel();
        previous.Dispose();
    }

    private static Checkpoint NewCheckpoint(int packID, int questID, string phase, string timeline)
    {
        PackManager pack = FindUnitySingleton<PackManager>();
        GameFieldManager field = FindUnitySingleton<GameFieldManager>();
        return new Checkpoint
        {
            ID = Guid.NewGuid().ToString("N"),
            PackID = packID,
            QuestID = questID,
            Phase = phase,
            Timeline = timeline ?? "",
            Difficulty = pack?.GetCurrentPackQuest(questID)?.QuestLevel ?? BDNetwork.CommonPacket.GetPackInfo(packID)?.QuestLevel ?? 0,
            MapID = field?.CurrentMapInfo?.Id ?? 0
        };
    }

    private static IEnumerator Send(string operation, Checkpoint checkpoint, int session, Action<Response> completed)
    {
        if (operation is "begin" or "read")
        {
            while (session == Session && !ApplicationQuitting && PendingCinemaCompletions > 0) yield return null;
            if (session != Session || ApplicationQuitting) yield break;
        }
        var request = new Request
        {
            Operation = operation,
            ID = checkpoint?.ID ?? "",
            PackID = checkpoint?.PackID ?? 0,
            QuestID = checkpoint?.QuestID ?? 0,
            Difficulty = checkpoint?.Difficulty ?? 0,
            Phase = checkpoint?.Phase ?? "",
            Timeline = checkpoint?.Timeline ?? "",
            ObjectID = checkpoint?.ObjectID ?? 0,
            MapID = checkpoint?.MapID ?? 0,
            TalkGroupID = checkpoint?.TalkGroupID ?? 0
        };
        float delay = 0.5f;
        float deadline = Time.realtimeSinceStartup + 45f;
        using var cancellation = CancellationTokenSource.CreateLinkedTokenSource(SessionCancellation.Token);
        cancellation.CancelAfter(TimeSpan.FromSeconds(45));
        while (session == Session && !ApplicationQuitting)
        {
            Task<Response> task = null;
            try { task = RequestStoryCheckpoint(request, cancellation.Token); }
            catch (Exception ex) { Log?.LogWarning("Story checkpoint transport unavailable: " + ex.Message); }
            if (task != null)
            {
                while (!task.IsCompleted && session == Session && !ApplicationQuitting && Time.realtimeSinceStartup < deadline) yield return null;
                if (session != Session || ApplicationQuitting) yield break;
                if (task.Status == TaskStatus.RanToCompletion)
                {
                    if (task.Result.Conflict)
                    {
                        BeginSessionRecovery("unfinished story checkpoint requires synchronized entry");
                        yield break;
                    }
                    if (task.Result.RejectedStatus != 0)
                    {
                        ShowStoryFailure("server rejected " + operation + " checkpoint: HTTP " + task.Result.RejectedStatus);
                        yield break;
                    }
                    ClearStoryStatus();
                    completed(task.Result);
                    yield break;
                }
                if (task.IsFaulted) Log?.LogWarning("Story checkpoint retry: " + task.Exception.GetBaseException().Message);
            }
            if (Time.realtimeSinceStartup >= deadline || cancellation.IsCancellationRequested)
            {
                cancellation.Cancel();
                ShowStoryFailure("server did not acknowledge " + operation + " checkpoint within 45 seconds");
                yield break;
            }
            ShowStoryStatus();
            yield return new WaitForSecondsRealtime(delay);
            delay = Math.Min(delay * 2f, 5f);
        }
    }

    private static bool CinemaPrefix(TimelinePlayManager __instance, object[] __args)
    {
        if (InvokingNative == StartCinema || ServerRoot == null || !EstablishedGameSession || (ETimelinePlayType)__args[7] != ETimelinePlayType.Quest || __instance.IsPlayCinemaTimeline)
            return true;
        if (IsStoryReplay) return true;
        int questID = (int)__args[2];
        string timeline = (string)__args[1];
        if (questID <= 0 || string.IsNullOrEmpty(timeline)) return true;
        QuestDTO quest = QuestInfo.GetQuest(questID);
        string phase = timeline == quest?.CompleteCinemaName ? "complete" : timeline == quest?.AcceptCinemaName ? "accept" : null;
        if (phase == null) return true;
        Checkpoint checkpoint = NewCheckpoint((int)__args[0], questID, phase, timeline);
        object[] args = (object[])__args.Clone();
        __instance.IsPlayCinemaTimeline = true;
        StartStoryCoroutine(BeginCinema(__instance, args, checkpoint, Session));
        return false;
    }

    private static IEnumerator BeginCinema(TimelinePlayManager timeline, object[] args, Checkpoint checkpoint, int session)
    {
        Response response = null;
        yield return Send("begin", checkpoint, session, value => response = value);
        if (session != Session || timeline == null || response?.Pending == null) yield break;
        checkpoint = response.Pending;
        var originalEnd = args[6] as Action;
        args[6] = new Action(() =>
        {
            if (session != Session) return;
            PendingCinemaCompletions++;
            // Native gate continuations read the live TimelineSignalManager before its destruction.
            originalEnd?.Invoke();
            if (session == Session)
                StartStoryCoroutine(Complete(checkpoint, session, () => PendingCinemaCompletions--));
        });
        timeline.IsPlayCinemaTimeline = false;
        Invoke(StartCinema, timeline, args);
        Log?.LogInfo("Story checkpoint started: pack=" + checkpoint.PackID + " quest=" + checkpoint.QuestID + " phase=" + checkpoint.Phase);
    }

    private static IEnumerator Complete(Checkpoint checkpoint, int session, Action completed)
    {
        bool acknowledged = false;
        yield return Send("complete", checkpoint, session, _ => acknowledged = true);
        if (session == Session && acknowledged) completed?.Invoke();
    }

    private static bool EntryPrefix(GameFieldManager __instance)
    {
        if (InvokingNative == CheckEntry || ServerRoot == null || !EstablishedGameSession || IsStoryReplay) return true;
        // The guild entry owns its intro callback and restarts an unfinished
        // cinema through CinemaPrefix, without accepting or clearing a quest.
        if (FindUnitySingleton<PackManager>()?.IsGuildRaidPack() == true) return true;
        if (WaitingForEntrySync) return false;
        WaitingForEntrySync = true;
        FindUnitySingleton<TimelinePlayManager>().IsPlayCinemaTimeline = true;
        StartStoryCoroutine(RestoreEntry(__instance, Session));
        return false;
    }

    private static void GuildIntroPostfix(ref IEnumerator __result)
    {
        if (ServerRoot != null && EstablishedGameSession)
            __result = RestoreGuildIntro(__result, Session);
    }

    private static IEnumerator RestoreGuildIntro(IEnumerator native, int session)
    {
        Response response = null;
        yield return Send("read", null, session, value => response = value);
        if (session != Session || response == null) yield break;
        Checkpoint pending = response.Pending;
        if (pending != null)
        {
            PackManager pack = FindUnitySingleton<PackManager>();
            if (pack?.IsGuildRaidPack() != true || pending.PackID != pack.PlayingPackID || pending.Phase != "accept")
            {
                ShowStoryFailure("pending checkpoint does not match the guild introduction");
                yield break;
            }
            // The native end callback writes this before server confirmation.
            // A pending checkpoint takes precedence over that local flag.
            SetLocalInt.Invoke(null, [string.Format(CultureInfo.InvariantCulture, "guild_raid_timeline_{0}", BDNetwork.CommonPacket.UserInfo.OwnerIndex), 0, true]);
        }
        yield return native;
    }

    private static IEnumerator RestoreEntry(GameFieldManager field, int session)
    {
        Response response = null;
        yield return Send("read", null, session, value => response = value);
        if (session != Session || field == null) yield break;
        if (response == null) yield break;
        WaitingForEntrySync = false;
        FindUnitySingleton<TimelinePlayManager>().IsPlayCinemaTimeline = false;
        Checkpoint checkpoint = response?.Pending;
        if (checkpoint == null)
        {
            Invoke(CheckEntry, field, null);
            yield break;
        }
        PackManager pack = FindUnitySingleton<PackManager>();
        if (pack == null || pack.PlayingPackID != checkpoint.PackID)
        {
            ShowStoryFailure("pending story checkpoint does not match the synchronized entry pack");
            yield break;
        }
        SynchronizedStory = true;
        bool finished = false;
        if (checkpoint.Phase == "interaction")
        {
            if (checkpoint.MapID > 0 && field.CurrentMapInfo.Id != checkpoint.MapID)
                yield return FindUnitySingleton<TimelinePlayManager>().MoveMapToTimelineDependOnScene(checkpoint.MapID, null, false);
            FieldQuestObjectController target = null;
            foreach (FieldQuestObjectController candidate in UnityEngine.Object.FindObjectsOfType<FieldQuestObjectController>())
                if (candidate.FieldQuestObjectDTO?.Id == checkpoint.ObjectID) { target = candidate; break; }
            if (target == null)
            {
                ShowStoryFailure("pending story interaction object is absent in the synchronized field");
                yield break;
            }
            yield return BeginInteraction(target, checkpoint, session, () => finished = true);
        }
        else
        {
            TimelinePlayManager timeline = FindUnitySingleton<TimelinePlayManager>();
            RestoringComplete = checkpoint.Phase == "complete";
            timeline.SavePosMapDataStartingCinemaTimeline(field.CurrentMapInfo.Id, field.PlayerController.transform.position);
            timeline.StartCinemaTimelineReplay(ETimelinePlayType.Quest, checkpoint.PackID,
                checkpoint.MapID > 0 ? checkpoint.MapID : field.CurrentMapInfo.Id, checkpoint.Timeline, checkpoint.QuestID, 0, 0, null,
                () => StartStoryCoroutine(Complete(checkpoint, session, () =>
                {
                    if (checkpoint.Phase == "complete") Proto.Local.ClientLocalInfo.AddFinishQuestTimeline(checkpoint.PackID, checkpoint.QuestID);
                    if (checkpoint.Phase == "accept") FinishRecoveredAccept(field, checkpoint.QuestID);
                    finished = true;
                })), "");
        }
        while (!finished && session == Session) yield return null;
        RestoringComplete = false;
        SynchronizedStory = false;
        if (session == Session && field != null) Invoke(CheckEntry, field, null);
    }

    private static void FinishRecoveredAccept(GameFieldManager field, int questID)
    {
        foreach (Type nested in typeof(GameFieldManager).GetNestedTypes(BindingFlags.NonPublic))
        {
            MethodInfo end = nested.GetGameMethod("<StartFirstQuestCinema>g__EndCinema|2", InstanceFlags);
            if (end == null) continue;
            object continuation = Activator.CreateInstance(nested, true);
            nested.GetGameField("<>4__this", InstanceFlags).SetValue(continuation, field);
            nested.GetGameField("currentPackQuestId", InstanceFlags).SetValue(continuation, questID);
            end.Invoke(continuation, null);
            return;
        }
        throw new MissingMethodException("The native accepted-story entry continuation is unavailable");
    }

    private static void InitializeTimelinePostfix(TimelineSignalManager __instance)
    {
        if (RestoringComplete)
            __instance.GetType().GetGameField("isReplayTimeline", InstanceFlags)?.SetValue(__instance, true);
    }

    private static bool InteractionPrefix(FieldQuestObjectController __instance)
    {
        if (InvokingNative == Interact || ServerRoot == null || !EstablishedGameSession || __instance.PlayQuestId <= 0) return true;
        if (__instance.QuestObjectState != FieldQuestObjectController.EQuestObjectState.Idle || FindUnitySingleton<GameFieldManager>().IsBattlePlay) return true;
        object state = typeof(FieldQuestObjectController).GetGameProperty("State", InstanceFlags | BindingFlags.DeclaredOnly)?.GetValue(__instance);
        FieldInfo idle = state?.GetType().GetGameField("None", BindingFlags.Static | BindingFlags.Public);
        if (state != null && (idle == null || !state.Equals(idle.GetValue(null)))) return true;
        FieldQuestObjectDTO design = __instance.FieldQuestObjectDTO;
        if (design == null || (string.IsNullOrEmpty(design.QuestTimeline) && design.QuestSimpleTalkGroupId <= 0)) return true;
        if (string.IsNullOrEmpty(design.QuestTimeline) && !string.IsNullOrEmpty(QuestInfo.GetQuest(__instance.PlayQuestId)?.CompleteCinemaName)) return true;
        if (ActiveInteraction != null) return false;
        Checkpoint checkpoint = NewCheckpoint(FindUnitySingleton<PackManager>().PlayingPackID, __instance.PlayQuestId, "interaction", design.QuestTimeline);
        checkpoint.ObjectID = design.Id;
        checkpoint.TalkGroupID = design.QuestSimpleTalkGroupId;
        StartStoryCoroutine(BeginInteraction(__instance, checkpoint, Session, null));
        return false;
    }

    private static IEnumerator BeginInteraction(FieldQuestObjectController controller, Checkpoint checkpoint, int session, Action completed)
    {
        Response response = null;
        var interaction = new Interaction { Controller = controller, Checkpoint = checkpoint, Session = session, Completed = completed };
        ActiveInteraction = interaction;
        yield return Send("begin", checkpoint, session, value => response = value);
        if (session != Session || controller == null || response?.Pending == null) yield break;
        interaction.Checkpoint = response.Pending;
        Invoke(Interact, controller, null);
    }

    private static bool QuestUpdatePrefix(FieldQuestObjectController __instance)
    {
        Interaction active = ActiveInteraction;
        if (InvokingNative?.IsGameMethod("QuestUpdate") == true || active == null || active.Controller != __instance || active.Session != Session) return true;
        active.UpdateDeferred = true;
        if (active.Checkpoint.TalkGroupID <= 0 || active.TalkFinished) FinishInteraction(active);
        return false;
    }

    private static void TalkStartedPrefix(SimpleBalloonUI __instance, int __0)
    {
        Interaction active = ActiveInteraction;
        if (active != null && active.Session == Session && active.Checkpoint.TalkGroupID == __0)
            active.TalkUI = __instance;
    }

    private static void TalkClosedPostfix(SimpleBalloonUI __instance)
    {
        Interaction active = ActiveInteraction;
        if (active == null || active.Session != Session || !ReferenceEquals(active.TalkUI, __instance)) return;
        active.TalkFinished = true;
        if (active.UpdateDeferred) FinishInteraction(active);
    }

    private static void FinishInteraction(Interaction active)
    {
        if (active.CompletionScheduled) return;
        active.CompletionScheduled = true;
        StartStoryCoroutine(Complete(active.Checkpoint, active.Session, () =>
        {
            if (ActiveInteraction != active) return;
            ActiveInteraction = null;
            if (active.Controller == null)
            {
                BeginSessionRecovery("story interaction target unloaded before quest update");
                return;
            }
            Invoke(typeof(FieldQuestObjectController).GetGameMethod("QuestUpdate", InstanceFlags, null, Type.EmptyTypes, null), active.Controller, null);
            active.Completed?.Invoke();
        }));
    }

    private static void Invoke(MethodInfo method, object instance, object[] args)
    {
        MethodInfo previous = InvokingNative;
        InvokingNative = method;
        try { method.Invoke(instance, args); }
        finally { InvokingNative = previous; }
    }
}
