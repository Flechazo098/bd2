using System;
using System.Collections.Generic;
using System.Reflection;
using BD2.GameNames;
using HarmonyLib;
using Proto.Net;

namespace Bd2CashShop;

// BuffItem rewards unlock account stats rather than an inventory item. Native
// reward handling ignores them, so refresh the authoritative collection stats.
internal static class RewardBuffRefresh
{
    internal static void Install(Harmony harmony)
    {
        var target = typeof(DataManager).GetGameMethod("AddRewardInfoBundle",
            BindingFlags.Public | BindingFlags.NonPublic | BindingFlags.Static, null,
            new[] { typeof(RewardDBInfoBundle), typeof(List<ItemBaseInfo>).MakeByRefType(), typeof(bool) }, null);
        if (target == null) throw new MissingMethodException("DataManager.AddRewardInfoBundle");
        harmony.Patch(target, postfix: new HarmonyMethod(typeof(RewardBuffRefresh), nameof(Refresh)));
    }

    private static void Refresh(RewardDBInfoBundle __0, bool __2)
    {
        if (__0 == null || !__2) return;
        foreach (var item in __0.ViewItemInfo)
        {
            if (item.Type != (int)EElementType.BuffItem) continue;
            ContentsPacket.SendRefreshCollectionBuff(null);
            return;
        }
    }
}
