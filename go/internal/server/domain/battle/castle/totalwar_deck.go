package castle

import (
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"unicode"
	"unicode/utf8"
)

var totalWarDeckCodes = map[string]int{"/TotalWarContentsItemRenew": 402, "/TotalWarDeckInfo": 237, "/TotalWarDeckPresetInfo": 239, "/TotalWarDeckPresetSave": 240, "/TotalWarDeckPresetSlotAdd": 241, "/TotalWarDeckSave": 238, "/TotalWarEquipSave": 0, "/TotalWarPresetDelete": 0, "/TotalWarPresetInfoChange": 0}

type totalWarDeckState struct {
	Slots          uint64
	Deck, Contents [][]byte
	Presets        map[uint64][]byte
}

func (s *Service) AttachTotalWarRuntime(c *roster.CharacterStore, col *roster.CollectionStore, e *assets.EquipmentInventory, w *assets.Wallet) error {
	if c == nil || col == nil || e == nil || w == nil {
		return fmt.Errorf("castle: incomplete total war runtime")
	}
	s.characters, s.collection, s.equipment, s.wallet = c, col, e, w
	return nil
}
func (s *Service) TotalWarPresetSlotCount(ctx command.Context) (uint64, error) {
	v, _, _, e := s.load(ctx)
	if e != nil {
		return 0, e
	}
	d, e := s.totalWarDeckState(v)
	if e != nil {
		return 0, e
	}
	return d.Slots, nil
}
func (s *Service) totalWarDeckState(v *state) (*totalWarDeckState, error) {
	r := s.design.TotalWarPresets
	if r == nil {
		return nil, fmt.Errorf("castle: total war preset design unavailable")
	}
	if v.TotalWarDeck == nil {
		v.TotalWarDeck = &totalWarDeckState{Slots: r.BaseCount, Presets: map[uint64][]byte{}}
	}
	d := v.TotalWarDeck
	if d.Presets == nil || d.Slots < r.BaseCount || d.Slots > r.Maximum {
		return nil, fmt.Errorf("castle: invalid total war slots")
	}
	return d, nil
}
func totalWarScalar(raw []byte, field int) (uint64, error) {
	var value uint64
	found := false
	err := wire.Walk(raw, func(f wire.Field) error {
		if f.Number != field {
			return nil
		}
		if f.Type != 0 || found {
			return fmt.Errorf("castle: invalid total war scalar %d", field)
		}
		found = true
		value, _ = binary.Uvarint(f.Value)
		if value > math.MaxInt64 {
			return fmt.Errorf("castle: invalid total war scalar %d", field)
		}
		return nil
	})
	return value, err
}
func (s *Service) validateTotalWarDeck(ctx command.Context, deck [][]byte) error {
	if s.characters == nil || s.collection == nil {
		return fmt.Errorf("castle: total war ownership unavailable")
	}
	if len(deck) > len(s.collection.Costumes()) {
		return fmt.Errorf("castle: excess total war costumes")
	}
	seen := map[uint64]bool{}
	attackers := 0
	for _, raw := range deck {
		mode, e := totalWarScalar(raw, 1)
		if e != nil || mode > 1 {
			return fmt.Errorf("castle: invalid total war role")
		}
		costume, e := totalWarScalar(raw, 2)
		if e != nil {
			return e
		}
		index, e := totalWarScalar(raw, 3)
		if e != nil {
			return e
		}
		c, ok := s.characters.Find(ctx, index)
		if !ok || roster.IsStoryCharacter(c) || roster.IsCharmCharacter(c) || roster.CharacterExpired(c, s.now()) {
			return fmt.Errorf("castle: total war character not permanently owned")
		}
		owned, ok := s.collection.CostumeByIndex(costume)
		if !ok || owned.UseChar != index || seen[costume] {
			return fmt.Errorf("castle: total war costume not owned or repeated")
		}
		seen[costume] = true
		if mode == 0 {
			attackers++
		}
	}
	if attackers > 20 {
		return fmt.Errorf("castle: more than twenty total war attackers")
	}
	return nil
}
func (s *Service) validateTotalWarContents(ctx command.Context, contents [][]byte) error {
	if s.characters == nil || s.collection == nil || s.equipment == nil {
		return fmt.Errorf("castle: total war equipment runtime unavailable")
	}
	var bindings []assets.PresetEquipmentBinding
	seen := map[uint64]bool{}
	for _, raw := range contents {
		index, e := totalWarScalar(raw, 1)
		if e != nil || index == 0 || seen[index] {
			return fmt.Errorf("castle: duplicate or invalid total war equipment character")
		}
		seen[index] = true
		c, ok := s.characters.Find(ctx, index)
		if !ok || roster.IsStoryCharacter(c) || roster.IsCharmCharacter(c) || roster.CharacterExpired(c, s.now()) {
			return fmt.Errorf("castle: equipment character not permanently owned")
		}
		connect, e := totalWarScalar(raw, 3)
		if e != nil || connect > math.MaxInt32 {
			return fmt.Errorf("castle: invalid total war potential costume")
		}
		if connect != 0 && connect != c.ConnectPotentialCostume {
			owned := false
			for _, costume := range s.collection.Costumes() {
				if costume.ID == connect && costume.UseChar == index {
					owned = true
					break
				}
			}
			if !owned {
				return fmt.Errorf("castle: total war potential costume not owned")
			}
		}
		refs, e := msgs(raw, 2)
		if e != nil {
			return e
		}
		b := assets.PresetEquipmentBinding{CharacterIndex: index, Equipment: make([]uint64, 5)}
		slots := map[uint64]bool{}
		for _, ref := range refs {
			slot, e := totalWarScalar(ref, 2)
			if e != nil || slot >= 5 || slots[slot] {
				return fmt.Errorf("castle: duplicate or invalid total war equipment slot")
			}
			slots[slot] = true
			id, e := totalWarScalar(ref, 1)
			if e != nil {
				return e
			}
			b.Equipment[slot] = id
		}
		bindings = append(bindings, b)
	}
	if len(bindings) > 0 {
		return s.equipment.ValidatePresetEquipment(ctx, bindings)
	}
	return nil
}
func (s *Service) totalWarLiveContents(ctx command.Context) ([][]byte, error) {
	if s.characters == nil || s.equipment == nil {
		return nil, fmt.Errorf("castle: total war live equipment unavailable")
	}
	equipment := map[uint64][]assets.Equipment{}
	for _, item := range s.equipment.All(ctx) {
		equipment[item.UseChar] = append(equipment[item.UseChar], item)
	}
	var out [][]byte
	for _, c := range s.characters.All(ctx) {
		if roster.IsStoryCharacter(c) || roster.IsCharmCharacter(c) || roster.CharacterExpired(c, s.now()) {
			continue
		}
		raw := wire.AppendVarint(nil, 1, c.InvenIndex)
		raw = wire.AppendVarint(raw, 3, c.ConnectPotentialCostume)
		slots := make([]uint64, 5)
		for _, item := range equipment[c.InvenIndex] {
			slot, ok := s.equipment.EquipmentSlot(item.ID)
			if !ok || slot >= 5 {
				return nil, fmt.Errorf("castle: live equipment slot unavailable")
			}
			if slots[slot] != 0 {
				return nil, fmt.Errorf("castle: live equipment occupies duplicate slot")
			}
			slots[slot] = item.InvenIndex
		}
		for slot, id := range slots {
			ref := wire.AppendVarint(nil, 1, id)
			ref = wire.AppendVarint(ref, 2, uint64(slot))
			raw = wire.AppendBytes(raw, 2, ref)
		}
		out = append(out, raw)
	}
	return out, s.validateTotalWarContents(ctx, out)
}
func (s *Service) AttachTotalWarTextCheck(check func(command.Context, string, string) error) {
	s.totalWarTextCheck = check
}
func (s *Service) validateTotalWarPreset(ctx command.Context, d *totalWarDeckState, raw []byte) error {
	if err := s.validateTotalWarPresetShape(ctx, d, raw); err != nil {
		return err
	}
	name, _, err := wire.Bytes(raw, 2)
	if err != nil {
		return err
	}
	if len(name) > 0 {
		if s.totalWarTextCheck == nil {
			return fmt.Errorf("castle: total war text approval unavailable")
		}
		return s.totalWarTextCheck(ctx, "total_war_preset_name", string(name))
	}
	return nil
}
func (s *Service) validateTotalWarPresetShape(ctx command.Context, d *totalWarDeckState, raw []byte) error {
	slot, e := totalWarScalar(raw, 1)
	if e != nil || slot >= d.Slots {
		return fmt.Errorf("castle: unavailable total war preset slot")
	}
	name, _, e := wire.Bytes(raw, 2)
	if e != nil || !utf8.Valid(name) || utf8.RuneCount(name) > 16 {
		return fmt.Errorf("castle: invalid total war preset name")
	}
	for _, r := range string(name) {
		if unicode.IsControl(r) || r == '<' || r == '>' {
			return fmt.Errorf("castle: invalid total war preset name")
		}
	}
	icon, e := totalWarScalar(raw, 3)
	if e != nil || icon != 0 && !s.design.TotalWarPresets.Icons[icon] {
		return fmt.Errorf("castle: invalid total war preset icon")
	}
	color, e := totalWarScalar(raw, 4)
	if e != nil || color > 5 {
		return fmt.Errorf("castle: invalid total war preset color")
	}
	deck, e := msgs(raw, 5)
	if e != nil {
		return e
	}
	return s.validateTotalWarDeck(ctx, deck)
}
func totalWarSlots(raw []byte) ([]uint64, error) {
	var out []uint64
	e := wire.Walk(raw, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type == 0 {
			v, k := binary.Uvarint(f.Value)
			if k <= 0 {
				return wire.ErrMalformed
			}
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
func (s *Service) totalWarDeckHandle(ctx command.Context, v *state, path string, req []byte) ([]byte, error) {
	d, e := s.totalWarDeckState(v)
	if e != nil {
		return nil, e
	}
	switch path {
	case "/TotalWarDeckInfo":
		if e = s.validateTotalWarDeck(ctx, d.Deck); e != nil {
			return nil, e
		}
		var out []byte
		for _, raw := range d.Deck {
			out = wire.AppendBytes(out, 1, raw)
		}
		for _, raw := range d.Contents {
			out = wire.AppendBytes(out, 2, raw)
		}
		return out, nil
	case "/TotalWarDeckSave":
		deck, e := msgs(req, 2)
		if e != nil {
			return nil, e
		}
		if len(deck) == 0 {
			return nil, fmt.Errorf("castle: empty total war formation")
		}
		if e = s.validateTotalWarDeck(ctx, deck); e != nil {
			return nil, e
		}
		requested, e := msgs(req, 3)
		if e != nil {
			return nil, e
		}
		if e = s.validateTotalWarContents(ctx, requested); e != nil {
			return nil, e
		}
		contents := mergeTotalWarContents(d.Contents, requested)
		live, e := s.totalWarLiveContents(ctx)
		if e != nil {
			return nil, e
		}
		present := map[uint64]bool{}
		for _, raw := range contents {
			present[n(raw, 1)] = true
		}
		for _, raw := range live {
			if !present[n(raw, 1)] {
				contents = append(contents, raw)
			}
		}
		if e = s.validateTotalWarContents(ctx, contents); e != nil {
			return nil, e
		}
		d.Deck, d.Contents = deck, contents
	case "/TotalWarContentsItemRenew":
		contents, e := s.totalWarLiveContents(ctx)
		if e != nil {
			return nil, e
		}
		d.Contents = contents
		var out []byte
		for _, raw := range contents {
			out = wire.AppendBytes(out, 1, raw)
		}
		return out, nil
	case "/TotalWarEquipSave":
		requested, e := msgs(req, 2)
		if e != nil {
			return nil, e
		}
		if len(requested) == 0 {
			return nil, fmt.Errorf("castle: empty total war equipment update")
		}
		for i, raw := range requested {
			if e = wire.Walk(raw, func(f wire.Field) error {
				if f.Number > 2 {
					return fmt.Errorf("castle: invalid total war equipment field")
				}
				return nil
			}); e != nil {
				return nil, e
			}
			index := n(raw, 1)
			for _, old := range d.Contents {
				if n(old, 1) == index {
					raw = wire.AppendVarint(raw, 3, n(old, 3))
					break
				}
			}
			requested[i] = raw
		}
		if e = s.validateTotalWarContents(ctx, requested); e != nil {
			return nil, e
		}
		contents := mergeTotalWarContents(d.Contents, requested)
		if e = s.validateTotalWarContents(ctx, contents); e != nil {
			return nil, e
		}
		d.Contents = contents
	case "/TotalWarDeckPresetInfo":
		var out []byte
		for slot := range d.Slots {
			if raw := d.Presets[slot]; raw != nil {
				if e = s.validateTotalWarPresetShape(ctx, d, raw); e != nil {
					return nil, e
				}
				out = wire.AppendBytes(out, 1, raw)
			}
		}
		return out, nil
	case "/TotalWarDeckPresetSave":
		raw, ok, e := wire.Bytes(req, 2)
		if e != nil || !ok {
			return nil, fmt.Errorf("castle: missing total war preset")
		}
		if e = s.validateTotalWarPreset(ctx, d, raw); e != nil {
			return nil, e
		}
		d.Presets[n(raw, 1)] = raw
	case "/TotalWarPresetInfoChange":
		slot, e := totalWarScalar(req, 5)
		if e != nil || slot >= d.Slots {
			return nil, fmt.Errorf("castle: unavailable total war preset slot")
		}
		raw := wire.AppendVarint(nil, 1, slot)
		name, _, e := wire.Bytes(req, 2)
		if e != nil {
			return nil, e
		}
		raw = wire.AppendBytes(raw, 2, name)
		icon, e := totalWarScalar(req, 3)
		if e != nil {
			return nil, e
		}
		color, e := totalWarScalar(req, 4)
		if e != nil {
			return nil, e
		}
		raw = wire.AppendVarint(raw, 3, icon)
		raw = wire.AppendVarint(raw, 4, color)
		deck, e := msgs(d.Presets[slot], 5)
		if e != nil {
			return nil, e
		}
		for _, entry := range deck {
			raw = wire.AppendBytes(raw, 5, entry)
		}
		if e = s.validateTotalWarPreset(ctx, d, raw); e != nil {
			return nil, e
		}
		d.Presets[slot] = raw
	case "/TotalWarPresetDelete":
		slots, e := totalWarSlots(req)
		if e != nil || len(slots) == 0 {
			return nil, fmt.Errorf("castle: invalid total war preset deletion")
		}
		for _, slot := range slots {
			if slot >= d.Slots {
				return nil, fmt.Errorf("castle: unavailable total war preset slot")
			}
		}
		for _, slot := range slots {
			delete(d.Presets, slot)
		}
	case "/TotalWarDeckPresetSlotAdd":
		count, e := totalWarScalar(req, 2)
		r := s.design.TotalWarPresets
		if e != nil || count == 0 || count > r.Maximum || d.Slots > r.Maximum-count || r.Price == 0 || count > math.MaxUint64/r.Price {
			return nil, fmt.Errorf("castle: invalid total war slot purchase")
		}
		if s.wallet == nil {
			return nil, fmt.Errorf("castle: total war wallet unavailable")
		}
		id := fmt.Sprintf("castle:totalwar:preset:%s:%s:%d", ctx.AccountID, ctx.SessionID, n(req, 1))
		price := count * r.Price
		switch r.PriceType {
		case 2:
			_, e = s.wallet.SpendJewelryOnce(ctx, id, price)
		case 3:
			_, e = s.wallet.SpendFreeJewelryOnce(ctx, id, price)
		case 4:
			_, e = s.wallet.SpendGoldOnce(ctx, id, price)
		case 12:
			_, e = s.wallet.SpendCatalystOnce(ctx, id, price)
		default:
			e = fmt.Errorf("castle: unsupported total war preset currency")
		}
		if e != nil {
			return nil, e
		}
		d.Slots += count
	default:
		return nil, fmt.Errorf("castle: unknown total war deck operation")
	}
	return nil, nil
}
func mergeTotalWarContents(previous, requested [][]byte) [][]byte {
	byIndex := map[uint64][]byte{}
	for _, raw := range previous {
		byIndex[n(raw, 1)] = raw
	}
	for _, raw := range requested {
		byIndex[n(raw, 1)] = raw
	}
	indices := make([]uint64, 0, len(byIndex))
	for id := range byIndex {
		indices = append(indices, id)
	}
	slices.Sort(indices)
	out := make([][]byte, 0, len(indices))
	for _, id := range indices {
		out = append(out, byIndex[id])
	}
	return out
}
