using System;
using System.Collections.Generic;
using System.Reflection;
using BD2.GameNames;
using HarmonyLib;
using Proto.Net;

namespace Bd2LocalIdentity;

internal static class BattlePreparation
{
    private static readonly FieldInfo RetryDeck = typeof(BattlePlayManager).GetGameField("_retryDeckInfoList", BindingFlags.Instance | BindingFlags.NonPublic);

    internal static void Install(Harmony harmony)
    {
        if (RetryDeck == null) throw new MissingFieldException(typeof(BattlePlayManager).FullName, "_retryDeckInfoList");
        harmony.Patch(Game.Method<BattlePlayManager>(battle => battle.Retry()),
            prefix: new HarmonyMethod(typeof(BattlePreparation), nameof(BeforeRetry)));
    }

    private static void BeforeRetry(BattlePlayManager __instance)
    {
        if (!ClientRouting.IsActive || !__instance.IsSupportContent() || __instance.GetBattleTurn() != 1 || __instance.IsCompleteBattleStart) return;
        var retry = (List<DeckDBInfo>)RetryDeck.GetValue(__instance);
        // A rejected supporter borrow can retry before BattleStart creates the native snapshot.
        retry.Clear();
        foreach (DeckDBInfo entry in __instance.GetDeckInfoList(false, false))
            retry.Add(entry.Clone());
    }
}
