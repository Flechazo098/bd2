// Package conditions holds the MissionTable condition semantics shared by the
// regular (daily/weekly) and the scheduled event mission services. Values are
// Define_MissionConditionType protocol enums, never mission IDs.
package conditions

import "slices"

// Define_MissionConditionType values recorded by the server.
const (
	Connect                       uint64 = 0   // McConnect
	TodayQuestClear               uint64 = 1   // McTodayQuestClear
	MonsterKill                   uint64 = 2   // McMonsterKill
	CollectionGet                 uint64 = 3   // McCollectionGet
	FieldRewardMonsterCatch       uint64 = 5   // McFieldRewardMonsterCatch
	TalentSkillUse                uint64 = 6   // McTalentSkillUse
	HuntingBattle                 uint64 = 7   // McHuntingBattle
	GachaBuy                      uint64 = 9   // McGachaBuy
	PackBattle                    uint64 = 10  // McPackBattle
	ItemCurrencyUse               uint64 = 11  // McItemCurrencyUse
	ResourceUse                   uint64 = 12  // McResourceUse
	EquipmentUpgrade              uint64 = 14  // McEquipmentUpgrade
	BattleModeStartTry            uint64 = 18  // McBattleModeStartTry
	BattleStartWithUniqueChar     uint64 = 19  // McBattleModeStartTryWithUniqueCharId
	EquipMakeGrade                uint64 = 20  // McEquipMakeGrade
	EquipSmelting                 uint64 = 21  // McEquipSmelting (gear option refinement)
	HuntDispatch                  uint64 = 23  // McHuntDispatch
	SkywayPositionGroupClear      uint64 = 24  // McSkyWayDungeonPositionGroupLevelClear
	CostumePotentialNodeKind      uint64 = 26  // McCostumePotentialActivateNodeKind
	PvpRankGoal                   uint64 = 27  // McPvpRankIdGoal
	TalentSkillUpgrade            uint64 = 29  // McTalentSkillUpgrade
	SameGroupMissionComplete      uint64 = 30  // McSamGroupIdMissionUpdateCompleteCount
	OtherGroupMissionComplete     uint64 = 31  // McOtherGroupIdMissionUpdateCompleteCount
	ItemCurrencyGet               uint64 = 32  // McItemCurrencyGet
	CafeteriaCumulativeReward     uint64 = 33  // McCafeteriaCumulativeReward
	MyRoomVisit                   uint64 = 34  // McMyRoomVisit
	MyRoomLike                    uint64 = 36  // McMyRoomLike
	ArcadeMenuVisit               uint64 = 37  // McArcadeMenuVisit
	PackQuestIDClear              uint64 = 38  // McPackQuestIdClear
	LobbyItemSettingChange        uint64 = 39  // McLobbyItemSettingChange
	DatingEpisodeClear            uint64 = 40  // McDatingEpisodeClear
	GuildLobbyUIEnter             uint64 = 41  // McGuildLobbyUiEnter
	CharImprintLevelUp            uint64 = 42  // McCharImprintLevelUp
	CharAwakeActive               uint64 = 43  // McCharAwakeActive
	EquipMakingUIEnter            uint64 = 44  // McEquipMakingUiEnter
	EquipUpgradeUIEnter           uint64 = 45  // McEquipUpgradeUiEnter
	MonsterHuntBattleWithTraining uint64 = 46  // McTryToBattleInMonsterHuntIncludeTraining
	EvilCastleRageTowerClear      uint64 = 47  // McEvilCastleRageTowerClear
	EvilCastleEnvyTowerClear      uint64 = 48  // McEvilCastleEnvyTowerClear
	CostumeUpgrade                uint64 = 104 // McCostumeUpgrade
	GachaGroupTry                 uint64 = 105 // McTryToGachaToTargetGachaGroup
	EnterPackInField              uint64 = 204 // McEnterPackInField
	CharTargetLevelUp             uint64 = 207 // McCharTargetLevelUp
	CharTargetGradeUp             uint64 = 208 // McCharTargetGradeUp
	CharScoutToTargetGrade        uint64 = 209 // McTryToCharScoutToTargetGrade
	BuyItemInShop                 uint64 = 211 // McBuyItemINShop
	EquipmentUpgradeToLevel       uint64 = 213 // McSuccessEquipmentUpgradeToTargetLevel
	EquipEquipmentItem            uint64 = 214 // McTryToEquipEquipmentItem
	ChangeEquipmentItem           uint64 = 215 // McTryToChangeEquipmentItem
	BreakEquipmentItem            uint64 = 216 // McTryToBreakEquipmentItem
	SmeltEquipmentItem            uint64 = 217 // McTryToSmeltEquipmentItem
	UseAutoHunting                uint64 = 224 // McTryToUseAutoHunting
	ChangeCostumeInBattle         uint64 = 226 // McTryToChangeCostumeInBattle
	SkywayGroupBattle             uint64 = 227 // McTryToBattleInSkywayDungeonGroupId
	EvilCastleFloorBattle         uint64 = 228 // McTryToBattleInEvilCastleToTargetFloor
	EvilCastleFloorClear          uint64 = 229 // McClearInEvilCastleToTargetFloor
	MonsterHuntBattle             uint64 = 230 // McTryToBattleInMonsterHunt
	PlatformConnectOrLogin        uint64 = 231 // McPlatformConnectOrLogin
	EvilCastleGreedTowerClear     uint64 = 233 // McEvilCastleGreedTowerClear
	MonsterHuntWin                uint64 = 240 // McWinToBattleInMonsterHunt
	TotalWarTryCount              uint64 = 280 // McTotalWarTryCount
	TotalWarResultScore           uint64 = 281 // McTotalWarResultScore
	MyRoomVisitAnotherUser        uint64 = 312 // McMyRoomVisitAnotherUser
	EventFieldMiniGamePlay        uint64 = 315 // McEventFieldMiniGamePlay
	MiniGameSurvivalCharLevel     uint64 = 316 // McMiniGameSurvivalCharLevel
	MiniGameSurvivalLifeTime      uint64 = 317 // McMiniGameSurvivalLifeTime
	MiniGameSurvivalSkillMaxCount uint64 = 318 // McMiniGameSurvivalSkillMaxLevelCount
	MiniGameSurvivalKillCount     uint64 = 319 // McMiniGameSurvivalKillMonsterCount
	MiniGameSurvivalMapClear      uint64 = 320 // McMiniGameSurvivalMapClear
	MiniGameSurvivalSkillMax      uint64 = 321 // McMiniGameSurvivalSpecificSkillMaxLevel
	MiniGameDefenseWaveClear      uint64 = 322 // McMiniGameDefenseWaveClear
	MiniGameActionMonster         uint64 = 323 // McMiniGameActionMonster
	FishingShopBuy                uint64 = 337 // McFishingShopBuy
	FishingMultiParticipation     uint64 = 338 // McFishingMultiParticipation
	FishingVoyageStart            uint64 = 340 // McFishingVoyageStart
	FishingFishBiteEnd            uint64 = 341 // McFishingFishBiteEnd
	FishingBoatUpgrade            uint64 = 342 // McFishingBoatUpgrade
	MiniGamePlay                  uint64 = 343 // McMiniGamePlay
	SquareReward                  uint64 = 344 // McSquareReward
	ColosseumBattleWin            uint64 = 345 // McColosseumBattleWin
	TacticsBingoBattleWin         uint64 = 346 // McTacticsBingoBattleWin
	TacticsBingoLineClear         uint64 = 347 // McTacticsBingoLineClear
	TacticsBingoAllClear          uint64 = 348 // McTacticsBingoAllClear
	FieldSpawnEventReward         uint64 = 349 // McFieldSpawnEventReward
	FriendshipTargetLevelUp       uint64 = 350 // McFriendshipTargetLevelUp
	FriendshipCostumeLevelUp      uint64 = 351 // McFriendshipCostumeLevelUp
	PackManNormalGameOver         uint64 = 360 // McMiniGamePackManNormalGameOver
	PackManNormalClear            uint64 = 361 // McMiniGamePackManNormalClear
	PackManChallengeGameOver      uint64 = 362 // McMiniGamePackManChallengeGameOver
	PackManChallengeTimeoutScore  uint64 = 363 // McMiniGamePackManChallengeTimeoutScore
	PackManPlayCount              uint64 = 364 // McMiniGamePackManPlayCount
)

