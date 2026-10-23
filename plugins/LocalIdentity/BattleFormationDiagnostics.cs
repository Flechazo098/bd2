using System;
using System.Collections.Generic;
using System.Reflection;
using System.Text;
using BD2.GameNames;
using BDNetwork;
using BepInEx.Logging;
using HarmonyLib;
using Proto.Net;

namespace Bd2LocalIdentity;

internal static class BattleFormationDiagnostics
{
    private static readonly FieldInfo BattleUI = typeof(BattleGridTouchManager).GetGameField("_battleUI", BindingFlags.Instance | BindingFlags.NonPublic);
    private static ManualLogSource Log;
    private static bool MissingGridReported;

    internal static void Install(Harmony harmony, ManualLogSource logger)
    {
        if (BattleUI == null) throw new MissingFieldException(typeof(BattleGridTouchManager).FullName, "_battleUI");
        Log = logger;
        harmony.Patch(Game.Method(() => CommonPacket.RecvDeckCharAutoReviveResponse(null, 0, 0)),
            postfix: new HarmonyMethod(typeof(BattleFormationDiagnostics), nameof(RecoveryApplied)));
        harmony.Patch(Game.Method<BattlePlayManager>(battle => battle.InitBattle(default, default)),
            prefix: new HarmonyMethod(typeof(BattleFormationDiagnostics), nameof(Reset)));
        harmony.Patch(Game.Method<BattlePlayData>(data => data.SetPlayData(0, null, false, true)),
            prefix: new HarmonyMethod(typeof(BattleFormationDiagnostics), nameof(BeforeSet)),
            postfix: new HarmonyMethod(typeof(BattleFormationDiagnostics), nameof(AfterSet)));
        harmony.Patch(Game.Method<BattleGridTouchManager>(touch => touch.OnTouchBegan(null)),
            prefix: new HarmonyMethod(typeof(BattleFormationDiagnostics), nameof(TouchBegan)));
    }

    private static void Reset() => MissingGridReported = false;

    private static void RecoveryApplied(byte[] __0, int __2, bool __result)
    {
        if (!ClientRouting.IsActive || __2 != 0 || !__result) return;
        DeckCharAutoReviveResponse response = DeckCharAutoReviveResponse.Parser.ParseFrom(__0);
        Log.LogInfo("Battle formation recovery applied: type=" + response.AutoReviveCharType + " deck=" + DeckHealth());
    }

    private static void BeforeSet(BattlePlayData __instance, int __0, out string __state)
    {
        __state = null;
        if (!ClientRouting.IsActive || __instance.TeamType != ETeamType.TT_BLUE) return;
        BattlePlayManager battle = BattlePlayManager.Instance;
        __state = "mode=" + battle?.BattleMode + " engine_v2=" + battle?.BattleProcessor?.IsActiveBattleMachine +
            " expected_grid=" + __0 + " before_count=" + __instance.BattleCharDataList?.Count + " deck=" + DeckHealth();
    }

    private static void AfterSet(BattlePlayData __instance, string __state)
    {
        if (__state == null) return;
        Log.LogInfo("Battle formation initialized: " + __state + " after_count=" + __instance.BattleCharDataList?.Count);
    }

    private static void TouchBegan(BattleGridTouchManager __instance, BattleGridTouchData __0)
    {
        if (!ClientRouting.IsActive || MissingGridReported || __0 == null || __0.DragBattleCharData != null) return;
        BattlePlayManager battle = BattlePlayManager.Instance;
        var ui = BattleUI.GetValue(__instance) as BattleUIBase;
        if (battle == null || !battle.IsBattleReadyState || ui == null || !ui.IsActive || !ui.CanTouchUICondition()) return;
        List<BattleCharData> team = battle.GetTeamBattleCharList(__0.TeamType);
        if (team != null && __0.GridIndex >= 0 && __0.GridIndex < team.Count && team[__0.GridIndex] != null) return;
        MissingGridReported = true;
        Log.LogWarning("Battle formation touch has no grid data: team=" + __0.TeamType + " grid=" + __0.GridIndex +
            " count=" + (team == null ? "null" : team.Count.ToString(System.Globalization.CultureInfo.InvariantCulture)) +
            " state=" + battle.CurrentBattleState + " mode=" + battle.BattleMode +
            " engine_v2=" + battle.BattleProcessor?.IsActiveBattleMachine + " deck=" + DeckHealth());
    }

    private static string DeckHealth()
    {
        var text = new StringBuilder();
        if (CommonPacket.DeckInfoList == null) return "null";
        foreach (DeckDBInfo entry in CommonPacket.DeckInfoList)
        {
            if (text.Length != 0) text.Append(',');
            if (entry == null)
            {
                text.Append("null");
                continue;
            }
            CharDBInfo character = CommonPacket.GetCharInfo(entry.CharInvenIndex);
            text.Append(entry.CharInvenIndex).Append('@').Append(entry.Position).Append(':');
            if (character == null) text.Append("missing");
            else text.Append(character.Hp);
        }
        return text.ToString();
    }
}
