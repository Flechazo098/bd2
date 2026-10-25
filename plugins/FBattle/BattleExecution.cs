using System;
using System.Collections.Generic;
using System.Linq;
using Battle.BattleRecord;
using BD2.GameNames;
using HarmonyLib;
using Proto.Net;

namespace Bd2FBattle;

internal static class BattleExecution
{
    private static object preparedProcessor;
    private static bool forceBurst;

    internal static void Install(Harmony harmony)
    {
        harmony.Patch(Game.Method(() => BattlePlayLibrary.AutoSkillReserve(default, null)),
            prefix: new HarmonyMethod(typeof(BattleExecution), nameof(BeforeSkills)),
            finalizer: new HarmonyMethod(typeof(BattleExecution), nameof(AfterAutomatic)));
        harmony.Patch(Game.Method(() => BattlePlayLibrary.AutoBurstReserve(default, null)),
            prefix: new HarmonyMethod(typeof(BattleExecution), nameof(BeforeBursts)),
            finalizer: new HarmonyMethod(typeof(BattleExecution), nameof(AfterAutomatic)));
        harmony.Patch(Game.Method(() => BattlePlayLibrary.AutoBurstReserveForChar(null, null)),
            prefix: new HarmonyMethod(typeof(BattleExecution), nameof(BeforeCharacterBurst)),
            finalizer: new HarmonyMethod(typeof(BattleExecution), nameof(AfterAutomatic)));
        harmony.Patch(Game.Method<BattlePlayManager>(battle => battle.ProcessAutoBattleWhenEnterBattle(null)),
            postfix: new HarmonyMethod(typeof(BattleExecution), nameof(AfterReady)));
        harmony.Patch(Game.Getter(() => UserSettingInfo.IsAutoBattleBurst),
            prefix: new HarmonyMethod(typeof(BattleExecution), nameof(AutoBurstEnabled)));
        harmony.Patch(Game.Method<BattlePlayManager>(battle => battle.Retry()),
            prefix: new HarmonyMethod(typeof(BattleExecution), nameof(Reset)));
    }

    internal static void Reset() => preparedProcessor = null;

    private static bool AutoBurstEnabled(ref bool __result)
    {
        if (!forceBurst) return true;
        __result = true;
        return false;
    }

    private static void BeforeSkills(ETeamType __0, out PlanScope __state) => __state = CreateScope(__0, false);
    private static void BeforeBursts(ETeamType __0, out PlanScope __state) => __state = CreateScope(__0, true);

    private static void BeforeCharacterBurst(BattleCharData __0, out PlanScope __state) =>
        __state = __0 == null ? null : CreateScope(__0.TeamType, true);

    private static Exception AfterAutomatic(Exception __exception, PlanScope __state)
    {
        __state?.Dispose();
        return __exception;
    }

    private static bool TryPlan(out BattlePlayManager battle, out IList<BattleCharData> team, out Dictionary<long, ActionChoice> choices)
    {
        battle = BattlePlayManager.Instance;
        team = null;
        choices = null;
        if (battle == null || battle.BattleProcessor == null || battle.GetBattleTurn() > 1 || battle.IsRetryCharActionSetting ||
            battle.IsBasePvpMode() || battle.IsTotalWar() || battle.IsRoguelike || battle.IsMonsterHunt || battle.IsGuildRaidBoss() ||
            BattleRecordManager.IsReadyBattleRecord || !battle.IsBlueTurn()) return false;
        team = battle.GetTeamBattleCharSequenceList(ETeamType.TT_BLUE);
        return team != null && PresetPlans.TryActive(team.Where(character => !character.IsBorrowedSupporter).Select(character => character.CharInvenIndex), out choices);
    }

    private static PlanScope CreateScope(ETeamType teamType, bool burst)
    {
        if (teamType != ETeamType.TT_BLUE || !TryPlan(out _, out IList<BattleCharData> team, out Dictionary<long, ActionChoice> choices))
            return null;
        var scope = new PlanScope();
        try
        {
            foreach (BattleCharData character in team)
            {
                if (!choices.TryGetValue(character.CharInvenIndex, out ActionChoice choice) || character.IsBorrowedSupporter) continue;
                if (choice.CostumeIndex > 0)
                {
                    CostumeDBInfo costume = CostumePacket.GetCostumeDBInfo(choice.CostumeIndex);
                    if (costume == null) continue;
                    int allowed = Math.Min(choice.BurstLevel, costume.BurstLevel);
                    scope.Add(character, choice.CostumeIndex, burst ? allowed : 0);
                }
                else scope.Add(character, choice.CostumeIndex, 0);
            }
            return scope;
        }
        catch
        {
            scope.Dispose();
            throw;
        }
    }

    private static void AfterReady(BattlePlayManager __instance, BattleUIBase __0)
    {
        if (__0 == null || !TryPlan(out _, out _, out _) || ReferenceEquals(preparedProcessor, __instance.BattleProcessor)) return;
        preparedProcessor = __instance.BattleProcessor;
        try
        {
            BattlePlayLibrary.CancelSkillReserve(ETeamType.TT_BLUE, __0, false);
            BattlePlayLibrary.AutoSkillReserve(ETeamType.TT_BLUE, __0);
            forceBurst = true;
            BattlePlayLibrary.AutoBurstReserve(ETeamType.TT_BLUE, __0);
        }
        finally { forceBurst = false; }
    }

    private sealed class PlanScope : IDisposable
    {
        private readonly List<(BattleCharData Character, DeckCostumeSettingSeqInfo[] Sequence, int Slot)> previous = [];

        internal void Add(BattleCharData character, long costume, int burst)
        {
            previous.Add((character, character.DeckCostumeSettingSeqInfoList.ToArray(), character.UseSequenceSlotIndex));
            character.DeckCostumeSettingSeqInfoList.Clear();
            character.DeckCostumeSettingSeqInfoList.Add(new DeckCostumeSettingSeqInfo(costume, burst));
            character.UseSequenceSlotIndex = 0;
        }

        public void Dispose()
        {
            foreach ((BattleCharData character, DeckCostumeSettingSeqInfo[] sequence, int slot) in previous)
            {
                character.DeckCostumeSettingSeqInfoList.Clear();
                character.DeckCostumeSettingSeqInfoList.AddRange(sequence);
                character.UseSequenceSlotIndex = slot;
            }
            previous.Clear();
        }
    }
}
