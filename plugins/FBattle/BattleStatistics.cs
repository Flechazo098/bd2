using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Runtime.CompilerServices;
using BD2.GameNames;
using Google.Protobuf;
using HarmonyLib;
using Proto.Net;

namespace Bd2FBattle;

// Reports how much of each PVP team remained so the server's skill rating can
// weigh the victory margin. The attacker is always the local blue team.
internal static class BattleStatistics
{
    private const int Field = 100;
    private static readonly ConditionalWeakTable<IMessage, object> reported = [];
    private static readonly BindingFlags All = BindingFlags.Public | BindingFlags.NonPublic | BindingFlags.Static | BindingFlags.Instance;

    internal static void Install(Harmony harmony)
    {
        MethodInfo send = typeof(BDNetwork.NetworkManager).GetMethods(All)
            .Single(method => method.IsGameMethod("Send") && method.GetParameters().Length == 6 && method.GetParameters()[0].ParameterType == typeof(IMessage));
        harmony.Patch(send, prefix: new HarmonyMethod(typeof(BattleStatistics), nameof(BeforeSend)));
    }

    private static void BeforeSend(IMessage __0)
    {
        if (__0 is not (PvpBattleEndRequest or ColosseumBattleEndRequest) || reported.TryGetValue(__0, out _)) return;
        try
        {
            BattlePlayManager battle = BattlePlayManager.Instance;
            BattleProcessor processor = battle?.BattleProcessor;
            if (processor == null) return;
            (long attackerHp, long attackerMax, int attackerAlive) = Team(battle, processor, ETeamType.TT_BLUE);
            (long defenderHp, long defenderMax, int defenderAlive) = Team(battle, processor, ETeamType.TT_RED);
            if (attackerMax <= 0 || defenderMax <= 0) return;
            using var payload = new MemoryStream();
            using (var output = new CodedOutputStream(payload, true))
            {
                Write(output, 1, attackerHp);
                Write(output, 2, attackerMax);
                Write(output, 3, defenderHp);
                Write(output, 4, defenderMax);
                Write(output, 5, attackerAlive);
                Write(output, 6, defenderAlive);
                Write(output, 7, Math.Max(0, battle.GetBattleTurn()));
            }
            using var envelope = new MemoryStream();
            using (var output = new CodedOutputStream(envelope, true))
            {
                output.WriteTag(Field, WireFormat.WireType.LengthDelimited);
                output.WriteBytes(ByteString.CopyFrom(payload.ToArray()));
            }
            __0.MergeFrom(envelope.ToArray());
            reported.Add(__0, new object());
        }
        catch (Exception error)
        {
            // The official result must still reach the server without statistics.
            Plugin.Log.LogError("PVP battle statistics failed: " + error);
        }
    }

    private static (long Health, long MaxHealth, int Alive) Team(BattlePlayManager battle, BattleProcessor processor, ETeamType team)
    {
        var seen = new HashSet<long>();
        long health = 0, maxHealth = 0;
        int alive = 0;
        IEnumerable<BattleCharData> members = battle.GetTeamBattleCharList(team)
            .Concat(processor.DeckOutObjectList.Where(character => character.TeamType == team));
        foreach (BattleCharData character in members)
        {
            if (character == null || character.CharID <= 0 || !seen.Add(character.UniqueIndex)) continue;
            health += character.GetCurrentHealth();
            maxHealth += character.GetMaxHealth();
            if (!character.IsDie()) alive++;
        }
        return (Math.Min(health, maxHealth), maxHealth, alive);
    }

    private static void Write(CodedOutputStream output, int field, long value)
    {
        if (value <= 0) return;
        output.WriteTag(field, WireFormat.WireType.Varint);
        output.WriteInt64(value);
    }
}
