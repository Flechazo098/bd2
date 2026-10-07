package calendar

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/design/schedule"

	"bd2server/internal/server/domain/commerce/gacha"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/protocol/wire"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

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

// validateMiniHubBindings rejects invalid published routes at startup instead
// of letting a client open an incompatible prefab or use another activity UID.
func (s *Set) validateMiniHubBindings(design *gamedata.EventPlayCatalog) error {
	uid := make(map[uint64]events.Schedule)
	for _, v := range s.Events {
		if v.UID != 0 {
			uid[v.UID] = v
		}
	}
	value := func(row []byte, field int) uint64 { v, _, _ := wire.Varint(row, field); return v }
	for _, hub := range s.EventHubs {
		table, err := design.Row("PackEventHubTable", 14, hub.HubID)
		if err != nil {
			return err
		}
		if value(table, 13) != 1 {
			continue
		}
		if hub.UID == 0 {
			return fmt.Errorf("calendar: mini hub %d requires a nonzero UID", hub.HubID)
		}
		start, err := ParseTimestamp(hub.Start)
		if err != nil {
			return err
		}
		playEnd, err := ParseTimestamp(hub.PlayEnd)
		if err != nil {
			return err
		}
		end, err := ParseTimestamp(hub.End)
		if err != nil {
			return err
		}
		for _, binding := range hub.Settings {
			var slot []byte
			for _, row := range design.Rows("PackEventListTable", 6, hub.HubID) {
				if value(row, 11) != binding.Slot {
					continue
				}
				if slot != nil {
					return fmt.Errorf("calendar: mini hub %d slot %d is ambiguous in GameData", hub.HubID, binding.Slot)
				}
				slot = row
			}
			if slot == nil {
				return fmt.Errorf("calendar: mini hub %d slot %d missing GameData", hub.HubID, binding.Slot)
			}
			contentType, contentID := value(slot, 9), value(slot, 7)
			if binding.ProgressType != contentType {
				return fmt.Errorf("calendar: mini hub %d slot %d content type %d, want %d", hub.HubID, binding.Slot, binding.ProgressType, contentType)
			}
			eventType, supported := gamedata.MiniHubEventType(contentType)
			// Mini stories and NPC quizzes have their own slot UID namespace,
			// distinct from Define_EventType (13 there means bingo). Their group
			// and availability derive from this static slot and the hub window.
			if contentType == 13 || contentType == 14 {
				if len(binding.EventUIDs) != 1 || binding.EventUIDs[0] == 0 {
					return fmt.Errorf("calendar: mini hub %d slot %d requires one local content UID", hub.HubID, binding.Slot)
				}
				if _, collision := uid[binding.EventUIDs[0]]; collision {
					return fmt.Errorf("calendar: mini hub %d slot %d content UID collides with global event", hub.HubID, binding.Slot)
				}
				if value(slot, 4) != 0 {
					return fmt.Errorf("calendar: mini hub %d story/quiz slot has invalid end type", hub.HubID)
				}
				continue
			}
			if !supported {
				return fmt.Errorf("calendar: mini hub %d slot %d content type %d has no supported scheduled route", hub.HubID, binding.Slot, contentType)
			}
			endType := value(slot, 4)
			if endType > 1 {
				return fmt.Errorf("calendar: mini hub %d slot %d unsupported end type %d", hub.HubID, binding.Slot, endType)
			}
			matches := 0
			for _, id := range binding.EventUIDs {
				child, ok := uid[id]
				if !ok || child.Type != eventType || child.ID != contentID || child.SubID != 0 {
					return fmt.Errorf("calendar: mini hub %d slot %d references incompatible event UID %d", hub.HubID, binding.Slot, id)
				}
				childStart, childEnd := child.Start, child.End
				if childStart < int64(start) {
					childStart = int64(start)
				}
				if childEnd > int64(end) {
					childEnd = int64(end)
				}
				// Project policy: EndDateType=0 closes the slot at PlayEnd;
				// EndDateType=1 allows it through the final hub End window.
				if endType == 0 && childEnd > int64(playEnd) {
					childEnd = int64(playEnd)
				}
				if childStart < childEnd {
					matches++
				}
			}
			if matches != 1 {
				return fmt.Errorf("calendar: mini hub %d slot %d has %d schedules in its window, want one", hub.HubID, binding.Slot, matches)
			}
		}
	}
	return nil
}