// ClientReported lists the UI-only conditions the native client reports
// itself through MissionUpdate (MissionPassPacket.UpdateMission call sites);
// no server request observes these actions.
var ClientReported = []uint64{ArcadeMenuVisit, LobbyItemSettingChange, GuildLobbyUIEnter, EquipMakingUIEnter, EquipUpgradeUIEnter, UseAutoHunting, ChangeCostumeInBattle, MyRoomVisitAnotherUser}

// Spec is one MissionTable condition.
type Spec struct {
	Type, SubType, SubTypeMoreCheck uint64
	// Params is ConditionSubTypeParams; empty or all zero accepts any value.
	Params []uint64
}

// Event is one authoritative occurrence. Sub is compared with ConditionSubType
// and Param with ConditionSubTypeParams.
type Event struct {
	Type, Sub, Param, Count uint64
}

// comparison selects between exact and at-least matching.
type comparison uint8

const (
	// compareDefault follows GameData: zero SubType accepts any sub; otherwise
	// IsConditionSubTypeMoreCheck selects at-least, and Params are membership.
	compareDefault comparison = iota
	compareExact
	compareAtLeast
)

// conditionRule overrides the GameData comparison for one condition type.
// A forced comparison also applies to SubType zero instead of treating it as
// a wildcard.
type conditionRule struct {
	sub   comparison // compareDefault keeps SubTypeMoreCheck
	param comparison // compareDefault is membership; compareAtLeast uses Params[0]
}

