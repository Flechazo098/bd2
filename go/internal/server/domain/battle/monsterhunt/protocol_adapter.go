package monsterhunt

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/protocol/staticdata"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func validateRequest(req []byte) error {
	return wire.Walk(req, func(f wire.Field) error {
		if f.Type == 0 {
			v, _ := binary.Uvarint(f.Value)
			if v > math.MaxInt32 {
				return fmt.Errorf("monsterhunt: integer outside protocol range")
			}
		}
		return nil
	})
}

func (s *Service) ValidatePack(ctx command.Context, pack int, req []byte) error {

	id, e := scalar(req, 6)
	if e != nil {
		return e
	}
	d, e := s.load(id)
	if e != nil {
		return e
	}
	if pack <= 0 || uint64(pack) != d.PackID {
		return fmt.Errorf("monsterhunt: hunt does not belong to current pack")
	}
	return nil
}

func (s *Service) rankWire(u user) []byte {
	b := wire.AppendVarint(nil, 7, 1)
	b = wire.AppendDouble(b, 8, s.score(u))
	return wire.AppendDouble(b, 10, 100)
}

func (s *Service) grant(ctx command.Context, identity string, rewards []gamedata.BattleReward) ([]byte, error) {
	if s.rewardGrant != nil {
		rs := make([]gamedata.Reward, len(rewards))
		for i, r := range rewards {
			rs[i] = gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count} //nolint:staticcheck // S1016
		}
		return s.rewardGrant(ctx, identity, rs)
	}
	var currency []gamedata.Reward
	var stack []gamedata.BattleReward
	for _, r := range rewards {
		switch r.Type {
		case 2, 3, 4, 12, 20:
			currency = append(currency, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count}) //nolint:staticcheck // S1016
		default:
			stack = append(stack, r)
		}
	}
	if _, e := s.wallet.GrantQuestOnce(ctx, identity+":currency", currency); e != nil {
		return nil, e
	}
	items, e := s.inventory.GrantOnce(ctx, identity+":items", stack)
	if e != nil {
		return nil, e
	}
	if len(items) == 0 {
		items = s.inventory.GrantedItems(identity + ":items")
	}
	var bundle []byte
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(item))
		v := wire.AppendVarint(nil, 2, item.ID)
		v = wire.AppendVarint(v, 3, item.Type)
		v = wire.AppendVarint(v, 4, item.Count)
		bundle = wire.AppendBytes(bundle, 6, v)
	}
	for _, r := range currency {
		v := wire.AppendVarint(nil, 3, r.Type)
		v = wire.AppendVarint(v, 4, r.Count)
		bundle = wire.AppendBytes(bundle, 1, v)
	}
	return bundle, nil
}

func (s *Service) presetBindings(ctx command.Context, p []byte) (map[uint64]uint64, []assets.PresetEquipmentBinding, error) {
	assignments := map[uint64]uint64{}
	var bindings []assets.PresetEquipmentBinding
	entries, e := messages(p, 5)
	if e != nil {
		return nil, nil, e
	}
	for _, entry := range entries {
		base, _, e := wire.Bytes(entry, 1)
		if e != nil {
			return nil, nil, e
		}
		index, _ := scalar(base, 1)
		costume, _ := scalar(entry, 2)
		if _, duplicate := assignments[index]; duplicate {
			return nil, nil, fmt.Errorf("monsterhunt: duplicate preset character")
		}
		assignments[index] = costume
		if costume != 0 && s.collection != nil {
			c, found := s.collection.CostumeByIndex(costume)
			if !found || c.UseChar != index {
				return nil, nil, fmt.Errorf("monsterhunt: preset costume not owned by character")
			}
		}
		binding := assets.PresetEquipmentBinding{CharacterIndex: index, Equipment: make([]uint64, 5)}
		equipment, e := messages(entry, 3)
		if e != nil {
			return nil, nil, e
		}
		seen := map[uint64]bool{}
		for _, item := range equipment {
			t, _ := scalar(item, 1)
			id, _ := scalar(item, 2)
			if t >= 5 || seen[t] {
				return nil, nil, fmt.Errorf("monsterhunt: invalid preset equipment slot")
			}
			seen[t] = true
			binding.Equipment[t] = id
		}
		bindings = append(bindings, binding)
	}
	if len(bindings) > 0 && s.equipment != nil {
		if e = s.equipment.ValidatePresetEquipment(ctx, bindings); e != nil {
			return nil, nil, e
		}
	}
	return assignments, bindings, nil
}