// LoadDirectory validates the whole directory before returning any calendar.
// Every calendar file must use the .bd2schedule suffix.
// Directories and symlinks are
// rejected so an accidentally omitted calendar never produces a partial set.
func LoadDirectory(dir, gameVersion, gameDataVersion string) (*Set, error) {
	entries, e := os.ReadDir(dir)
	if e != nil {
		return nil, fmt.Errorf("calendar: read directory: %w", e)
	}
	set := &Set{GachaSeed: &gacha.ScheduleSeed{ClientVersion: gameVersion}}
	seen := map[string]bool{}
	claim := func(key string) error {
		if seen[key] {
			return fmt.Errorf("duplicate calendar identity %s", key)
		}
		seen[key] = true
		return nil
	}
	files := 0
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".bd2schedule") {
			return nil, fmt.Errorf("calendar: unsupported entry %s", entry.Name())
		}
		info, e := entry.Info()
		if e != nil {
			return nil, e
		}
		if info.Size() > MaxFileSize {
			return nil, fmt.Errorf("calendar: %s: file size limit exceeded", entry.Name())
		}
		raw, e := os.ReadFile(filepath.Join(dir, entry.Name()))
		if e != nil {
			return nil, e
		}
		m, e := UnmarshalBinary(raw)
		if e != nil {
			return nil, fmt.Errorf("calendar: %s: %w", entry.Name(), e)
		}
		if m.SchemaVersion != 1 || strings.TrimSpace(m.Revision) == "" || m.GameVersion != gameVersion || m.GameDataVersion != gameDataVersion || gameVersion == "" || gameDataVersion == "" {
			return nil, fmt.Errorf("calendar: %s: incompatible schema/version or missing revision", entry.Name())
		}
		if e = merge(set, m, claim); e != nil {
			return nil, fmt.Errorf("calendar: %s: %w", entry.Name(), e)
		}
		set.Revisions = append(set.Revisions, m.Revision)
		files++
	}
	if files == 0 {
		return nil, fmt.Errorf("calendar: empty directory")
	}
	sort.Slice(set.EventHubs, func(i, j int) bool { return set.EventHubs[i].UID < set.EventHubs[j].UID })
	sort.Slice(set.MiniGameHubs, func(i, j int) bool { return set.MiniGameHubs[i].Slot < set.MiniGameHubs[j].Slot })
	sort.Slice(set.Events, func(i, j int) bool {
		a, b := set.Events[i], set.Events[j]
		if a.UID != b.UID {
			return a.UID < b.UID
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.SubID < b.SubID
	})
	sortWindows := func(w []gacha.ScheduleWindow) {
		sort.Slice(w, func(i, j int) bool {
			if w[i].GroupID != w[j].GroupID {
				return w[i].GroupID < w[j].GroupID
			}
			return w[i].StartTime < w[j].StartTime
		})
	}
	sortWindows(set.GachaSeed.Schedules)
	sortWindows(set.GachaSeed.StepUps)
	if e = set.GachaSeed.Validate(gameVersion); e != nil {
		return nil, e
	}
	if set.RegularService != nil {
		sort.Slice(set.RegularService.Contents, func(i, j int) bool { return set.RegularService.Contents[i].ID < set.RegularService.Contents[j].ID })
		sort.Slice(set.RegularService.Regular, func(i, j int) bool {
			return set.RegularService.Regular[i].ContentID < set.RegularService.Regular[j].ContentID
		})
		if e = set.RegularService.Validate(); e != nil {
			return nil, e
		}
	}
	sort.Slice(set.CashProducts, func(i, j int) bool {
		a, b := set.CashProducts[i], set.CashProducts[j]
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		if a.ProductID != b.ProductID {
			return a.ProductID < b.ProductID
		}
		return a.SaleGroup < b.SaleGroup
	})
	if set.MonsterHunt != nil {
		sort.Slice(set.MonsterHunt.Seasons, func(i, j int) bool {
			return set.MonsterHunt.Seasons[i].Season.ID < set.MonsterHunt.Seasons[j].Season.ID
		})
		sort.Slice(set.MonsterHunt.History, func(i, j int) bool { return set.MonsterHunt.History[i].Season < set.MonsterHunt.History[j].Season })
	}
	return set, nil
}

