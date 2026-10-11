package social

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"unicode/utf8"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
)

func (s *Service) raidLocal(ctx command.Context, path string, p packet, id string, g Guild, season Season, standing GuildStanding) ([]byte, error) {
	state, e := LoadRaidState(ctx.State)
	if e != nil {
		return nil, e
	}
	if state.PresetSlots == 0 && s.config.Presets != nil {
		state.PresetSlots = s.config.Presets.BaseCount
	}
	switch path {
	case "/GuildRaidDeckInfo":
		return addMessages(addMessages(addMessages(nil, 1, state.Deck), 2, state.SupporterDeck), 4, state.Settings), nil
	case "/GuildRaidDeckSave":
		if state.Active != nil {
			return nil, ErrPending
		}
		deck, e := p.messages(2)
		if e != nil {
			return nil, e
		}
		supporters, e := p.messages(3)
		if e != nil {
			return nil, e
		}
		settings, e := p.messages(5)
		if e != nil {
			return nil, e
		}
		if len(deck)+len(supporters) > 7 {
			return nil, ErrInvalid
		}
		if _, e = resolveSupporters(g, ctx.AccountID, supporters); e != nil {
			return nil, e
		}
		if e = s.validateRaidFormation(ctx, deck, settings); e != nil {
			return nil, e
		}
		if e = validateCombinedPositions(deck, supporters); e != nil {
			return nil, e
		}
		state.Deck, state.SupporterDeck, state.Settings = deck, supporters, settings
		return nil, saveRaidState(ctx, state)
	case "/GuildRaidBossQuickBattle":
		if state.Active != nil || s.config.Now().UnixMilli() >= season.EndsMS {
			return nil, ErrIneligible
		}
		member := personal(state, season.ID)
		if member.HighestLevel == 0 {
			return nil, ErrIneligible
		}
		if day := member.Days[s.config.Day(s.config.Now())]; day.Score >= member.Score && day.Level >= member.HighestLevel {
			return nil, ErrIneligible
		}
		var rewards []gamedata.Reward
		for level, boss := range s.config.Owner.design.Raid.Bosses[season.BossGroupID] {
			if level > member.RewardedLevel && level <= member.HighestLevel {
				rewards = append(rewards, boss.Rewards...)
			}
		}
		var bundle []byte
		if len(rewards) > 0 {
			if s.config.RewardBundle == nil {
				return nil, ErrInvalid
			}
			bundle, e = s.config.RewardBundle(ctx, id, rewards)
			if e != nil {
				return nil, e
			}
		}
		member.RewardedLevel = member.HighestLevel
		day := member.Days[s.config.Day(s.config.Now())]
		day.Level, day.Score = member.HighestLevel, member.Score
		member.Days[s.config.Day(s.config.Now())] = day
		state.Seasons[strconv.FormatUint(season.ID, 10)] = member
		if e = saveRaidState(ctx, state); e != nil {
			return nil, e
		}
		return wire.AppendBytes(nil, 1, bundle), nil
	case "/GuildRaidPresetInfo":
		return addMessages(nil, 1, sortedPresets(state.Presets)), nil
	case "/GuildRaidPresetSlotAdd":
		if s.config.Presets == nil {
			return nil, ErrInvalid
		}
		count, e := p.integer(2)
		d := s.config.Presets
		if e != nil || count == 0 || count > d.Maximum || state.PresetSlots > d.Maximum-count || d.Price != 0 && count > math.MaxUint64/d.Price {
			return nil, ErrInvalid
		}
		if e = s.config.Spend(ctx, id, []gamedata.Reward{{Type: d.PriceType, ID: d.PriceID, Count: d.Price * count}}); e != nil {
			return nil, e
		}
		state.PresetSlots += count
		return nil, saveRaidState(ctx, state)
	case "/GuildRaidPresetSave":
		rows, e := p.messages(2)
		if e != nil || len(rows) != 1 {
			return nil, ErrInvalid
		}
		slot, _, _, _, e := s.validateRaidPreset(ctx, rows[0], state.PresetSlots)
		if e != nil {
			return nil, e
		}
		state.Presets[strconv.FormatUint(slot, 10)] = rows[0]
		return nil, saveRaidState(ctx, state)
	case "/GuildRaidPresetDelete":
		slots, e := p.integers(2)
		if e != nil {
			return nil, e
		}
		for _, slot := range slots {
			if slot >= state.PresetSlots {
				return nil, ErrInvalid
			}
			delete(state.Presets, strconv.FormatUint(slot, 10))
		}
		return nil, saveRaidState(ctx, state)
	case "/GuildRaidPresetInfoChange":
		slot, e := p.integer(5)
		if e != nil || slot >= state.PresetSlots {
			return nil, ErrInvalid
		}
		name, e := p.text(2)
		if e != nil {
			return nil, e
		}
		icon, e := p.integer(3)
		if e != nil {
			return nil, e
		}
		color, e := p.integer(4)
		if e != nil {
			return nil, e
		}
		old := state.Presets[strconv.FormatUint(slot, 10)]
		if old == nil {
			old = wire.AppendVarint(nil, 4, slot)
		}
		old, found, e := wire.ReplaceBytes(old, 1, []byte(name))
		if e != nil {
			return nil, e
		}
		if !found {
			old = wire.AppendString(old, 1, name)
		}
		old, _, e = wire.ReplaceVarint(old, 2, icon)
		if e != nil {
			return nil, e
		}
		old, _, e = wire.ReplaceVarint(old, 3, color)
		if e != nil {
			return nil, e
		}
		if _, _, _, _, e = s.validateRaidPreset(ctx, old, state.PresetSlots); e != nil {
			return nil, e
		}
		state.Presets[strconv.FormatUint(slot, 10)] = old
		return nil, saveRaidState(ctx, state)
	case "/GuildRaidPresetUse":
		if state.Active != nil {
			return nil, ErrPending
		}
		slot, e := p.integer(2)
		if e != nil {
			return nil, e
		}
		raw, ok := state.Presets[strconv.FormatUint(slot, 10)]
		if !ok {
			return nil, ErrNotFound
		}
		_, deck, assignments, bindings, e := s.validateRaidPreset(ctx, raw, state.PresetSlots)
		if e != nil {
			return nil, e
		}
		if len(deck) == 0 || s.config.Equipment == nil {
			return nil, ErrIneligible
		}
		characters, e := s.config.Characters.ApplyPresetCostumes(ctx, assignments)
		if e != nil {
			return nil, e
		}
		affected, e := s.config.Equipment.ApplyPresetEquipment(ctx, bindings)
		if e != nil {
			return nil, e
		}
		state.Deck, state.SupporterDeck = deck, nil
		if e = saveRaidState(ctx, state); e != nil {
			return nil, e
		}
		out := addMessages(nil, 1, deck)
		seen := map[uint64]bool{}
		for _, char := range characters {
			out = wire.AppendBytes(out, 2, roster.CharacterWire(char))
			seen[char.InvenIndex] = true
		}
		for _, char := range affected {
			if !seen[char.InvenIndex] {
				if v, ok := s.config.Characters.Find(ctx, char.InvenIndex); ok {
					out = wire.AppendBytes(out, 2, roster.CharacterWire(v))
				}
			}
		}
		for _, binding := range bindings {
			row := wire.AppendVarint(nil, 1, binding.CharacterIndex)
			for _, index := range binding.Equipment {
				row = wire.AppendVarint(row, 2, index)
			}
			out = wire.AppendBytes(out, 3, row)
		}
		return out, nil
	}
	return nil, fmt.Errorf("social: unknown raid operation %s", path)
}
func validateCombinedPositions(deck, supporters [][]byte) error {
	positions, sequences := map[uint64]bool{}, map[uint64]bool{}
	for _, part := range []struct {
		rows               [][]byte
		position, sequence int
	}{{deck, 2, 3}, {supporters, 3, 4}} {
		for _, raw := range part.rows {
			p, e := parsePacket(raw)
			if e != nil {
				return e
			}
			pos, e := p.integer(part.position)
			if e != nil || pos >= 18 {
				return ErrInvalid
			}
			seq, e := p.integer(part.sequence)
			if e != nil || seq >= 7 || positions[pos] || sequences[seq] {
				return ErrInvalid
			}
			positions[pos], sequences[seq] = true, true
		}
	}
	return nil
}
func (s *Service) validateRaidPreset(ctx command.Context, raw []byte, slots uint64) (uint64, [][]byte, map[uint64]uint64, []assets.PresetEquipmentBinding, error) {
	fail := func(e error) (uint64, [][]byte, map[uint64]uint64, []assets.PresetEquipmentBinding, error) {
		return 0, nil, nil, nil, e
	}
	if s.config.Presets == nil || s.config.Equipment == nil {
		return fail(ErrInvalid)
	}
	p, e := parsePacket(raw)
	if e != nil {
		return fail(e)
	}
	slot, e := p.integer(4)
	if e != nil || slot >= slots {
		return fail(ErrInvalid)
	}
	name, e := p.text(1)
	if e != nil || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 32 {
		return fail(ErrInvalid)
	}
	icon, e := p.integer(2)
	if e != nil || icon != 0 && !s.config.Presets.Icons[icon] {
		return fail(ErrInvalid)
	}
	color, e := p.integer(3)
	if e != nil || color > math.MaxInt32 {
		return fail(ErrInvalid)
	}
	rows, e := p.messages(5)
	if e != nil || len(rows) > 7 {
		return fail(ErrInvalid)
	}
	var deck [][]byte
	assignments := map[uint64]uint64{}
	var bindings []assets.PresetEquipmentBinding
	for _, row := range rows {
		r, e := parsePacket(row)
		if e != nil {
			return fail(e)
		}
		bases, e := r.messages(1)
		if e != nil || len(bases) != 1 {
			return fail(ErrInvalid)
		}
		base, e := parsePacket(bases[0])
		if e != nil {
			return fail(e)
		}
		char, e := base.integer(1)
		if e != nil {
			return fail(e)
		}
		costume, e := r.integer(2)
		if e != nil {
			return fail(e)
		}
		if costume != 0 {
			c, ok := s.config.Collection.CostumeByIndex(costume)
			if !ok || c.UseChar != char {
				return fail(ErrIneligible)
			}
		}
		assignments[char] = costume
		deck = append(deck, bases[0])
		binding := assets.PresetEquipmentBinding{CharacterIndex: char, Equipment: make([]uint64, 5)}
		refs, e := r.messages(3)
		if e != nil {
			return fail(e)
		}
		used := map[uint64]bool{}
		for _, ref := range refs {
			q, e := parsePacket(ref)
			if e != nil {
				return fail(e)
			}
			typ, e := q.integer(1)
			if e != nil || typ >= 5 || used[typ] {
				return fail(ErrInvalid)
			}
			index, e := q.integer(2)
			if e != nil {
				return fail(e)
			}
			binding.Equipment[typ] = index
			used[typ] = true
		}
		bindings = append(bindings, binding)
	}
	if e = s.validateRaidFormation(ctx, deck, nil); e != nil {
		return fail(e)
	}
	if len(bindings) > 0 {
		if e = s.config.Equipment.ValidatePresetEquipment(ctx, bindings); e != nil {
			return fail(e)
		}
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].CharacterIndex < bindings[j].CharacterIndex })
	return slot, deck, assignments, bindings, nil
}