func (s *Service) applyPreset(ctx command.Context, p []byte) ([]byte, error) {
	if s.characters == nil || s.equipment == nil || s.collection == nil {
		return nil, fmt.Errorf("monsterhunt: preset ownership runtime unavailable")
	}
	assignments, bindings, e := s.presetBindings(ctx, p)
	if e != nil {
		return nil, e
	}
	if _, e = s.characters.ApplyPresetCostumes(ctx, assignments); e != nil {
		return nil, e
	}
	if len(bindings) > 0 {
		if _, e = s.equipment.ApplyPresetEquipment(ctx, bindings); e != nil {
			return nil, e
		}
	}
	var out []byte
	for _, binding := range bindings {
		c, ok := s.characters.Find(ctx, binding.CharacterIndex)
		if ok {
			out = wire.AppendBytes(out, 2, roster.CharacterWire(c))
		}
		b := wire.AppendVarint(nil, 1, binding.CharacterIndex)
		for _, id := range binding.Equipment {
			b = wire.AppendVarint(b, 2, id)
		}
		out = wire.AppendBytes(out, 3, b)
	}
	return out, nil
}

func (s *Service) validateSettings(ctx command.Context, settings [][]byte) error {
	for _, setting := range settings {
		index, e := scalar(setting, 1)
		if e != nil || index == 0 {
			return fmt.Errorf("monsterhunt: invalid costume setting character")
		}
		if s.characters != nil {
			if _, ok := s.characters.Find(ctx, index); !ok {
				return fmt.Errorf("monsterhunt: setting character not owned")
			}
		}
		seq, e := messages(setting, 2)
		if e != nil {
			return e
		}
		mode, _ := scalar(setting, 3)
		if mode != BattleMode && mode != PracticeMode {
			return fmt.Errorf("monsterhunt: invalid setting battle mode")
		}
		for _, item := range seq {
			costume, _, e := wire.Varint(item, 1)
			if e != nil {
				return e
			}
			if costume == 0 || costume == ^uint64(0) {
				continue
			}
			if s.collection != nil {
				c, ok := s.collection.CostumeByIndex(costume)
				if !ok || c.UseChar != index {
					return fmt.Errorf("monsterhunt: setting costume not owned")
				}
			}
		}
	}
	return nil
}

func Open(ctx command.Context, storage stateio.Store, rules *Rules, inventory *assets.Inventory, wallet *assets.Wallet) (*Service, error) {
	if storage == nil || rules == nil || rules.seed == nil || rules.presets == nil || rules.load == nil || len(rules.seasons) == 0 || inventory == nil || wallet == nil {
		return nil, fmt.Errorf("monsterhunt: incomplete configuration")
	}
	s := &Service{storage: storage, seed: rules.seed, seasons: rules.seasons, presets: rules.presets, load: rules.load, inventory: inventory, wallet: wallet, now: time.Now, active: map[string]encounter{}}
	s.baseSlots = rules.presets.BaseCount
	s.maxSlots = rules.presets.Maximum
	s.state = snapshot{Version: versionconfig.State(), Slots: s.baseSlots, Users: map[string]user{}, Presets: map[string][]byte{}, Replies: map[string]reply{}}
	raw, e := storage.Load(ctx.State, "monsterhunt")
	if e != nil {
		return nil, e
	}
	if raw != nil {
		if e = stateio.RequireExactJSONObject(raw, "version", "slots", "users", "decks", "settings", "presets", "replies"); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &s.state); e != nil {
			return nil, e
		}
		if s.state.Version != versionconfig.State() || s.state.Users == nil || s.state.Presets == nil || s.state.Replies == nil || s.state.Slots < s.baseSlots || s.state.Slots > s.maxSlots {
			return nil, fmt.Errorf("monsterhunt: incompatible state")
		}
	}
	if e = s.validateDecks(ctx, s.state.Decks); e != nil {
		return nil, e
	}
	if e = s.validateSettings(ctx, s.state.Settings); e != nil {
		return nil, e
	}
	for key, u := range s.state.Users {
		if key != strconv.FormatUint(u.Season, 10) || u.Season == 0 || u.Hunt == 0 || u.Level == 0 {
			return nil, fmt.Errorf("monsterhunt: invalid saved user")
		}
		d, e := s.load(u.Hunt)
		if e != nil {
			return nil, e
		}
		hp, e := d.HP(u.Level)
		if e != nil || u.StartHP > hp || u.HighestHP != hp || u.ClearLevel > d.MaxLevel {
			return nil, fmt.Errorf("monsterhunt: saved progress exceeds design")
		}
	}
	for key, p := range s.state.Presets {
		slot, _ := scalar(p, 4)
		if key != strconv.FormatUint(slot, 10) {
			return nil, fmt.Errorf("monsterhunt: invalid saved preset key")
		}
		if e = s.validatePreset(ctx, p); e != nil {
			return nil, e
		}
	}
	return s, nil
}