// ValidateDesign checks playable identities against their domain's installed
// GameData. Announcement-only types 3/6/14/15/16/18 have no local gameplay
// design resolver and receive only the manifest's protocol/format validation.
func (s *Set) ValidateDesign(root, version string) error {
	cash, e := gamedata.LoadCashCatalog(root, version)
	if e != nil {
		return e
	}
	keys := map[gamedata.CashProductKey]bool{}
	for _, v := range cash.Products {
		keys[v.Key] = true
	}
	for _, v := range s.CashProducts {
		if !keys[gamedata.CashProductKey{GroupID: v.GroupID, ProductID: v.ProductID, SaleGroup: v.SaleGroup}] {
			return fmt.Errorf("calendar: cash product %d/%d/%d missing GameData", v.GroupID, v.ProductID, v.SaleGroup)
		}
	}
	if s.MonsterHunt != nil {
		ids, e := gamedata.LoadMonsterHuntIDs(root, version)
		if e != nil {
			return e
		}
		for _, v := range s.MonsterHunt.Seasons {
			if !ids[v.HuntID] {
				return fmt.Errorf("calendar: active/playable hunt %d missing GameData", v.HuntID)
			}
		}
	}
	play, e := gamedata.LoadEventPlayCatalog(root, version)
	if e != nil {
		return e
	}
	tasks, e := gamedata.LoadEventTasksDesign(root, version)
	if e != nil {
		return e
	}
	exchange, e := gamedata.LoadEventExchangeCatalog(root, version)
	if e != nil {
		return e
	}
	actions, e := gamedata.LoadEventActionsDesign(root, version)
	if e != nil {
		return e
	}
	packs, e := gamedata.LoadCalendarPackIDs(root, version)
	if e != nil {
		return e
	}
	for _, h := range s.EventHubs {
		if _, e = play.Row("PackEventHubTable", 14, h.HubID); e != nil {
			return fmt.Errorf("calendar: hub %d: %w", h.HubID, e)
		}
	}
	if err := s.validateMiniHubBindings(play); err != nil {
		return err
	}
	uid := map[uint64]events.Schedule{}
	for _, v := range s.Events {
		if v.UID != 0 {
			uid[v.UID] = v
		}
	}
	for _, h := range s.MiniGameHubs {
		v, ok := uid[h.EventUID]
		if !ok || v.Type != 11 {
			return fmt.Errorf("calendar: mini game slot %d references non-minigame event %d", h.Slot, h.EventUID)
		}
		if _, e := play.Row("PackEventMiniGameTable", 8, v.ID); e != nil {
			return e
		}

	}
	for _, v := range s.Events {
		valid := true
		var err error
		switch v.Type {
		case 0:
			_, valid = tasks.Attendance[v.ID]
		case 1:
			valid = false
			for k := range tasks.LimitRewards {
				if k[0] == v.ID {
					valid = true
					break
				}
			}
		case 4:
			_, valid = tasks.MissionGroups[v.ID]
		case 5:
			_, valid = tasks.Passes[v.ID]
		case 7:
			_, valid = exchange.Groups[v.ID]
		case 8:
			valid = packs[v.ID]
		case 9:
			_, err = play.Row("PackEventBattleGroupTable", 3, v.ID)
		case 10:
			_, err = play.Row("PackEventStoryGroupTable", 1, v.ID)
		case 11:
			_, err = play.Row("PackEventMiniGameTable", 8, v.ID)
		case 12, 13, 17, 19:
			_, err = gamedata.LoadEventGame(root, version, v.Type, v.ID)
		case 20:
			_, valid = actions.Row("TacticsBingoGroupTable", 3, v.ID)
		case 21:
			_, valid = actions.Row("FieldSpawnEventTable", 5, v.ID)
		case 22:
			_, valid = actions.Row("FireworksTable", 4, v.ID)
		case 23:
			_, valid = actions.Row("VotingEventTable", 6, v.ID)
		case 24:
			_, valid = actions.Row("FriendshipSpecialEpisodeTable", 5, v.ID)
		case 25:
			id := v.SubID
			if id == 0 {
				id = v.ID
			}
			_, valid = actions.Row("CafeteriaEventTable", 5, id)
		}
		if !valid || err != nil {
			return fmt.Errorf("calendar: event uid %d type %d id %d missing GameData: %v", v.UID, v.Type, v.ID, err)
		}
	}
	return nil
}

