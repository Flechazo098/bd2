package calendar

import "bd2server/internal/server/schedule"

func (e *encoder) manifest(v Manifest) {
	e.str(v.Revision)
	e.str(v.GameVersion)
	e.str(v.GameDataVersion)
	e.count(len(v.Events))
	for _, item := range v.Events {
		e.event(item)
	}
	e.count(len(v.Gacha))
	for _, item := range v.Gacha {
		e.gacha(item)
	}
	e.count(len(v.StepUps))
	for _, item := range v.StepUps {
		e.gacha(item)
	}
	e.b(v.Regular != nil)
	if v.Regular != nil {
		e.regular(*v.Regular)
	}
	e.b(v.MonsterHunt != nil)
	if v.MonsterHunt != nil {
		e.monsterhunt(*v.MonsterHunt)
	}
	e.count(len(v.CashProducts))
	for _, item := range v.CashProducts {
		e.cashproduct(item)
	}
	e.count(len(v.EventHubs))
	for _, item := range v.EventHubs {
		e.eventhub(item)
	}
	e.count(len(v.MiniGameHubs))
	for _, item := range v.MiniGameHubs {
		e.minigamehub(item)
	}
}
func (d *decoder) manifest() Manifest {
	var v Manifest
	v.Revision = d.str()
	v.GameVersion = d.str()
	v.GameDataVersion = d.str()
	for n := d.count(); n > 0 && d.err == nil; n-- {
		v.Events = append(v.Events, d.event())
	}
	for n := d.count(); n > 0 && d.err == nil; n-- {
		v.Gacha = append(v.Gacha, d.gacha())
	}
	for n := d.count(); n > 0 && d.err == nil; n-- {
		v.StepUps = append(v.StepUps, d.gacha())
	}
	if d.b() {
		item := d.regular()
		v.Regular = &item
	}
	if d.b() {
		item := d.monsterhunt()
		v.MonsterHunt = &item
	}
	for n := d.count(); n > 0 && d.err == nil; n-- {
		v.CashProducts = append(v.CashProducts, d.cashproduct())
	}
	for n := d.count(); n > 0 && d.err == nil; n-- {
		v.EventHubs = append(v.EventHubs, d.eventhub())
	}
	for n := d.count(); n > 0 && d.err == nil; n-- {
		v.MiniGameHubs = append(v.MiniGameHubs, d.minigamehub())
	}
	return v
}
func (e *encoder) event(v Event) {
	e.u64(v.UID)
	e.u64(v.Type)
	e.u64(v.ID)
	e.u64(v.SubID)
	e.str(v.Start)
	e.str(v.End)
}
func (d *decoder) event() Event {
	var v Event
	v.UID = d.u64()
	v.Type = d.u64()
	v.ID = d.u64()
	v.SubID = d.u64()
	v.Start = d.str()
	v.End = d.str()
	return v
}
func (e *encoder) gacha(v Gacha) {
	e.u64(v.GroupID)
	e.str(v.Start)
	e.str(v.End)
	e.b(v.FreeCountBonus)
	e.b(v.CashCountBonus)
}
func (d *decoder) gacha() Gacha {
	var v Gacha
	v.GroupID = d.u64()
	v.Start = d.str()
	v.End = d.str()
	v.FreeCountBonus = d.b()
	v.CashCountBonus = d.b()
	return v
}
func (e *encoder) season(v Season) {
	e.u64(v.ID)
	e.str(v.Start)
	e.str(v.End)
	e.u64(v.RankRewardGroupID)
	e.b(v.Error)
	e.b(v.Return)
}
func (d *decoder) season() Season {
	var v Season
	v.ID = d.u64()
	v.Start = d.str()
	v.End = d.str()
	v.RankRewardGroupID = d.u64()
	v.Error = d.b()
	v.Return = d.b()
	return v
}
func (e *encoder) content(v Content) {
	e.u64(v.ID)
	e.season(v.Current)
	e.season(v.Next)
}
func (d *decoder) content() Content {
	var v Content
	v.ID = d.u64()
	v.Current = d.season()
	v.Next = d.season()
	return v
}
func (e *encoder) regular(v Regular) {
	e.u64(v.CalculateMilliseconds)
	e.count(len(v.Contents))
	for _, item := range v.Contents {
		e.content(item)
	}
	e.count(len(v.Regular))
	for _, item := range v.Regular {
		e.regularseason(item)
	}
}
func (d *decoder) regular() Regular {
	var v Regular
	v.CalculateMilliseconds = d.u64()
	for n := d.count(); n > 0 && d.err == nil; n-- {
		v.Contents = append(v.Contents, d.content())
	}
	for n := d.count(); n > 0 && d.err == nil; n-- {
		v.Regular = append(v.Regular, d.regularseason())
	}
	return v
}
func (e *encoder) hunt(v Hunt) {
	e.season(v.Season)
	e.u64(v.HuntID)
	e.u64(v.InfoOpenDay)
	e.str(v.CalculateEndAt)
	e.b(v.ErrorFlag)
	e.b(v.IndependentFlag)
	e.u64(v.RankRewardGroupID)
	e.count(len(v.CostumeBanIDs))
	for _, item := range v.CostumeBanIDs {
		e.u64(item)
	}
	e.count(len(v.BurstBanIDs))
	for _, item := range v.BurstBanIDs {
		e.u64(item)
	}
}
func (d *decoder) hunt() Hunt {
	var v Hunt
	v.Season = d.season()
	v.HuntID = d.u64()
	v.InfoOpenDay = d.u64()
	v.CalculateEndAt = d.str()
	v.ErrorFlag = d.b()
	v.IndependentFlag = d.b()
	v.RankRewardGroupID = d.u64()
	for n := d.count(); n > 0 && d.err == nil; n-- {
		v.CostumeBanIDs = append(v.CostumeBanIDs, d.u64())
	}
	for n := d.count(); n > 0 && d.err == nil; n-- {
		v.BurstBanIDs = append(v.BurstBanIDs, d.u64())
	}
	return v
}
func (e *encoder) hunthistory(v HuntHistory) {
	e.u64(v.Season)
	e.u64(v.HuntID)
	e.b(v.ErrorFlag)
	e.b(v.Hidden)
}
func (d *decoder) hunthistory() HuntHistory {
	var v HuntHistory
	v.Season = d.u64()
	v.HuntID = d.u64()
	v.ErrorFlag = d.b()
	v.Hidden = d.b()
	return v
}
func (e *encoder) monsterhunt(v MonsterHunt) {
	e.count(len(v.Seasons))
	for _, item := range v.Seasons {
		e.hunt(item)
	}
	e.u64(v.StartRegularSeason)
	e.count(len(v.History))
	for _, item := range v.History {
		e.hunthistory(item)
	}
}
func (d *decoder) monsterhunt() MonsterHunt {
	var v MonsterHunt
	for n := d.count(); n > 0 && d.err == nil; n-- {
		v.Seasons = append(v.Seasons, d.hunt())
	}
	v.StartRegularSeason = d.u64()
	for n := d.count(); n > 0 && d.err == nil; n-- {
		v.History = append(v.History, d.hunthistory())
	}
	return v
}
func (e *encoder) cashproduct(v CashProduct) {
	e.u64(v.GroupID)
	e.u64(v.ProductID)
	e.u64(v.SaleGroup)
	e.str(v.Start)
	e.str(v.End)
	e.u64(v.EndDelayMinutes)
	e.u64(v.EventIndex)
}
func (d *decoder) cashproduct() CashProduct {
	var v CashProduct
	v.GroupID = d.u64()
	v.ProductID = d.u64()
	v.SaleGroup = d.u64()
	v.Start = d.str()
	v.End = d.str()
	v.EndDelayMinutes = d.u64()
	v.EventIndex = d.u64()
	return v
}
func (e *encoder) hubsetting(v HubSetting) {
	e.u64(v.Slot)
	e.u64(v.ProgressType)
	e.count(len(v.EventUIDs))
	for _, item := range v.EventUIDs {
		e.u64(item)
	}
}
func (d *decoder) hubsetting() HubSetting {
	var v HubSetting
	v.Slot = d.u64()
	v.ProgressType = d.u64()
	for n := d.count(); n > 0 && d.err == nil; n-- {
		v.EventUIDs = append(v.EventUIDs, d.u64())
	}
	return v
}
func (e *encoder) eventhub(v EventHub) {
	e.u64(v.UID)
	e.u64(v.HubID)
	e.str(v.Start)
	e.str(v.PlayEnd)
	e.str(v.End)
	e.count(len(v.Settings))
	for _, item := range v.Settings {
		e.hubsetting(item)
	}
}
func (d *decoder) eventhub() EventHub {
	var v EventHub
	v.UID = d.u64()
	v.HubID = d.u64()
	v.Start = d.str()
	v.PlayEnd = d.str()
	v.End = d.str()
	for n := d.count(); n > 0 && d.err == nil; n-- {
		v.Settings = append(v.Settings, d.hubsetting())
	}
	return v
}
func (e *encoder) minigamehub(v MiniGameHub) {
	e.u64(v.Slot)
	e.u64(v.EventUID)
	e.u64(v.ProgressType)
}
func (d *decoder) minigamehub() MiniGameHub {
	var v MiniGameHub
	v.Slot = d.u64()
	v.EventUID = d.u64()
	v.ProgressType = d.u64()
	return v
}
func (e *encoder) regularseason(v schedule.RegularSeason) {
	e.u64(v.ContentID)
	e.u64(v.Season)
}
func (d *decoder) regularseason() schedule.RegularSeason {
	var v schedule.RegularSeason
	v.ContentID = d.u64()
	v.Season = d.u64()
	return v
}