// conditionRules covers types whose mission text compares at least while
// IsConditionSubTypeMoreCheck is zero, and McFieldSpawnEventReward, whose
// zero SubType names the normal spawn rather than any sub.
var conditionRules = map[uint64]conditionRule{
	EquipMakeGrade:                {sub: compareAtLeast},
	TotalWarResultScore:           {sub: compareAtLeast},
	MiniGameSurvivalCharLevel:     {sub: compareAtLeast},
	MiniGameSurvivalSkillMaxCount: {sub: compareAtLeast},
	MiniGameSurvivalKillCount:     {sub: compareAtLeast},
	MiniGameDefenseWaveClear:      {sub: compareAtLeast},
	PackManChallengeTimeoutScore:  {sub: compareAtLeast},
	MiniGameSurvivalLifeTime:      {param: compareAtLeast},
	FieldSpawnEventReward:         {sub: compareExact},
}

// Matches reports whether e advances a mission with this condition.
func (s Spec) Matches(e Event) bool {
	return s.Type == e.Type && s.SubTypeReached(e.Sub) && s.ParamAccepts(e.Param)
}

// AnyParam reports whether ConditionSubTypeParams places no restriction.
// GameData writes "no restriction" as either no value or a single zero.
func AnyParam(params []uint64) bool {
	for _, p := range params {
		if p != 0 {
			return false
		}
	}
	return true
}

// SubTypeReached applies the ConditionSubType comparison to an achieved value.
func (s Spec) SubTypeReached(value uint64) bool {
	switch conditionRules[s.Type].sub {
	case compareAtLeast:
		return value >= s.SubType
	case compareExact:
		return value == s.SubType
	}
	if s.SubType == 0 {
		return true
	}
	if s.SubTypeMoreCheck != 0 {
		return value >= s.SubType
	}
	return value == s.SubType
}

// ParamAccepts applies the ConditionSubTypeParams filter to one value.
func (s Spec) ParamAccepts(value uint64) bool {
	if AnyParam(s.Params) {
		return true
	}
	if conditionRules[s.Type].param == compareAtLeast {
		return value >= s.Params[0]
	}
	return slices.Contains(s.Params, value)
}

// Target is ConditionValue; a zero value still requires one occurrence.
func Target(value uint64) uint64 {
	return max(value, 1)
}
