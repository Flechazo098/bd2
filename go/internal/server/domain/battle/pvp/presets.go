package pvp

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"fmt"
	"math"
	"sort"
	"strconv"
	"unicode/utf8"
)

func (s *Service) validatePreset(ctx command.Context, raw []byte) error {
	slot, e := scalar(raw, 4)
	if e != nil || slot >= s.state.PresetSlots {
		return fmt.Errorf("pvp: preset slot unavailable")
	}
	name, _, e := wire.Bytes(raw, 1)
	if e != nil || !utf8.Valid(name) || utf8.RuneCount(name) > 32 {
		return fmt.Errorf("pvp: invalid preset name")
	}
	icon, e := scalar(raw, 2)
	if e != nil || icon != 0 && !s.rules.Design.Presets.Icons[icon] {
		return fmt.Errorf("pvp: invalid preset icon")
	}
	color, e := scalar(raw, 3)
	if e != nil || color > math.MaxInt32 {
		return fmt.Errorf("pvp: invalid preset color")
	}
	entries, e := messages(raw, 5)
	if e != nil {
		return e
	}
	var deck, contents [][]byte
	for _, entry := range entries {
		base, exists, e := wire.Bytes(entry, 1)
		if e != nil || !exists {
			return fmt.Errorf("pvp: missing preset character")
		}
		index, e := scalar(base, 1)
		if e != nil {
			return e
		}
		costume, e := scalar(entry, 2)
		if e != nil {
			return e
		}
		deck = append(deck, wire.AppendVarint(append([]byte(nil), base...), 4, costume))
		refs, e := messages(entry, 3)
		if e != nil {
			return e
		}
		item := wire.AppendVarint(nil, 1, index)
		for _, ref := range refs {
			slot, e := scalar(ref, 1)
			if e != nil {
				return e
			}
			equipment, e := scalar(ref, 2)
			if e != nil {
				return e
			}
			item = wire.AppendBytes(item, 2, wire.AppendVarint(wire.AppendVarint(nil, 1, equipment), 2, slot))
		}
		contents = append(contents, item)
	}
	season, e := s.current(Colosseum)
	if e != nil {
		return e
	}
	if e = s.validateDeck(Colosseum, season.ID, deck); e != nil {
		return e
	}
	// Preset storage never changes the live formation or publishes a defense.
	_, e = s.builder.Freeze(ctx, FreezeRequest{Mode: Colosseum, Season: season.ID, Deck: deck, Contents: contents, Renew: false})
	return e
}

func (s *Service) preset(ctx command.Context, path string, request []byte) ([]byte, error) {
	if path == "/ColosseumPresetInfo" {
		entries, e := ctx.State.ListEntries("pvp", "presets")
		if e != nil {
			return nil, e
		}
		slots := make([]int, 0, len(entries))
		for key := range entries {
			slot, e := strconv.Atoi(key)
			if e != nil || slot < 0 || uint64(slot) >= s.state.PresetSlots {
				return nil, fmt.Errorf("pvp: corrupt preset slot")
			}
			slots = append(slots, slot)
		}
		sort.Ints(slots)
		var out []byte
		for _, slot := range slots {
			out = wire.AppendBytes(out, 1, entries[strconv.Itoa(slot)])
		}
		return out, nil
	}
	switch path {
	case "/ColosseumPresetSave":
		raw, exists, e := wire.Bytes(request, 2)
		if e != nil || !exists {
			return nil, fmt.Errorf("pvp: missing preset")
		}
		if e = s.validatePreset(ctx, raw); e != nil {
			return nil, e
		}
		slot, _ := scalar(raw, 4)
		return nil, ctx.State.PutEntry("pvp", "presets", strconv.FormatUint(slot, 10), raw)
	case "/ColosseumPresetDelete":
		slots, e := ints(request, 2)
		if e != nil {
			return nil, e
		}
		for _, slot := range slots {
			if slot >= s.state.PresetSlots {
				return nil, fmt.Errorf("pvp: invalid delete slot")
			}
		}
		for _, slot := range slots {
			if _, e = ctx.State.DeleteEntry("pvp", "presets", strconv.FormatUint(slot, 10)); e != nil {
				return nil, e
			}
		}
		return nil, nil
	case "/ColosseumPresetInfoChange":
		slot, e := scalar(request, 5)
		if e != nil || slot >= s.state.PresetSlots {
			return nil, fmt.Errorf("pvp: unavailable preset slot")
		}
		raw, exists, e := ctx.State.LoadEntry("pvp", "presets", strconv.FormatUint(slot, 10))
		if e != nil {
			return nil, e
		}
		if !exists {
			raw = wire.AppendVarint(nil, 4, slot)
		}
		name, _, e := wire.Bytes(request, 2)
		if e != nil {
			return nil, e
		}
		raw, exists, e = wire.ReplaceBytes(raw, 1, name)
		if e != nil {
			return nil, e
		}
		if !exists {
			raw = wire.AppendBytes(raw, 1, name)
		}
		for field := 2; field <= 3; field++ {
			value, e := scalar(request, field+1)
			if e != nil {
				return nil, e
			}
			raw, _, e = wire.ReplaceVarint(raw, field, value)
			if e != nil {
				return nil, e
			}
		}
		if e = s.validatePreset(ctx, raw); e != nil {
			return nil, e
		}
		return nil, ctx.State.PutEntry("pvp", "presets", strconv.FormatUint(slot, 10), raw)
	case "/ColosseumPresetSlotAdd":
		count, e := scalar(request, 2)
		d := s.rules.Design.Presets
		if e != nil || count == 0 || count > d.Maximum || s.state.PresetSlots > d.Maximum-count || count > math.MaxUint64/d.Price {
			return nil, fmt.Errorf("pvp: invalid preset slot purchase")
		}
		seq, _ := scalar(request, 1)
		id := fmt.Sprintf("pvp:preset:%s:%s:%d", s.profile.OwnerID, ctx.SessionID, seq)
		if e = s.spend(ctx, id, []gamedata.BattleReward{{Type: d.PriceType, ID: d.PriceID, Count: d.Price * count}}); e != nil {
			return nil, e
		}
		s.state.PresetSlots += count
		return nil, s.save(ctx)
	}
	return nil, fmt.Errorf("pvp: unknown preset operation")
}
