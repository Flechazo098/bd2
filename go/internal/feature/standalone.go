package feature

// standaloneDefaults are native local-server defaults for optional query
// features. An empty protobuf means the local account currently has no rows or
// no active server schedule; it is not copied from a captured response.
var standaloneDefaults = map[string]int{
	"/EventScheduleInfo":     163,
	"/LoginEvent":            0,
	"/BalanceVersionCheck":   186,
	"/ChargeCostInfo":        123,
	"/EquipInfo":             34,
	"/AchievementInfo":       166,
	"/HuntDispatchInfo":      189,
	"/EventMissionInfo":      127,
	"/MissionInfo":           118,
	"/EventRewardHistory":    0,
	"/PackEventStoryInfo":    220,
	"/PackEventBattleInfo":   214,
	"/FishingItemInfo":       459,
	"/MonsterHuntDeckInfo":   263,
	"/Attendance":            0,
	"/AttendanceInfo":        0,
	"/PackPreviewInfo":       104,
	"/TodayQuestInfo":        64,
	"/WaypointInfo":          31,
	"/FieldDeckInfo":         273,
	"/HuntingGroundInfoList": 387,
	"/FriendRecommend":       211,
	"/SupporterStatus":       438,
	"/MailHistoryInfo":       138,
	"/SupporterBattleInfo":   439,
	"/InnOpen":               109,
}

// commandSuccess contains idempotent local mutations whose response type is
// canonically empty. Domain persistence can be added without changing their
// protocol contract. Core progress commands are handled by session first.
var commandSuccess = map[string]int{
	"/UpdateAgeGate":                0,
	"/SaveFieldCharControlDeckType": 288,
	"/WaypointSave":                 32,
	"/ActiveMap":                    0,
	"/MasterTitleInfoUpdate":        592,
	"/FieldDeckSave":                274,
	"/CostumeUse":                   41,
	"/BattleExit":                   388,
}
