using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Runtime.CompilerServices;
using BDNetwork;
using gamfs;
using BD2.GameNames;
using BepInEx.Logging;
using Google.Protobuf;
using HarmonyLib;
using Proto.Net;

namespace Bd2LocalIdentity;

internal static class BattleCheckpoint
{
    private static ManualLogSource log;
    private static readonly ConditionalWeakTable<IMessage, object> uploaded = [];
    private static readonly BindingFlags All = BindingFlags.Public | BindingFlags.NonPublic | BindingFlags.Static | BindingFlags.Instance;

    internal static void Install(Harmony harmony, ManualLogSource logger)
    {
        log = logger;
        MethodInfo send = typeof(BDNetwork.NetworkManager).GetMethods(All)
            .Single(method => method.IsGameMethod("Send") && method.GetParameters().Length == 6 && method.GetParameters()[0].ParameterType == typeof(IMessage));
        harmony.Patch(send, prefix: new HarmonyMethod(typeof(BattleCheckpoint), nameof(BeforeSend)));

    }

    private static void BeforeSend(IMessage __0)
    {
        if (uploaded.TryGetValue(__0, out _)) return;
        if (__0 is PvpBattleMatchingRequest matching)
        {
            try
            {
                BattlePlayManager manager = BattlePlayManager.Instance;
                if (manager == null) return;
                PackManager pack = Singleton<PackManager>.Instance;
                var recommendation = new RecommendDeckInfoRequest { BattleMode = 6, PackId = pack.PlayingPackID, BattleIndex = manager.CurrentBattleDeckInfo?.Id ?? 0 };
                var packType = (Define_PackType)pack.EnterPackDTO.PackType;
                if (packType == Define_PackType.PackGuildRaid || packType == Define_PackType.PackHidden) recommendation.PackId = 0;
                using var envelope = new MemoryStream();
                using (var output = new CodedOutputStream(envelope, true))
                {
                    output.WriteTag(103, WireFormat.WireType.LengthDelimited);
                    output.WriteBytes(ByteString.CopyFrom(recommendation.ToByteArray()));
                }
                matching.MergeFrom(envelope.ToArray());
                uploaded.Add(matching, new object());
            }
            catch (Exception exception)
            {
                log.LogError("PVP recommendation context serialization failed: " + exception);
                throw;
            }
            return;
        }
        if (__0 is not BattleStartRequest request) return;
        try
        {
            BattlePlayManager manager = BattlePlayManager.Instance;
            BattleProcessor processor = manager?.BattleProcessor;
            if (processor == null) return;
            var result = new BattleResultDBInfo();
            foreach (BattleCharData character in manager.GetTeamBattleCharList(ETeamType.TT_RED).OrderBy(character => character.CharSequence))
                if (character.CharID > 0) result.RedCharInfo.Add(processor.CreateCharBuffProtoData(character));
            foreach (BattleCharData character in manager.GetTeamBattleCharList(ETeamType.TT_BLUE).OrderBy(character => character.CharSequence))
                if (character.CharID > 0) result.BlueCharInfo.Add(processor.CreateCharBuffProtoData(character));
            foreach (BattleCharData character in processor.DeckOutObjectList)
                if (character.CharID > 0)
                {
                    Google.Protobuf.Collections.RepeatedField<BattleCharDBInfo> target = character.TeamType == ETeamType.TT_RED ? result.RedDeckOutCharInfo : result.BlueDeckOutCharInfo;
                    target.Add(processor.CreateCharBuffProtoData(character));
                }
            foreach (BattleCharData character in processor.MonsterHuntBeforeTeamData)
                if (character.CharID > 0 && !result.BlueDeckOutCharInfo.Any(saved => saved.UniqueIndex == character.UniqueIndex))
                    result.BlueDeckOutCharInfo.Add(processor.CreateCharBuffProtoData(character));
            BattleProcessorGridInfo grid = processor.BattleProcessorGridInfo;
            foreach (BattleProcessorGridItem cell in grid._redGridItemList.Concat(grid._blueGridItemList))
                foreach (BattleProcessorGridTypeItem item in cell._gridItemList)
                    result.GridItem.Add(new BattleGridTypeItemDBInfo { TeamType = (int)cell._teamType, GridIndex = cell._gridIndex, BuffId = item.BuffId, IsInfinite = item.IsInfinite, BuffTurn = item.BuffTurn });
            result.BattleStatisticsInfo = processor._battleStatisticsInfo.GetBattleStatisticsDBInfo();
            if (request.GolemInfo != null) result.GolemInfo = request.GolemInfo.Clone();
            var checkpoint = new BattleResumeResponse
            {
                ModeInfo = new BattleResumeCommonInfo { BattleMode = manager.GetBattleMode(), BattleIndex = request.BattleIndex, RoundCount = manager.GetBattleTurn() - 1 },
                MonsterId = manager.BattleEnterMonsterId,
                Level = processor.BattleLevel,
                BlueTeamSkillBubbleCount = manager.GetPlayData(ETeamType.TT_BLUE).SkillBubbleCount,
                RedTeamSkillBubbleCount = manager.GetPlayData(ETeamType.TT_RED).SkillBubbleCount,
                MonsterHuntTeam = manager.MonsterHuntTeam,
                CumulativeSummonCount = processor.CumulativeSummonCount,
                BattleResult = Convert.ToBase64String(result.ToByteArray()),
                EngineType = (Define_BattleEngineType)1,
                ContinueRedTeamSkillBubbleCount = manager.ContinueEnemySkillBubblePoint,
                ContinuePhaseGroupId = manager.ContinuePhaseInfo.Item1,
                ContinuePhaseId = manager.ContinuePhaseInfo.Item2,
            };
            var continued = new BattleResultDBInfo();
            foreach (BattleCharData character in manager.ContinueBlueBattleCharDatas)
                if (character.CharID > 0) continued.BlueCharInfo.Add(processor.CreateCharBuffProtoData(character));
            foreach (BattleCharData character in manager.ContinueEnemyBattleCharDatas)
                if (character.CharID > 0) continued.RedCharInfo.Add(processor.CreateCharBuffProtoData(character));
            if (continued.BlueCharInfo.Count != 0 || continued.RedCharInfo.Count != 0)
                checkpoint.ContinueBattleResult = Convert.ToBase64String(continued.ToByteArray());
            foreach (KeyValuePair<long, BattleCharDBInfo> pair in processor.UseBattleCharDic)
                checkpoint.UseBattleCharDic.Add(pair.Key, Convert.ToBase64String(pair.Value.ToByteArray()));
            foreach (BattleCharData character in manager.GetTeamBattleCharList(ETeamType.TT_BLUE))
                if (character.CharInvenIndex > 0) checkpoint.DeckInIndexList.Add(character.CharInvenIndex);
            foreach (BattleCharData character in processor.DeckOutObjectList)
                if (character.TeamType == ETeamType.TT_BLUE && character.CharInvenIndex > 0) checkpoint.DeckOutIndexList.Add(character.CharInvenIndex);
            Dictionary<RelicInfo.ETiming, List<RelicItem>> relics = processor.RelicCaster?.GetRelicRuler<RelicRuleBattle>()?.BattleRelicData;
            if (relics != null)
                foreach (RelicItem relic in relics.Values.SelectMany(items => items).Distinct())
                    checkpoint.RelicItemList.Add(new BattleRelicItemInfo { RelicId = relic._relicId, RelicBuffId = relic._relicBuffId, IsApplied = relic.IsApplied(processor) });
            if (processor.MonsterHuntBeforeTeamData.Count != 0)
            {
                var previous = new BattleResultDBInfo();
                var equipment = new EquipInfoResponse();
                var costumes = new CostumeInfoResponse();
                var awake = new CharAwakeInfoResponse();
                var buffs = new AllCharRefreshResponse();
                foreach (BattleCharData character in processor.MonsterHuntBeforeTeamData)
                {
                    previous.BlueCharInfo.Add(processor.CreateCharBuffProtoData(character));
                    if (character.UseEquips != null) equipment.EquipInfo.Add(character.UseEquips);
                    if (character.AwakeInfo != null) awake.CharAwakeInfo.Add(character.AwakeInfo.Clone());
                    costumes.CostumeInfo.Add(processor.GetCostumeDBInfoListByCharId(ETeamType.TT_BLUE, character.CharID, character.SupportOwnerIndex));
                }
                buffs.BuffStatInfo.Add(processor._blueTeamCollectionBuffInfos);
                checkpoint.MonsterHuntBeforeTeamInfo.Add(new MonsterHuntTeamProtoInfo
                {
                    BlueCharProto = Convert.ToBase64String(previous.ToByteArray()),
                    BlueEquipProto = Convert.ToBase64String(equipment.ToByteArray()),
                    BlueBuffProto = Convert.ToBase64String(buffs.ToByteArray()),
                    BlueCostumeProto = Convert.ToBase64String(costumes.ToByteArray()),
                    BlueAwakeProto = Convert.ToBase64String(awake.ToByteArray())
                });
            }
            BattleCharData body = manager.GetMonsterHuntBodyCharData();
            if (body != null)
            {
                checkpoint.BossCurrentMonsterHp = body.GetCurrentHealth();
                checkpoint.BossStartMonsterHp = body.GetMaxHealth();
                List<BossSkillData> history = body.SkillHistory;
                if (history != null)
                    foreach (BossSkillData skill in history)
                        checkpoint.MonsterHuntSkillHistory.Add(new Proto.Net.MonsterHuntSkillData { CostumeId = skill.CostumeId, ConditionId = skill.ConditionId, PartsTableId = skill.PartsTableId, UseCount = skill.UseCount, LimitType = (Define_EMonsterHuntSkillLimits)skill.LimitType });
            }
            if (manager.IsMonsterHunt && MonsterHuntPacket.MonsterHuntUserInfo != null)
            {
                checkpoint.Season = MonsterHuntPacket.MonsterHuntUserInfo.Season;
                checkpoint.BossStartMonsterHp = MonsterHuntPacket.MonsterHuntUserInfo.StartHp;
            }
            if (manager.IsGuildRaidBoss())
            {
                checkpoint.Level = GuildRaidPacket.CurrentTargetBossLevel;
                foreach (GuildRaidSupporterDeckDBInfo deck in GuildRaidPacket.CurrentTargetRaidSupporterDBList)
                {
                    GuildPacket.GuildSupporterData saved = GuildPacket.GetSupporterData(deck.OwnerIndex, deck.SlotIndex) ?? throw new InvalidOperationException("Missing entered raid supporter");
                    SupporterDeckInfo owner = checkpoint.SupporterInfo.FirstOrDefault(info => info.OwnerIndex == deck.OwnerIndex);
                    if (owner == null) { owner = new SupporterDeckInfo { OwnerIndex = deck.OwnerIndex, UserId = saved.supporterInfo.UserId }; checkpoint.SupporterInfo.Add(owner); }
                    owner.SupporterSlots.Add(new SupporterSlotInfo { OwnerIndex = deck.OwnerIndex, SlotIndex = deck.SlotIndex, BattleUseCount = saved.supporterInfo.BattleUseCount, SupporterCharInfo = saved.supporterInfo.SupporterCharInfoProto });
                }
                checkpoint.SupporterCharInfo.Add(GuildRaidPacket.SupporterCharInfos);
                FieldInfo season = typeof(GuildRaidPacket.GuildRaidMainInfo).GetGameField("season", All);
                checkpoint.Season = (int)season.GetValue(GuildRaidPacket.MainInfo);
            }
            if (manager.BorrowedSupporterDeckInfo != null) checkpoint.SupporterInfo.Add(manager.BorrowedSupporterDeckInfo.Clone());
            checkpoint.CanRequestFriend = manager.CanRequestFriend;
            var recommendation = new RecommendDeckInfoRequest { BattleMode = (int)manager.GetBattleMode(), PackId = Singleton<PackManager>.Instance.PlayingPackID, BattleIndex = manager.CurrentBattleDeckInfo?.Id ?? 0 };
            var packType = (Define_PackType)Singleton<PackManager>.Instance.EnterPackDTO.PackType;
            if (packType == Define_PackType.PackGuildRaid || packType == Define_PackType.PackHidden) recommendation.PackId = 0;
            if (manager.IsMonsterHunt) { recommendation.Level = manager.MonsterHuntLevel; recommendation.TeamCountType = manager.MonsterHuntTeamCount; }
            if (manager.IsGuildRaidBoss()) recommendation.Level = GuildRaidPacket.CurrentTargetBossLevel;
            byte[] payload;
            using (var stream = new MemoryStream())
            {
                using (var output = new CodedOutputStream(stream, true))
                {
                    checkpoint.WriteTo(output);
                    output.WriteTag(101, WireFormat.WireType.Varint);
                    output.WriteBool(manager.IsPhaseBattle ? manager.IsBlueTurn() : manager.GetBattleTurn() % 2 != 0);
                    output.WriteTag(103, WireFormat.WireType.LengthDelimited);
                    output.WriteBytes(ByteString.CopyFrom(recommendation.ToByteArray()));
                }
                payload = stream.ToArray();
            }
            using var envelope = new MemoryStream();
            using (var output = new CodedOutputStream(envelope, true))
            {
                output.WriteTag(100, WireFormat.WireType.LengthDelimited);
                output.WriteBytes(ByteString.CopyFrom(payload));
            }
            request.MergeFrom(envelope.ToArray());
            uploaded.Add(request, new object());
        }
        catch (Exception exception)
        {
            log.LogError("Battle checkpoint serialization failed: " + exception);
            throw;
        }
    }

}