func (e *encoder) u64(v uint64) {
	if e.room(8) {
		e.data = binary.LittleEndian.AppendUint64(e.data, v)
	}
}

func (e *encoder) u32(v uint32) {
	if e.room(4) {
		e.data = binary.LittleEndian.AppendUint32(e.data, v)
	}
}

func (e *encoder) str(v string) {
	if e.err != nil {
		return
	}
	if len(v) > maxString || !utf8.ValidString(v) {
		e.err = fmt.Errorf("calendar: invalid/oversized UTF8 string")
		return
	}
	if !e.room(4 + len(v)) {
		return
	}
	e.u32(uint32(len(v)))
	e.data = append(e.data, v...)
}

func (e *encoder) count(n int) {
	if e.err != nil {
		return
	}
	e.rows += uint64(n)
	if n > maxRows || e.rows > maxRows {
		e.err = fmt.Errorf("calendar: row limit exceeded")
		return
	}
	e.u32(uint32(n))
}

func (d *decoder) u64() uint64 {
	v := d.take(8)
	if len(v) != 8 {
		return 0
	}
	return binary.LittleEndian.Uint64(v)
}

func (d *decoder) u32() uint32 {
	v := d.take(4)
	if len(v) != 4 {
		return 0
	}
	return binary.LittleEndian.Uint32(v)
}

func (d *decoder) str() string {
	n := d.u32()
	if n > maxString {
		d.err = fmt.Errorf("calendar: string limit exceeded")
		return ""
	}
	v := d.take(int(n))
	if !utf8.Valid(v) {
		d.err = fmt.Errorf("calendar: invalid UTF8")
	}
	return string(v)
}

func (d *decoder) count() int {
	n := d.u32()
	d.rows += uint64(n)
	if n > maxRows || d.rows > maxRows || uint64(n) > uint64(len(d.data)-d.pos) {
		d.err = fmt.Errorf("calendar: row count limit/truncation")
		return 0
	}
	return int(n)
}

func MarshalBinary(m Manifest) ([]byte, error) {
	if m.SchemaVersion != 1 {
		return nil, fmt.Errorf("calendar: unsupported schema")
	}
	e := &encoder{}
	e.manifest(m)
	if e.err != nil {
		return nil, e.err
	}
	if len(e.data) > MaxFileSize-headerSize {
		return nil, fmt.Errorf("calendar: file size limit exceeded")
	}
	out := append([]byte(nil), magic...)
	out = binary.LittleEndian.AppendUint16(out, 1)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(e.data)))
	sum := sha256.Sum256(e.data)
	out = append(out, sum[:]...)
	return append(out, e.data...), nil
}

func UnmarshalBinary(raw []byte) (Manifest, error) {
	var m Manifest
	if len(raw) < headerSize || len(raw) > MaxFileSize {
		return m, fmt.Errorf("calendar: invalid file size")
	}
	if !bytes.Equal(raw[:8], magic) || binary.LittleEndian.Uint16(raw[8:10]) != 1 {
		return m, fmt.Errorf("calendar: unsupported magic/format")
	}
	n := binary.LittleEndian.Uint32(raw[10:14])
	if uint64(n) != uint64(len(raw)-headerSize) {
		return m, fmt.Errorf("calendar: payload length mismatch")
	}
	payload := raw[headerSize:]
	sum := sha256.Sum256(payload)
	if !bytes.Equal(raw[14:46], sum[:]) {
		return m, fmt.Errorf("calendar: checksum mismatch")
	}
	d := &decoder{data: payload}
	m = d.manifest()
	if d.err != nil {
		return Manifest{}, d.err
	}
	if d.pos != len(payload) {
		return Manifest{}, fmt.Errorf("calendar: trailing payload")
	}
	m.SchemaVersion = 1
	return m, nil
}
