package feature

// standaloneDefaults are native local-server defaults for optional query
// features. An empty protobuf means the local account currently has no rows or
// no active server schedule; it is not copied from a captured response.
var standaloneDefaults = map[string]int{
	"/EventScheduleInfo":   163,
	"/LoginEvent":          0,
	"/BalanceVersionCheck": 186,
	"/ChargeCostInfo":      123,
	"/EquipInfo":           34,
	"/HuntDispatchInfo":    189,
	"/EventMissionInfo":    127,
	"/MissionInfo":         118,
	"/EventRewardHistory":  0,
	"/PackEventStoryInfo":  220,
	"/PackEventBattleInfo": 214,
	"/FishingItemInfo":     459,
	"/MonsterHuntDeckInfo": 263,
	"/Attendance":          0,
	"/AttendanceInfo":      0,
	// No PvP decks, battle history or one-time reward claims have been saved.
	// Their repeated-only protobufs encode these account states as empty, and
	// the client's callbacks continue the arena-lobby entry sequence.
	"/PvpBattleDeckInfo":       87,
	"/PvpBattleHistory":        97,
	"/PvpBattleOnceRewardInfo": 277,
	"/TodayQuestInfo":          64,
	"/FieldDeckInfo":           273,
	"/HuntingGroundInfoList":   387,
	"/FriendRecommend":         211,
	"/SupporterStatus":         438,
	"/SupporterBattleInfo":     439,
	"/InnOpen":                 109,
}

// commandSuccess contains idempotent local mutations whose response type is
// canonically empty. Domain persistence can be added without changing their
// protocol contract. Core progress commands are handled by session first.
var commandSuccess = map[string]int{
	"/UpdateAgeGate":                0,
	"/SaveFieldCharControlDeckType": 288,
	"/ActiveMap":                    0,
	"/FieldDeckSave":                274,
	"/CostumeUse":                   41,
	"/BattleExit":                   388,
}
