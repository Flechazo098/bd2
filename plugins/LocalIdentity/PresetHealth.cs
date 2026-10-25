using System;
using System.Collections.Generic;
using BD2.GameNames;
using BDNetwork;
using BepInEx.Logging;
using HarmonyLib;
using Proto.Net;

namespace Bd2LocalIdentity;

internal static class PresetHealth
{
    internal static void Install(Harmony harmony, ManualLogSource logger)
    {
        // CommonPacket has already applied the authoritative CharInfo response
        // before this method starts replacing individual equipment slots.
        harmony.Patch(Game.Method(() => EquipmentPacket.RecvPresetUseEquipInfos(null)),
            prefix: new HarmonyMethod(typeof(PresetHealth), nameof(BeforeEquipment)),
            postfix: new HarmonyMethod(typeof(PresetHealth), nameof(AfterEquipment)));
        logger.LogInfo("Preset equipment preserves authoritative health after all slot changes");
    }

    private static void BeforeEquipment(IList<PresetUseEquipInfo> __0, out Dictionary<long, long> __state)
    {
        __state = [];
        if (__0 == null) return;
        foreach (PresetUseEquipInfo entry in __0)
        {
            Capture(__state, entry.CharInvenIndex);
            foreach (long index in entry.EquipInvenIndex)
            {
                EquipDBInfo item = EquipmentPacket.GetEquipment(index);
                if (item != null && item.UseChar > 0) Capture(__state, item.UseChar);
            }
        }
    }

    private static void Capture(Dictionary<long, long> health, long index)
    {
        CharDBInfo character = CommonPacket.GetCharInfo(index);
        if (character != null) health[index] = character.Hp;
    }

    private static void AfterEquipment(Dictionary<long, long> __state)
    {
        foreach (KeyValuePair<long, long> pair in __state)
        {
            CharDBInfo character = CommonPacket.GetCharInfo(pair.Key);
            if (character == null) continue;
            long maximum = (long)CharStatInfo.GetCharStat(ECharStatType.CST_HEALTH, pair.Key,
                out _, EquipmentPacket.EEquipmentGetType.EGT_NORMAL, 0);
            character.Hp = Math.Min(pair.Value, maximum);
            UIManager.DefaultUI?.RefreshCharHp(pair.Key);
        }
    }
}