func (s *Service) AttachPresetRuntime(ctx command.Context, c *roster.CharacterStore, e *assets.EquipmentInventory, col *roster.CollectionStore) error {
	if c == nil || e == nil || col == nil {
		return fmt.Errorf("monsterhunt: incomplete preset runtime")
	}
	s.characters = c
	s.equipment = e
	s.collection = col
	if err := s.validateDecks(ctx, s.state.Decks); err != nil {
		return err
	}
	if err := s.validateSettings(ctx, s.state.Settings); err != nil {
		return err
	}
	for _, p := range s.state.Presets {
		if err := s.validatePreset(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) scheduleInfo(ctx command.Context, req []byte) (int, []byte, bool, error) {
	current := s.current()
	var independent []Season
	for _, row := range s.seasons {
		if row.Independent {
			independent = append(independent, row)
		}
	}
	selectedIndependent := selectSeason(independent, uint64(s.now().UnixMilli()))
	response := s.seed.Responses["/MonsterHuntScheduleInfo"]
	fields := make([]readonly.Field, 0, len(response.Fields))
	var selected []readonly.Field
	for _, field := range response.Fields {
		if field.Number == 1 && field.Type == 2 {
			var id uint64
			independent := false
			for _, f := range field.Fields {
				if f.Number == 1 {
					for _, v := range f.Fields {
						if v.Number == 1 {
							id = v.Varint
						}
					}
				}
				if f.Number == 6 {
					independent = f.Varint != 0
				}
			}
			// The current client assigns each category in a foreach: its last
			// row wins. Keep every calendar row, but move each selected row
			// last so future announcements cannot change the playable hunt.
			if !independent && id == current.ID || independent && id == selectedIndependent.ID {
				selected = append(selected, field)
				continue
			}
		}
		fields = append(fields, field)
	}
	fields = append(fields, selected...)
	// Keep the entire loaded calendar immutable; projection only affects this
	// response so subsequent requests can cross a season boundary without reload.
	projected := &readonly.Seed{Version: s.seed.Version, Responses: map[string]readonly.Response{
		"/MonsterHuntScheduleInfo": {PacketCode: response.PacketCode, Fields: fields},
	}}
	return projected.Handle(ctx, "/MonsterHuntScheduleInfo", req)
}

func (s *Service) encodeUser(u user, c Season) []byte {
	var b []byte
	for n, v := range map[int]uint64{1: u.Season, 2: u.Hunt, 3: u.Level, 4: u.StartHP, 6: u.HighestHP, 7: u.HighestDate, 8: u.CurrentDamage, 9: u.DailyDamage, 11: u.DailyLevel, 12: u.DailyDate} {
		if n == 3 {
			v = u.ClearLevel
			if v == 0 {
				v = 1
			}
		}
		if v != 0 {
			b = wire.AppendVarint(b, n, v)
		}
	}
	if u.Played && !u.Claimed && uint64(s.now().UnixMilli()) > c.Calculate {
		b = wire.AppendVarint(b, 10, 1)
	}
	return b
}

func scalar(b []byte, n int) (uint64, error) {
	v, _, e := wire.Varint(b, n)
	if e != nil || v > math.MaxInt64 {
		return 0, fmt.Errorf("monsterhunt: invalid scalar %d", n)
	}
	return v, nil
}

func messages(b []byte, n int) ([][]byte, error) {
	var out [][]byte
	e := wire.Walk(b, func(f wire.Field) error {
		if f.Number == n {
			if f.Type != 2 {
				return wire.ErrMalformed
			}
			out = append(out, append([]byte(nil), f.Value...))
		}
		return nil
	})
	return out, e
}

func packed(b []byte, n int) ([]uint64, error) {
	var out []uint64
	e := wire.Walk(b, func(f wire.Field) error {
		if f.Number != n {
			return nil
		}
		if f.Type == 0 {
			v, _ := binary.Uvarint(f.Value)
			out = append(out, v)
			return nil
		}
		if f.Type != 2 {
			return wire.ErrMalformed
		}
		for p := f.Value; len(p) > 0; {
			v, k := binary.Uvarint(p)
			if k <= 0 {
				return wire.ErrMalformed
			}
			out = append(out, v)
			p = p[k:]
		}
		return nil
	})
	return out, e
}

func (s *Service) Handle(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	commandSession := ctx.SessionID
	code, ok := codes[path]
	if !ok {
		return 0, nil, false, nil
	}

	seq, e := scalar(req, 1)
	if e != nil || seq == 0 || seq > math.MaxInt32 {
		return code, nil, true, fmt.Errorf("monsterhunt: invalid sequence")
	}
	if e = validateRequest(req); e != nil {
		return code, nil, true, e
	}
	key := commandSession + ":" + path + ":" + strconv.FormatUint(seq, 10)
	if r, found := s.state.Replies[key]; found {
		if !bytes.Equal(req, r.Request) {
			return code, nil, true, fmt.Errorf("monsterhunt: sequence reused with different request")
		}
		return code, r.Response, true, nil
	}
	if path == "/MonsterHuntScheduleInfo" {
		return s.scheduleInfo(ctx, req)
	}
	c := s.current()
	u, e := s.getUser(c)
	if e != nil {
		return code, nil, true, e
	}
	next := s.clone()
	var out []byte
	mutation := false
	var updateReceipt string
	var updateEncounter encounter
	switch path {
	case "/MonsterHuntUserInfo":
		out = wire.AppendBytes(nil, 1, s.encodeUser(u, c))
		if u.Played {
			out = wire.AppendVarint(out, 2, 1)
			out = wire.AppendDouble(out, 3, 100)
		}
	case "/MonsterHuntDeckInfo":
		for _, v := range s.state.Decks {
			out = wire.AppendBytes(out, 1, v)
		}
		for _, v := range s.state.Settings {
			out = wire.AppendBytes(out, 2, v)
		}
	case "/MonsterHuntDeckSave":
		next.Decks, e = messages(req, 2)
		if e == nil {
			e = s.validateDecks(ctx, next.Decks)
		}
		if e == nil {
			next.Settings, e = messages(req, 3)
		}
		if e == nil {
			e = s.validateSettings(ctx, next.Settings)
		}
		mutation = true
	case "/MonsterHuntChangeTeam":
		var d []byte
		d, _, e = wire.Bytes(req, 2)
		if e == nil {
			e = s.validateDecks(ctx, [][]byte{d})
		}
		mode, _ := scalar(req, 4)
		if mode != BattleMode && mode != PracticeMode {
			e = fmt.Errorf("monsterhunt: invalid team mode")
		}
		if e == nil {
			found := false
			team, _ := scalar(d, 1)
			for receipt, a := range s.active {
				if !strings.HasPrefix(receipt, commandSession+":") || a.Mode != mode {
					continue
				}
				found = true
				if team != a.Team+1 {
					e = fmt.Errorf("monsterhunt: team transition is out of order")
					break
				}
				design, err := s.load(a.Hunt)
				if err != nil {
					e = err
					break
				}
				if team > 1 && (int(team-2) >= len(design.TeamOpenLevels) || a.Level < design.TeamOpenLevels[team-2]) {
					e = fmt.Errorf("monsterhunt: team not unlocked")
					break
				}
				a.Team = team
				updateReceipt = receipt
				updateEncounter = a
				break
			}
			if !found {
				e = fmt.Errorf("monsterhunt: no active battle for team change")
			}
		}
		if e == nil {
			team, _ := scalar(d, 1)
			found := false
			for i, v := range next.Decks {
				t, _ := scalar(v, 1)
				if team == t {
					next.Decks[i] = d
					found = true
				}
			}
			if !found {
				next.Decks = append(next.Decks, d)
			}
		}
		mutation = true
	case "/MonsterHuntRankInfo":
		id, _ := scalar(req, 2)
		if id == 0 {
			id = c.ID
		}
		if v, ok := s.state.Users[strconv.FormatUint(id, 10)]; ok && v.Played {
			b := s.rankWire(v)
			out = wire.AppendBytes(out, 1, b)
			out = wire.AppendBytes(out, 2, b)
		}
	case "/MonsterHuntPresetInfo":
		keys := make([]int, 0, len(next.Presets))
		for k := range next.Presets {
			i, _ := strconv.Atoi(k)
			keys = append(keys, i)
		}
		sort.Ints(keys)
		for _, k := range keys {
			out = wire.AppendBytes(out, 1, next.Presets[strconv.Itoa(k)])
		}
	case "/MonsterHuntPresetSave":
		var p []byte
		p, _, e = wire.Bytes(req, 2)
		if e == nil {
			e = s.validatePreset(ctx, p)
		}
		if e == nil {
			slot, _ := scalar(p, 4)
			next.Presets[strconv.FormatUint(slot, 10)] = p
		}
		mutation = true
	case "/MonsterHuntPresetInfoChange":
		slot, _ := scalar(req, 5)
		p, exists := next.Presets[strconv.FormatUint(slot, 10)]
		if !exists {
			e = fmt.Errorf("monsterhunt: preset missing")
			break
		}
		name, _, _ := wire.Bytes(req, 2)
		if !utf8.Valid(name) || utf8.RuneCount(name) > 30 {
			e = fmt.Errorf("monsterhunt: invalid preset name")
			break
		}
		p, _, e = wire.ReplaceBytes(p, 1, name)
		for a, b := range map[int]int{3: 2, 4: 3} {
			v, _ := scalar(req, a)
			p, _, e = wire.ReplaceVarint(p, b, v)
		}
		if e == nil {
			e = s.validatePreset(ctx, p)
		}
		next.Presets[strconv.FormatUint(slot, 10)] = p
		mutation = true
	case "/MonsterHuntPresetDelete":
		var slots []uint64
		slots, e = packed(req, 2)
		if len(slots) == 0 {
			e = fmt.Errorf("monsterhunt: empty preset deletion")
		}
		for _, slot := range slots {
			if slot >= s.state.Slots {
				e = fmt.Errorf("monsterhunt: invalid preset slot")
			}
			delete(next.Presets, strconv.FormatUint(slot, 10))
		}
		mutation = true
	case "/MonsterHuntPresetSlotAdd":
		count, _ := scalar(req, 2)
		if count == 0 || count > s.maxSlots-next.Slots {
			e = fmt.Errorf("monsterhunt: invalid added slot count")
			break
		}
		if count > math.MaxUint64/s.presets.Price {
			e = fmt.Errorf("monsterhunt: preset price overflow")
			break
		}
		cost := count * s.presets.Price
		switch s.presets.PriceType {
		case 4:
			_, e = s.wallet.SpendGoldOnce(ctx, "monsterhunt:"+key, cost)
		case 3:
			_, e = s.wallet.SpendFreeJewelryOnce(ctx, "monsterhunt:"+key, cost)
		case 2:
			_, e = s.wallet.SpendJewelryOnce(ctx, "monsterhunt:"+key, cost)
		case 12:
			_, e = s.wallet.SpendCatalystOnce(ctx, "monsterhunt:"+key, cost)
		default:
			e = fmt.Errorf("monsterhunt: unsupported preset currency")
		}
		if e == nil {
			next.Slots += count
		}
		mutation = true
	case "/MonsterHuntPresetUse":
		slot, _ := scalar(req, 2)
		p, exists := next.Presets[strconv.FormatUint(slot, 10)]
		if !exists {
			e = fmt.Errorf("monsterhunt: preset missing")
			break
		}
		next.Decks, e = s.presetDecks(p)
		if e == nil {
			out, e = s.applyPreset(ctx, p)
		}
		if e == nil {
			for _, v := range next.Decks {
				out = wire.AppendBytes(out, 1, v)
			}
		}
		mutation = true
	case "/MonsterHuntQuickBattle":
		if !s.playing(c) || !u.Played || u.ClearLevel == 0 || u.CurrentDamage == 0 || u.DailyDamage >= u.CurrentDamage {
			e = fmt.Errorf("monsterhunt: no eligible saved battle to sweep")
			break
		}
		u.DailyDamage = u.CurrentDamage
		d, loadErr := s.load(u.Hunt)
		if loadErr != nil {
			e = loadErr
			break
		}
		bundle, grantErr := s.grant(ctx, "monsterhunt:"+key, dailyDifference(d, u.DailyLevel, u.ClearLevel))
		if grantErr != nil {
			e = grantErr
			break
		}
		out = wire.AppendBytes(out, 3, bundle)
		u.DailyLevel = u.ClearLevel
		u.DailyDate = uint64(s.now().UnixMilli())
		next.Users[strconv.FormatUint(c.ID, 10)] = u
		out = wire.AppendBytes(out, 1, s.encodeUser(u, c))
		out = wire.AppendVarint(out, 4, 1)
		out = wire.AppendDouble(out, 5, 100)
		mutation = true
	case "/MonsterHuntSeasonReward":
		if !u.Played || u.Claimed || uint64(s.now().UnixMilli()) <= c.Calculate {
			e = fmt.Errorf("monsterhunt: season reward unavailable")
			break
		}
		u.Claimed = true
		d, loadErr := s.load(u.Hunt)
		if loadErr != nil {
			e = loadErr
			break
		}
		rewards := s.rankRewards(d, c.RankGroup)
		bundle, grantErr := s.grant(ctx, "monsterhunt:"+key, rewards)
		if grantErr != nil {
			e = grantErr
			break
		}
		next.Users[strconv.FormatUint(c.ID, 10)] = u
		out = wire.AppendVarint(nil, 3, u.Hunt)
		out = wire.AppendBytes(out, 4, bundle)
		out = wire.AppendVarint(out, 1, 1)
		out = wire.AppendVarint(out, 2, uint64(s.score(u)))
		out = wire.AppendDouble(out, 5, 100)
		mutation = true
	}
	if e != nil {
		return code, nil, true, e
	}
	if mutation {
		next.Replies[key] = reply{append([]byte(nil), req...), out}
		if e = s.save(ctx, next); e != nil {
			return code, nil, true, e
		}
		if updateReceipt != "" {
			s.active[updateReceipt] = updateEncounter
			s.transientVersion++
		}
	}
	return code, out, true, nil
}

func (s *Service) validateDecks(ctx command.Context, ds [][]byte) error {
	seen := map[uint64]bool{}
	for _, d := range ds {
		t, e := scalar(d, 1)
		if e != nil || t == 0 || t > 3 || seen[t] {
			return fmt.Errorf("monsterhunt: invalid or duplicate team")
		}
		seen[t] = true
		chars, e := messages(d, 2)
		if e != nil || len(chars) > 5 {
			return fmt.Errorf("monsterhunt: invalid team size")
		}
		positions := map[uint64]bool{}
		indices := map[uint64]bool{}
		for _, b := range chars {
			i, e := scalar(b, 1)
			p, _ := scalar(b, 2)
			seq, _ := scalar(b, 3)
			if e != nil || i == 0 || p > 11 || seq == 0 || seq > 5 || positions[p] || indices[i] {
				return fmt.Errorf("monsterhunt: invalid deck character")
			}
			if s.characters != nil {
				if _, found := s.characters.Find(ctx, i); !found {
					return fmt.Errorf("monsterhunt: character not owned")
				}
			}
			positions[p] = true
			indices[i] = true
		}
	}
	return nil
}

func (s *Service) validatePreset(ctx command.Context, p []byte) error {
	slot, e := scalar(p, 4)
	if e != nil || slot >= s.state.Slots {
		return fmt.Errorf("monsterhunt: invalid preset slot")
	}
	name, _, e := wire.Bytes(p, 1)
	if e != nil || !utf8.Valid(name) || utf8.RuneCount(name) > 30 {
		return fmt.Errorf("monsterhunt: invalid preset name")
	}
	icon, _ := scalar(p, 2)
	color, _ := scalar(p, 3)
	if color > 5 || icon != 0 && !s.presets.Icons[icon] {
		return fmt.Errorf("monsterhunt: invalid preset icon")
	}
	d, e := s.presetDecks(p)
	if e != nil {
		return e
	}
	if _, _, e = s.presetBindings(ctx, p); e != nil {
		return e
	}
	return s.validateDecks(ctx, d)
}

func (s *Service) presetDecks(p []byte) ([][]byte, error) {
	entries, e := messages(p, 5)
	if e != nil {
		return nil, e
	}
	teams := map[uint64][]byte{}
	for _, entry := range entries {
		base, ok, e := wire.Bytes(entry, 1)
		if e != nil || !ok {
			return nil, fmt.Errorf("monsterhunt: missing preset deck")
		}
		t, e := scalar(entry, 4)
		if e != nil || t == 0 || t > 3 {
			return nil, fmt.Errorf("monsterhunt: invalid preset team")
		}
		teams[t] = wire.AppendBytes(teams[t], 2, base)
	}
	var out [][]byte
	for t := uint64(1); t <= 3; t++ {
		if b, ok := teams[t]; ok {
			out = append(out, append(wire.AppendVarint(nil, 1, t), b...))
		}
	}
	return out, nil
}

func (s *Service) EnterBattle(ctx command.Context, req []byte, receipt string) ([]byte, error) {

	mode, e := scalar(req, 5)
	if e != nil || mode != BattleMode && mode != PracticeMode {
		return nil, fmt.Errorf("monsterhunt: invalid battle mode")
	}
	id, _ := scalar(req, 6)
	c := s.current()
	if id != c.Hunt {
		return nil, fmt.Errorf("monsterhunt: hunt not configured")
	}
	if mode == BattleMode && !s.playing(c) {
		return nil, fmt.Errorf("monsterhunt: season is closed")
	}
	d, e := s.load(id)
	if e != nil {
		return nil, e
	}
	deck, _ := scalar(req, 4)
	if deck != d.DeckID {
		return nil, fmt.Errorf("monsterhunt: invalid battle deck")
	}
	level, _ := scalar(req, 10)
	u, e := s.getUser(c)
	if e != nil {
		return nil, e
	}
	if level == 0 {
		level = u.Level
	}
	if level > d.MaxLevel || mode == BattleMode && level > d.ChallengeableLevel && level > u.ClearLevel+1 {
		return nil, fmt.Errorf("monsterhunt: invalid challenge level")
	}
	if mode == BattleMode && u.ClearLevel >= d.MaxLevel {
		return nil, fmt.Errorf("monsterhunt: maximum level already cleared")
	}
	if receipt == "" {
		return nil, fmt.Errorf("monsterhunt: missing battle receipt")
	}
	hp, e := d.HP(level)
	if e != nil {
		return nil, e
	}
	if mode == BattleMode && level == u.Level {
		hp = u.StartHP
	}
	s.active[receipt] = encounter{c.ID, id, level, hp, mode, u.Level, u.StartHP, 1}
	s.transientVersion++
	if mode == PracticeMode {
		u.Level = level
		u.StartHP = hp
	}
	return wire.AppendBytes(nil, 5, s.encodeUser(u, c)), nil
}

func (s *Service) CompleteBattle(ctx command.Context, req []byte, receipt string) ([]byte, error) {

	key := "battle:" + receipt
	if r, ok := s.state.Replies[key]; ok {
		if !bytes.Equal(r.Request, req) {
			return nil, fmt.Errorf("monsterhunt: changed battle retry")
		}
		return r.Response, nil
	}
	a, ok := s.active[receipt]
	if !ok {
		return nil, fmt.Errorf("monsterhunt: battle was not entered")
	}
	remaining, e := scalar(req, 7)
	if e != nil || remaining > a.HP {
		return nil, fmt.Errorf("monsterhunt: invalid remaining body HP")
	}
	if a.Mode == PracticeMode {
		delete(s.active, receipt)
		s.transientVersion++
		return nil, nil
	}
	c := s.current()
	u, e := s.getUser(c)
	if e != nil {
		return nil, e
	}
	if a.Season != c.ID || a.Hunt != u.Hunt || a.BeforeLevel != u.Level || a.BeforeHP != u.StartHP {
		return nil, fmt.Errorf("monsterhunt: stale battle progress")
	}
	u.Level = a.Level
	d, e := s.load(u.Hunt)
	if e != nil {
		return nil, e
	}
	u.HighestHP, e = d.HP(a.Level)
	if e != nil {
		return nil, e
	}
	damage := a.HP - remaining
	if damage > u.CurrentDamage {
		u.CurrentDamage = damage
	}
	u.StartHP = remaining
	u.Played = true
	u.HighestDate = uint64(s.now().UnixMilli())
	previousClear := u.ClearLevel
	var clear uint64
	if remaining == 0 {
		clear = u.Level
		if clear > u.ClearLevel {
			u.ClearLevel = clear
		}
		d, e := s.load(u.Hunt)
		if e != nil {
			return nil, e
		}
		if u.ClearLevel < d.MaxLevel {
			u.Level = u.ClearLevel + 1
			u.StartHP, e = d.HP(u.Level)
			if e != nil {
				return nil, e
			}
			u.HighestHP = u.StartHP
		}
	}
	out := wire.AppendBytes(nil, 13, s.encodeUser(u, c))
	d, e = s.load(u.Hunt)
	if e != nil {
		return nil, e
	}
	if clear != 0 {
		out = wire.AppendVarint(out, 27, clear)
		var bundle []byte
		for level := previousClear + 1; level <= clear; level++ {
			b, e := s.grant(ctx, fmt.Sprintf("monsterhunt:season:%d:clear:%d", c.ID, level), d.Rewards[level].Clear)
			if e != nil {
				return nil, e
			}
			bundle = append(bundle, b...)
		}
		if len(bundle) > 0 {
			out = wire.AppendBytes(out, 7, bundle)
		}
	}
	day := uint64(s.now().UnixMilli()) / 86400000
	if damage > 0 && (u.DailyDate/86400000 != day || a.Level > u.DailyLevel) {
		bundle, e := s.grant(ctx, "monsterhunt:"+key+":daily", dailyDifference(d, u.DailyLevel, a.Level))
		if e != nil {
			return nil, e
		}
		out = wire.AppendBytes(out, 8, bundle)
		u.DailyLevel = a.Level
		u.DailyDate = uint64(s.now().UnixMilli())
		u.DailyDamage = damage
	}
	out, _, _ = wire.ReplaceBytes(out, 13, s.encodeUser(u, c))
	out = wire.AppendVarint(out, 14, 1)
	out = wire.AppendDouble(out, 26, 100)
	next := s.clone()
	next.Users[strconv.FormatUint(c.ID, 10)] = u
	next.Replies[key] = reply{append([]byte(nil), req...), out}
	if e = s.save(ctx, next); e != nil {
		return nil, e
	}
	delete(s.active, receipt)
	s.transientVersion++
	return out, nil
}
