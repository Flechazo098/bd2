package deck

import (
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
)

// Handle implements session.Handler. Every mutation validates its complete
// typed request before committing a replacement JSON state.
func (s *Store) Handle(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	switch path {
	case "/TalentSlotSave", "/CharAutoReviveSet":
		return s.handleFieldSettings(ctx, path, req)
	case "/PresetInfo":
		return s.handlePresetInfo(req)
	case "/PresetSave":
		return s.handlePresetSave(ctx, req)
	case "/PresetAddSlot":
		return s.handlePresetAddSlot(ctx, req)
	case "/PresetInfoChange":
		return s.handlePresetInfoChange(ctx, req)
	case "/PresetDelete":
		return s.handlePresetDelete(ctx, req)
	case "/PresetUse":
		return s.handlePresetUse(ctx, req)
	case "/DeckCostumeSettingInfo":
		return s.handleCostumeSettingInfo(req)
	case "/DeckCostumeSettingSave":
		return s.handleCostumeSettingSave(ctx, req)
	case "/DeckInfo":
		if e := checkSeq(req); e != nil {
			return 0, nil, true, e
		}

		out := encodeDeck(s.state.Deck)
		if s.fieldSettingsDesign != nil {
			v, e := s.loadFieldSettings(ctx)
			if e != nil {
				return 0, nil, true, e
			}
			for _, id := range s.projectTalentSlots(ctx, v.TalentIDs) {
				out = wire.AppendVarint(out, 2, id)
			}
		}
		return 8, out, true, nil
	case "/FieldDeckInfo":
		if e := checkSeq(req); e != nil {
			return 0, nil, true, e
		}

		return 273, encodeField(s.visibleFieldDeckLocked(ctx)), true, nil
	case "/DeckCharAutoRevive":
		return s.handleAutoRecovery(ctx, req)
	case "/WaypointInfo":
		return s.handleWaypoint(ctx, path, req)
	case "/DeckSave":
		if e := checkSeq(req); e != nil {
			return 0, nil, true, e
		}
		x, e := triples(req)
		if e != nil {
			return 0, nil, true, e
		}

		if e = s.validateOwnedDeckLocked(ctx, x); e != nil {
			return 0, nil, true, e
		}
		n := clone(s.state)
		n.Deck = x
		e = s.commit(ctx, n)
		return 10, nil, true, e
	case "/FieldDeckSave":
		if e := checkSeq(req); e != nil {
			return 0, nil, true, e
		}
		x, e := fieldEntries(req)
		if e != nil {
			return 0, nil, true, e
		}

		if e = s.validateOwnedFieldDeckLocked(ctx, x); e != nil {
			return 0, nil, true, e
		}
		n := clone(s.state)
		n.FieldDeck = x
		e = s.commit(ctx, n)
		return 274, nil, true, e
	case "/SaveFieldCharControlDeckType":
		// Define_FieldCharControllDeckType is a proto3 enum whose valid values
		// are BATTLE=0, FIELD=1 and STORY=2. BATTLE is the protobuf default, so
		// the generated client deliberately omits field 2 when it switches out
		// of story mode after the final quest. An absent field is therefore a
		// real value 0, not a malformed request.
		v, _, e := wire.Varint(req, 2)
		if e != nil || v > 2 {
			return 0, nil, true, errors.New("deck: invalid field control type")
		}
		if e = checkSeq(req); e != nil {
			return 0, nil, true, e
		}

		n := clone(s.state)
		n.FieldCharControlDeckType = v
		e = s.commit(ctx, n)
		return 288, nil, true, e
	case "/WaypointSave", "/WaypointUse":
		return s.handleWaypoint(ctx, path, req)
	case "/CostumeUse":
		return s.handleCostumeUse(ctx, req)
	case "/SaveTotalBattlePower":
		power, ok, e := wire.Varint(req, 2)
		if e != nil || !ok || power == 0 {
			return 0, nil, true, errors.New("deck: invalid total battle power")
		}
		if e = checkSeq(req); e != nil {
			return 0, nil, true, e
		}

		n := clone(s.state)
		if power > n.HighestTotalBattlePower {
			n.HighestTotalBattlePower = power
		}
		if e = s.commit(ctx, n); e != nil {
			return 0, nil, true, e
		}
		return 258, wire.AppendVarint(nil, 1, n.HighestTotalBattlePower), true, nil
	case "/UserPortraitChange":
		costumeID, ok, e := wire.Varint(req, 2)
		if e != nil || !ok || costumeID == 0 {
			return 0, nil, true, errors.New("deck: invalid portrait costume")
		}
		if e = checkSeq(req); e != nil {
			return 0, nil, true, e
		}

		n := clone(s.state)
		n.PortraitCostumeID = costumeID
		if e = s.commit(ctx, n); e != nil {
			return 0, nil, true, e
		}
		return 75, wire.AppendVarint(nil, 1, costumeID), true, nil
	}
	return 0, nil, false, nil
}

func (s *Store) handleWaypoint(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	seq, err := requestSequence(req)
	if err != nil {
		return 0, nil, true, err
	}
	pack, found, err := wire.Varint(req, 2)
	if err != nil || !found || pack == 0 || pack > math.MaxInt32 {
		return 0, nil, true, errors.New("deck: invalid waypoint pack")
	}

	if s.waypointDesign == nil || s.waypointPack == nil {
		return 0, nil, true, errors.New("deck: waypoint runtime unavailable")
	}
	if err = s.waypointPack(ctx, pack, path == "/WaypointUse"); err != nil {
		return 0, nil, true, err
	}
	design, err := s.waypointDesign(pack)
	if err != nil {
		return 0, nil, true, err
	}
	if path == "/WaypointInfo" {
		ids := append([]uint64(nil), s.state.Waypoints[pack]...)
		slices.Sort(ids)
		var packed []byte
		for _, id := range ids {
			if _, known := design.Points[id]; !known {
				return 0, nil, true, fmt.Errorf("deck: saved waypoint %d absent from pack%d", id, pack)
			}
			packed = binary.AppendUvarint(packed, id)
		}
		if len(packed) == 0 {
			return 31, nil, true, nil
		}
		return 31, wire.AppendBytes(nil, 1, packed), true, nil
	}
	id, found, err := wire.Varint(req, 3)
	if err != nil || !found || id == 0 || id > math.MaxInt32 {
		return 0, nil, true, errors.New("deck: invalid waypoint")
	}
	if _, known := design.Points[id]; !known {
		return 0, nil, true, errors.New("deck: unknown waypoint")
	}
	if path == "/WaypointSave" {
		if hasWaypoint(s.state.Waypoints[pack], id) {
			return 32, nil, true, nil
		}
		next := clone(s.state)
		next.Waypoints[pack] = append(next.Waypoints[pack], id)
		err = s.commit(ctx, next)
		return 32, nil, true, err
	}
	end, found, err := wire.Varint(req, 4)
	if err != nil || !found || end == 0 || end > math.MaxInt32 || end == id {
		return 0, nil, true, errors.New("deck: invalid waypoint destination")
	}
	target, known := design.Points[end]
	if !known || target.MapID == 0 || !hasWaypoint(s.state.Waypoints[pack], end) || !hasWaypoint(s.state.Waypoints[pack], id) {
		return 0, nil, true, errors.New("deck: waypoint is not activated")
	}
	moves, found, err := wire.Varint(req, 5)
	if err != nil || !found || moves != 1 {
		return 0, nil, true, errors.New("deck: invalid waypoint move count")
	}
	if reply, ok := s.cachedReplyLocked(ctx, "waypoint-use", seq); ok {
		return reply.code, reply.body, true, nil
	}
	if design.PriceUnit != 0 {
		if s.wallet == nil || ctx.SessionID == "" {
			return 0, nil, true, errors.New("deck: waypoint wallet session unavailable")
		}
		identity := fmt.Sprintf("waypoint:%s:%d", ctx.SessionID, seq)
		switch design.PriceType {
		case 4:
			_, err = s.wallet.SpendGoldOnce(ctx, identity, design.PriceUnit)
		case 3:
			_, err = s.wallet.SpendFreeJewelryOnce(ctx, identity, design.PriceUnit)
		case 2:
			_, err = s.wallet.SpendJewelryOnce(ctx, identity, design.PriceUnit)
		default:
			err = errors.New("deck: unsupported waypoint currency")
		}
		if err != nil {
			return 0, nil, true, err
		}
	}
	// The client performs its warp and sends SaveUserPosition with scene coordinates.
	s.rememberReplyLocked(ctx, "waypoint-use", seq, 33, nil)
	return 33, nil, true, nil
}

func decodePreset(data []byte) (Preset, error) {
	var p Preset
	name, _, err := wire.Bytes(data, 1)
	if err != nil {
		return p, err
	}
	p.Name = string(name)
	p.ResourceID, _, err = wire.Varint(data, 2)
	if err != nil {
		return p, err
	}
	p.ResourceColor, _, err = wire.Varint(data, 3)
	if err != nil {
		return p, err
	}
	p.Slot, _, err = wire.Varint(data, 4)
	if err != nil {
		return p, err
	}
	err = wire.Walk(data, func(field wire.Field) error {
		if field.Type != 2 {
			return nil
		}
		switch field.Number {
		case 5:
			deck, err := decodePresetDeck(field.Value)
			if err != nil {
				return err
			}
			p.Decks = append(p.Decks, deck)
		case 6:
			bless, err := decodePresetBless(field.Value)
			if err != nil {
				return err
			}
			p.Blesses = append(p.Blesses, bless)
		}
		return nil
	})
	return p, err
}

func decodePresetDeck(data []byte) (PresetDeck, error) {
	var result PresetDeck
	base, found, err := wire.Bytes(data, 1)
	if err != nil || !found {
		return result, errors.New("deck: preset missing deck base")
	}
	character, ok, err := wire.Varint(base, 1)
	if err != nil || !ok || character == 0 {
		return result, errors.New("deck: invalid preset character")
	}
	position, _, err := wire.Varint(base, 2)
	if err != nil {
		return result, err
	}
	sequence, ok, err := wire.Varint(base, 3)
	if err != nil || !ok || sequence == 0 {
		return result, errors.New("deck: invalid preset sequence")
	}
	result.Deck = DeckEntry{CharacterInvenIndex: character, CostumeInvenIndex: position, Slot: sequence}
	result.CostumeIndex, _, err = wire.Varint(data, 2)
	if err != nil {
		return result, err
	}
	result.Team, _, err = wire.Varint(data, 4)
	if err != nil {
		return result, err
	}
	err = wire.Walk(data, func(field wire.Field) error {
		if field.Number != 3 {
			return nil
		}
		if field.Type != 2 {
			return errors.New("deck: invalid preset equipment")
		}
		typeID, _, err := wire.Varint(field.Value, 1)
		if err != nil {
			return err
		}
		index, _, err := wire.Varint(field.Value, 2)
		if err != nil {
			return err
		}
		result.Equipment = append(result.Equipment, PresetEquipmentItem{Type: typeID, Index: index})
		return nil
	})
	sort.Slice(result.Equipment, func(i, j int) bool { return result.Equipment[i].Type < result.Equipment[j].Type })
	return result, err
}

func decodePresetBless(data []byte) (PresetBless, error) {
	var result PresetBless
	result.DeckType, _, _ = wire.Varint(data, 1)
	err := wire.Walk(data, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		values, err := repeatedUint64(field)
		if err != nil {
			return err
		}
		result.IDs = append(result.IDs, values...)
		return nil
	})
	return result, err
}

func repeatedUint64(field wire.Field) ([]uint64, error) {
	if field.Type == 0 {
		value, count := binary.Uvarint(field.Value)
		if count <= 0 {
			return nil, wire.ErrMalformed
		}
		return []uint64{value}, nil
	}
	if field.Type != 2 {
		return nil, wire.ErrMalformed
	}
	var out []uint64
	for offset := 0; offset < len(field.Value); {
		value, count := binary.Uvarint(field.Value[offset:])
		if count <= 0 {
			return nil, wire.ErrMalformed
		}
		out = append(out, value)
		offset += count
	}
	return out, nil
}

func presetWire(p Preset) []byte {
	var out []byte
	if p.Name != "" {
		out = wire.AppendString(out, 1, p.Name)
	}
	if p.ResourceID != 0 {
		out = wire.AppendVarint(out, 2, p.ResourceID)
	}
	if p.ResourceColor != 0 {
		out = wire.AppendVarint(out, 3, p.ResourceColor)
	}
	if p.Slot != 0 {
		out = wire.AppendVarint(out, 4, p.Slot)
	}
	for _, deck := range p.Decks {
		out = wire.AppendBytes(out, 5, presetDeckWire(deck))
	}
	for _, bless := range p.Blesses {
		var b []byte
		if bless.DeckType != 0 {
			b = wire.AppendVarint(b, 1, bless.DeckType)
		}
		for _, id := range bless.IDs {
			b = wire.AppendVarint(b, 2, id)
		}
		out = wire.AppendBytes(out, 6, b)
	}
	return out
}

func presetDeckWire(deck PresetDeck) []byte {
	base := wire.AppendVarint(nil, 1, deck.Deck.CharacterInvenIndex)
	if deck.Deck.CostumeInvenIndex != 0 {
		base = wire.AppendVarint(base, 2, deck.Deck.CostumeInvenIndex)
	}
	base = wire.AppendVarint(base, 3, deck.Deck.Slot)
	out := wire.AppendBytes(nil, 1, base)
	if deck.CostumeIndex != 0 {
		out = wire.AppendVarint(out, 2, deck.CostumeIndex)
	}
	for _, item := range deck.Equipment {
		var b []byte
		if item.Type != 0 {
			b = wire.AppendVarint(b, 1, item.Type)
		}
		if item.Index != 0 {
			b = wire.AppendVarint(b, 2, item.Index)
		}
		out = wire.AppendBytes(out, 3, b)
	}
	if deck.Team != 0 {
		out = wire.AppendVarint(out, 4, deck.Team)
	}
	return out
}

func requestSequence(request []byte) (uint64, error) {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return 0, errors.New("deck: invalid request sequence")
	}
	return seq, nil
}

func (s *Store) handlePresetInfo(request []byte) (int, []byte, bool, error) {
	if _, err := requestSequence(request); err != nil {
		return 0, nil, true, err
	}

	slots := make([]uint64, 0, len(s.presets))
	for slot := range s.presets {
		if slot < s.presetSlots {
			slots = append(slots, slot)
		}
	}
	slices.Sort(slots)
	var response []byte
	for _, slot := range slots {
		response = wire.AppendBytes(response, 1, presetWire(s.presets[slot]))
	}
	return 178, response, true, nil
}

func (s *Store) handlePresetSave(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, err := requestSequence(request)
	if err != nil {
		return 0, nil, true, err
	}
	raw, found, err := wire.Bytes(request, 2)
	if err != nil || !found {
		return 0, nil, true, errors.New("deck: PresetSave missing preset")
	}
	preset, err := decodePreset(raw)
	if err != nil {
		return 0, nil, true, fmt.Errorf("deck: decode preset: %w", err)
	}

	if reply, found := s.cachedReplyLocked(ctx, "save", seq); found {
		return reply.code, reply.body, true, nil
	}
	if s.characters == nil || s.collection == nil || s.equipment == nil {
		return 0, nil, true, errors.New("deck: preset runtime unavailable")
	}
	if err := s.validatePresetOwnedLocked(ctx, preset); err != nil {
		return 0, nil, true, fmt.Errorf("deck: invalid preset: %w", err)
	}
	if err := s.persistPresetLocked(ctx, preset); err != nil {
		return 0, nil, true, fmt.Errorf("deck: persist preset: %w", err)
	}
	s.presets[preset.Slot] = preset
	s.rememberReplyLocked(ctx, "save", seq, 179, nil)
	return 179, nil, true, nil
}

func (s *Store) handlePresetAddSlot(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, err := requestSequence(request)
	if err != nil {
		return 0, nil, true, err
	}
	count, found, err := wire.Varint(request, 2)
	if err != nil || !found || count == 0 {
		return 0, nil, true, errors.New("deck: PresetAddSlot invalid count")
	}

	if reply, found := s.cachedReplyLocked(ctx, "add-slot", seq); found {
		return reply.code, reply.body, true, nil
	}
	if s.wallet == nil || s.presetDesign.Validate() != nil {
		return 0, nil, true, errors.New("deck: preset wallet/design unavailable")
	}
	if s.presetSlots > s.presetDesign.Maximum || count > s.presetDesign.Maximum-s.presetSlots {
		return 0, nil, true, errors.New("deck: preset slot limit exceeded")
	}
	if count > ^uint64(0)/s.presetDesign.Price {
		return 0, nil, true, errors.New("deck: preset slot price overflow")
	}
	identity := "preset-slot:" + ctx.SessionID + ":" + strconv.FormatUint(seq, 10)
	var spendErr error
	switch s.presetDesign.PriceType {
	case 4:
		_, spendErr = s.wallet.SpendGoldOnce(ctx, identity, count*s.presetDesign.Price)
	case 3:
		_, spendErr = s.wallet.SpendFreeJewelryOnce(ctx, identity, count*s.presetDesign.Price)
	case 2:
		_, spendErr = s.wallet.SpendJewelryOnce(ctx, identity, count*s.presetDesign.Price)
	case 12:
		_, spendErr = s.wallet.SpendCatalystOnce(ctx, identity, count*s.presetDesign.Price)
	}
	if err := spendErr; err != nil {
		return 0, nil, true, fmt.Errorf("deck: buy preset slot: %w", err)
	}
	next := s.presetSlots + count
	payload, err := json.Marshal(next)
	if err != nil {
		return 0, nil, true, err
	}
	core, err := s.corePayloadLocked()
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.storage.SaveWithEntries(ctx.State, "deck", core, []stateio.EntryMutation{{Bucket: "preset_config", Key: "slots", Payload: payload}}); err != nil {
		return 0, nil, true, fmt.Errorf("deck: persist preset slots: %w", err)
	}
	s.presetSlots = next
	s.rememberReplyLocked(ctx, "add-slot", seq, 180, nil)
	return 180, nil, true, nil
}

func (s *Store) handlePresetInfoChange(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, err := requestSequence(request)
	if err != nil {
		return 0, nil, true, err
	}
	nameBytes, _, err := wire.Bytes(request, 2)
	if err != nil {
		return 0, nil, true, errors.New("deck: invalid preset name")
	}
	resourceID, _, err := wire.Varint(request, 3)
	if err != nil {
		return 0, nil, true, errors.New("deck: invalid preset icon")
	}
	color, _, err := wire.Varint(request, 4)
	if err != nil {
		return 0, nil, true, errors.New("deck: invalid preset color")
	}
	// Ordinary preset slots are zero-based. Proto3 omits slot=0, so an absent
	// field 5 is the first slot rather than a malformed request.
	slot, _, err := wire.Varint(request, 5)
	if err != nil {
		return 0, nil, true, errors.New("deck: invalid preset slot")
	}

	if reply, found := s.cachedReplyLocked(ctx, "info-change", seq); found {
		return reply.code, reply.body, true, nil
	}
	preset, exists := s.presets[slot]
	if !exists {
		preset = Preset{Slot: slot, Decks: []PresetDeck{}, Blesses: []PresetBless{}}
	}
	preset.Name, preset.ResourceID, preset.ResourceColor = string(nameBytes), resourceID, color
	if err := s.validatePresetShape(preset, s.presetSlots); err != nil {
		return 0, nil, true, fmt.Errorf("deck: invalid preset metadata: %w", err)
	}
	if err := s.persistPresetLocked(ctx, preset); err != nil {
		return 0, nil, true, err
	}
	s.presets[slot] = preset
	s.rememberReplyLocked(ctx, "info-change", seq, 278, nil)
	return 278, nil, true, nil
}

func (s *Store) handlePresetDelete(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, err := requestSequence(request)
	if err != nil {
		return 0, nil, true, err
	}
	var slots []uint64
	if err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		values, err := repeatedUint64(field)
		if err != nil {
			return err
		}
		slots = append(slots, values...)
		return nil
	}); err != nil || len(slots) == 0 {
		return 0, nil, true, errors.New("deck: invalid preset delete slots")
	}

	if reply, found := s.cachedReplyLocked(ctx, "delete", seq); found {
		return reply.code, reply.body, true, nil
	}
	seen := make(map[uint64]bool, len(slots))
	changes := make([]stateio.EntryMutation, 0, len(slots))
	for _, slot := range slots {
		if slot >= s.presetSlots || seen[slot] {
			return 0, nil, true, errors.New("deck: invalid or duplicate preset delete slot")
		}
		seen[slot] = true
		changes = append(changes, stateio.EntryMutation{Bucket: "presets", Key: strconv.FormatUint(slot, 10), Delete: true})
	}
	core, err := s.corePayloadLocked()
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.storage.SaveWithEntries(ctx.State, "deck", core, changes); err != nil {
		return 0, nil, true, err
	}
	for slot := range seen {
		delete(s.presets, slot)
	}
	// 2.35.10 contains the request/response classes but no PacketCode enum
	// member. Code zero is the same compatibility fallback used for other
	// unnumbered local endpoints; it must not be treated as an official value.
	s.rememberReplyLocked(ctx, "delete", seq, 0, nil)
	return 0, nil, true, nil
}

func (s *Store) handlePresetUse(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, err := requestSequence(request)
	if err != nil {
		return 0, nil, true, err
	}
	// PresetUse is also zero-based and slot=0 is omitted by proto3.
	slot, _, err := wire.Varint(request, 2)
	if err != nil {
		return 0, nil, true, errors.New("deck: PresetUse missing slot")
	}

	if reply, found := s.cachedReplyLocked(ctx, "use", seq); found {
		return reply.code, reply.body, true, nil
	}
	preset, found := s.presets[slot]
	if !found || len(preset.Decks) == 0 {
		return 0, nil, true, errors.New("deck: PresetUse references an empty slot")
	}
	if s.characters == nil || s.collection == nil || s.equipment == nil {
		return 0, nil, true, errors.New("deck: preset runtime unavailable")
	}
	if err := s.validatePresetOwnedLocked(ctx, preset); err != nil {
		return 0, nil, true, fmt.Errorf("deck: stale preset: %w", err)
	}
	deckEntries := make([]DeckEntry, 0, len(preset.Decks))
	assignments := make(map[uint64]uint64, len(preset.Decks))
	bindings := make([]assets.PresetEquipmentBinding, 0, len(preset.Decks))
	for _, entry := range preset.Decks {
		deckEntries = append(deckEntries, entry.Deck)
		assignments[entry.Deck.CharacterInvenIndex] = entry.CostumeIndex
		equipment := make([]uint64, 5)
		for _, item := range entry.Equipment {
			equipment[item.Type] = item.Index
		}
		bindings = append(bindings, assets.PresetEquipmentBinding{CharacterIndex: entry.Deck.CharacterInvenIndex, Equipment: equipment})
	}
	if _, err := s.characters.ApplyPresetCostumes(ctx, assignments); err != nil {
		return 0, nil, true, fmt.Errorf("deck: apply preset costumes: %w", err)
	}
	affected, err := s.equipment.ApplyPresetEquipment(ctx, bindings)
	if err != nil {
		return 0, nil, true, fmt.Errorf("deck: apply preset equipment: %w", err)
	}
	next := clone(s.state)
	next.Deck = append([]DeckEntry(nil), deckEntries...)
	maps.Copy(next.Costumes, assignments)
	if err := s.commit(ctx, next); err != nil {
		return 0, nil, true, fmt.Errorf("deck: persist applied preset: %w", err)
	}
	var response []byte
	for _, entry := range deckEntries {
		base := wire.AppendVarint(nil, 1, entry.CharacterInvenIndex)
		if entry.CostumeInvenIndex != 0 {
			base = wire.AppendVarint(base, 2, entry.CostumeInvenIndex)
		}
		base = wire.AppendVarint(base, 3, entry.Slot)
		response = wire.AppendBytes(response, 1, base)
	}
	characterSet := make(map[uint64]bool, len(assignments)+len(affected))
	for index := range assignments {
		characterSet[index] = true
	}
	for _, character := range affected {
		characterSet[character.InvenIndex] = true
	}
	indices := make([]uint64, 0, len(characterSet))
	for index := range characterSet {
		indices = append(indices, index)
	}
	slices.Sort(indices)
	for _, index := range indices {
		if character, found := s.characters.Find(ctx, index); found {
			response = wire.AppendBytes(response, 2, roster.CharacterWire(character))
		}
	}
	for _, binding := range bindings {
		info := wire.AppendVarint(nil, 1, binding.CharacterIndex)
		for _, index := range binding.Equipment {
			info = wire.AppendVarint(info, 2, index)
		}
		response = wire.AppendBytes(response, 3, info)
	}
	s.rememberReplyLocked(ctx, "use", seq, 409, response)
	return 409, response, true, nil
}

func costumeSettingWire(setting CostumeSetting) []byte {
	out := wire.AppendVarint(nil, 1, setting.CharacterIndex)
	for _, item := range setting.Sequence {
		entry := wire.AppendVarint(nil, 1, uint64(item.CostumeIndex))
		if item.BurstLevel != 0 {
			entry = wire.AppendVarint(entry, 2, item.BurstLevel)
		}
		out = wire.AppendBytes(out, 2, entry)
	}
	if setting.BattleMode != 0 {
		out = wire.AppendVarint(out, 3, setting.BattleMode)
	}
	if setting.MonsterID != 0 {
		out = wire.AppendVarint(out, 4, setting.MonsterID)
	}
	return out
}

func decodeCostumeSetting(data []byte) (CostumeSetting, error) {
	var result CostumeSetting
	character, found, err := wire.Varint(data, 1)
	if err != nil || !found || character == 0 {
		return result, errors.New("deck: costume setting missing character")
	}
	result.CharacterIndex = character
	result.BattleMode, _, err = wire.Varint(data, 3)
	if err != nil {
		return result, err
	}
	result.MonsterID, _, err = wire.Varint(data, 4)
	if err != nil {
		return result, err
	}
	err = wire.Walk(data, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		if field.Type != 2 {
			return errors.New("deck: invalid costume setting sequence")
		}
		raw, found, err := wire.Varint(field.Value, 1)
		if err != nil || !found {
			return errors.New("deck: costume setting entry missing costume")
		}
		burst, _, err := wire.Varint(field.Value, 2)
		if err != nil {
			return err
		}
		result.Sequence = append(result.Sequence, CostumeSettingItem{CostumeIndex: int64(raw), BurstLevel: burst})
		return nil
	})
	return result, err
}

func (s *Store) handleCostumeSettingInfo(request []byte) (int, []byte, bool, error) {
	if _, err := requestSequence(request); err != nil {
		return 0, nil, true, err
	}

	indices := make([]uint64, 0, len(s.costumeSettings))
	for index := range s.costumeSettings {
		indices = append(indices, index)
	}
	slices.Sort(indices)
	var response []byte
	for _, index := range indices {
		response = wire.AppendBytes(response, 1, costumeSettingWire(s.costumeSettings[index]))
	}
	return 397, response, true, nil
}

func (s *Store) handleCostumeSettingSave(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, err := requestSequence(request)
	if err != nil {
		return 0, nil, true, err
	}
	raw, found, err := wire.Bytes(request, 2)
	if err != nil || !found {
		return 0, nil, true, errors.New("deck: DeckCostumeSettingSave missing setting")
	}
	setting, err := decodeCostumeSetting(raw)
	if err != nil {
		return 0, nil, true, err
	}

	if reply, found := s.cachedReplyLocked(ctx, "costume-setting-save", seq); found {
		return reply.code, reply.body, true, nil
	}
	if err := s.validateCostumeSettingLocked(ctx, setting); err != nil {
		return 0, nil, true, err
	}
	payload, err := json.Marshal(setting)
	if err != nil {
		return 0, nil, true, err
	}
	change := stateio.EntryMutation{Bucket: "costume_settings", Key: strconv.FormatUint(setting.CharacterIndex, 10), Payload: payload}
	core, err := s.corePayloadLocked()
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.storage.SaveWithEntries(ctx.State, "deck", core, []stateio.EntryMutation{change}); err != nil {
		return 0, nil, true, err
	}
	s.costumeSettings[setting.CharacterIndex] = setting
	s.rememberReplyLocked(ctx, "costume-setting-save", seq, 398, nil)
	return 398, nil, true, nil
}

func (s *Store) handleFieldSettings(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	if e := checkSeq(req); e != nil {
		return 0, nil, true, e
	}

	v, e := s.loadFieldSettings(ctx)
	if e != nil {
		return 0, nil, true, e
	}
	code := 71
	var body []byte
	if path == "/TalentSlotSave" {
		var ids []uint64
		e = wire.Walk(req, func(f wire.Field) error {
			if f.Number != 2 {
				return nil
			}
			if f.Type != 0 && f.Type != 2 {
				return fmt.Errorf("deck: invalid talent slots")
			}
			b := f.Value
			for len(b) > 0 {
				n, k := binary.Uvarint(b)
				if k <= 0 {
					return fmt.Errorf("deck: invalid packed talent slots")
				}
				ids = append(ids, n)
				if len(ids) > s.fieldSettingsDesign.TalentSlots {
					return fmt.Errorf("deck: too many talent slots")
				}
				b = b[k:]
			}
			return nil
		})
		if e != nil {
			return 0, nil, true, e
		}
		v.TalentIDs = ids
	} else {
		code = 372
		on, _, e := wire.Varint(req, 2)
		if e != nil || on > 1 {
			return 0, nil, true, fmt.Errorf("deck: invalid auto-recovery switch")
		}
		caster, _, e := wire.Varint(req, 3)
		if e != nil {
			return 0, nil, true, e
		}
		v.AutoRevive = on != 0
		v.Caster = caster
		body = wire.AppendVarint(nil, 1, on)
		body = wire.AppendVarint(body, 2, caster)
	}
	if e = s.validateFieldSettings(ctx, v, path == "/TalentSlotSave"); e != nil {
		return 0, nil, true, e
	}
	raw, e := json.Marshal(v)
	if e == nil {
		core, err := json.Marshal(s.state)
		if err != nil {
			return 0, nil, true, err
		}
		e = s.storage.SaveWithEntries(ctx.State, "deck", core, []stateio.EntryMutation{{Bucket: "field_settings", Key: "state", Payload: raw}})
	}
	return code, body, true, e
}

func checkSeq(req []byte) error {
	v, ok, e := wire.Varint(req, 1)
	if e != nil || !ok || v == 0 || v > 2147483647 {
		return errors.New("deck: invalid request sequence")
	}
	return nil
}

func triples(req []byte) ([]DeckEntry, error) {
	var out []DeckEntry
	e := wire.Walk(req, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 2 {
			return errors.New("deck: deck field")
		}
		a, aok, e := wire.Varint(f.Value, 1)
		if e != nil || !aok || a == 0 {
			return errors.New("deck: deck character")
		}
		position, _, e := wire.Varint(f.Value, 2)
		if e != nil || (position > 11 && position != ^uint64(0)) {
			return errors.New("deck: invalid deck position")
		}
		sequence, sequenceOK, e := wire.Varint(f.Value, 3)
		if e != nil || !sequenceOK || sequence == 0 || sequence > 5 {
			return errors.New("deck: invalid deck sequence")
		}
		out = append(out, DeckEntry{CharacterInvenIndex: a, CostumeInvenIndex: position, Slot: sequence})
		return nil
	})
	if e != nil {
		return nil, e
	}
	if len(out) == 0 {
		return nil, errors.New("deck: empty deck")
	}
	if e := validDeck(out); e != nil {
		return nil, e
	}
	return out, nil
}

func fieldEntries(req []byte) ([]FieldEntry, error) {
	var out []FieldEntry
	e := wire.Walk(req, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 2 {
			return errors.New("deck: field deck entry is not a message")
		}
		slot, slotOK, err := wire.Varint(f.Value, 1)
		if err != nil || !slotOK || slot == 0 {
			return errors.New("deck: invalid field deck slot")
		}
		character, characterOK, err := wire.Varint(f.Value, 2)
		if err != nil || !characterOK || character == 0 {
			return errors.New("deck: invalid field deck character")
		}
		costume, _, err := wire.Varint(f.Value, 3)
		if err != nil {
			return errors.New("deck: invalid field deck costume")
		}
		out = append(out, FieldEntry{Slot: slot, CharacterInvenIndex: character, CostumeInvenIndex: costume})
		return nil
	})
	if e != nil {
		return nil, e
	}
	if len(out) == 0 {
		return nil, errors.New("deck: empty field deck")
	}
	return out, validField(out)
}

func encodeDeck(xs []DeckEntry) []byte {
	var b []byte
	for _, x := range xs {
		v := wire.AppendVarint(nil, 1, x.CharacterInvenIndex)
		v = wire.AppendVarint(v, 2, x.CostumeInvenIndex)
		v = wire.AppendVarint(v, 3, x.Slot)
		b = wire.AppendBytes(b, 1, v)
	}
	return b
}

func encodeField(xs []FieldEntry) []byte {
	var b []byte
	for _, x := range xs {
		v := wire.AppendVarint(nil, 1, x.Slot)
		v = wire.AppendVarint(v, 2, x.CharacterInvenIndex)
		v = wire.AppendVarint(v, 3, x.CostumeInvenIndex)
		b = wire.AppendBytes(b, 1, v)
	}
	return b
}

func (s *Store) handleCostumeUse(ctx command.Context, req []byte) (int, []byte, bool, error) {
	fail := func(e error) (int, []byte, bool, error) { return 41, nil, true, e }
	if e := checkSeq(req); e != nil {
		return fail(e)
	}
	assignments := map[uint64]uint64{}
	e := wire.Walk(req, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 2 {
			return fmt.Errorf("deck: invalid costume use entry")
		}
		cost, _, e := wire.Varint(f.Value, 1)
		if e != nil || cost == 0 || cost > math.MaxInt64 {
			return fmt.Errorf("deck: invalid costume index")
		}
		char, _, e := wire.Varint(f.Value, 2)
		if e != nil || char == 0 || char > math.MaxInt64 {
			return fmt.Errorf("deck: invalid costume character")
		}
		if _, ok := assignments[char]; ok {
			return fmt.Errorf("deck: repeated costume character")
		}
		assignments[char] = cost
		return nil
	})
	if e != nil {
		return fail(e)
	}
	if len(assignments) == 0 {
		return fail(fmt.Errorf("deck: missing costume assignments"))
	}

	if s.characters != nil {
		if s.collection == nil {
			return fail(fmt.Errorf("deck: costume collection unavailable"))
		}
		for char, cost := range assignments {
			if _, ok := s.characters.Find(ctx, char); !ok {
				return fail(fmt.Errorf("deck: unknown costume character"))
			}
			c, ok := s.collection.CostumeByIndex(cost)
			if !ok || c.UseChar != char {
				return fail(fmt.Errorf("deck: costume not owned by character"))
			}
		}
		if _, e = s.characters.ApplyPresetCostumes(ctx, assignments); e != nil {
			return fail(e)
		}
	}
	n := clone(s.state)
	maps.Copy(n.Costumes, assignments)
	return 41, nil, true, s.commit(ctx, n)
}

func (s *Store) handleAutoRecovery(ctx command.Context, req []byte) (int, []byte, bool, error) {
	fail := func(e error) (int, []byte, bool, error) { return 373, nil, true, e }
	if e := checkSeq(req); e != nil {
		return fail(e)
	}
	caster, _, e := wire.Varint(req, 2)
	if e != nil {
		return fail(e)
	}
	seq, _, _ := wire.Varint(req, 1)

	key := ctx.SessionID + ":" + fmt.Sprint(seq)
	digest := fmt.Sprintf("%x", sha256.Sum256(req))
	if s.storage != nil {
		raw, ok, err := s.storage.LoadEntry(ctx.State, "deck", "auto_recovery", key)
		if err != nil {
			return fail(err)
		}
		if ok {
			var r autoRecoveryReceipt
			if err = json.Unmarshal(raw, &r); err != nil {
				return fail(err)
			}
			if r.Digest != digest {
				return fail(fmt.Errorf("deck: changed automatic recovery replay"))
			}
			return 373, r.Body, true, nil
		}
	}
	n := clone(s.state)
	if len(n.Deck) == 0 {
		return fail(fmt.Errorf("deck: automatic recovery has no battle deck"))
	}
	result := roster.AutoRecoveryResult{Caster: caster, Catalyst: n.AutoReviveCatalyst}
	if s.wallet != nil {
		result.Catalyst = s.wallet.CatalystBalance(ctx)
	}
	mode := uint64(2)
	var settings fieldSettings
	if s.characters != nil {
		settings, e = s.loadFieldSettings(ctx)
		if e != nil {
			return fail(e)
		}
		if caster != settings.Caster {
			return fail(fmt.Errorf("deck: automatic recovery caster differs from saved setting"))
		}
		if n.FieldCharControlDeckType == 2 {
			allExhausted := true
			for _, v := range n.Deck {
				hp, err := s.characters.CurrentHealth(ctx, v.CharacterInvenIndex)
				if err != nil {
					return fail(err)
				}
				if hp > 0 {
					allExhausted = false
				}
			}
			if allExhausted {
				mode = 3
			}
		}
		field := s.visibleFieldDeckLocked(ctx)
		var targets []uint64
		seen := map[uint64]bool{}
		add := func(id uint64) error {
			if seen[id] {
				return nil
			}
			seen[id] = true
			c, ok := s.characters.Find(ctx, id)
			if !ok || roster.IsStoryCharacter(c) || roster.IsCharmCharacter(c) {
				return nil
			}
			hp, err := s.characters.CurrentHealth(ctx, id)
			if err != nil {
				return err
			}
			if hp == 0 {
				targets = append(targets, id)
			}
			return nil
		}
		if n.FieldCharControlDeckType == 0 { //nolint:staticcheck // QF1003
			for _, v := range n.Deck {
				if e = add(v.CharacterInvenIndex); e != nil {
					return fail(e)
				}
			}
		} else if n.FieldCharControlDeckType == 1 {
			for _, v := range field {
				if e = add(v.CharacterInvenIndex); e != nil {
					return fail(e)
				}
			}
		}
		allowed := true
		if s.autoRecoveryAllowed != nil {
			allowed, e = s.autoRecoveryAllowed(ctx)
			if e != nil {
				return fail(e)
			}
		}
		if settings.AutoRevive && allowed && len(targets) > 0 {
			if s.autoRecovery == nil {
				return fail(fmt.Errorf("deck: automatic recovery executor unavailable"))
			}
			result, e = s.autoRecovery(ctx, seq, caster, targets)
			if e != nil {
				return fail(e)
			}
			settings.Caster = result.Caster
			if len(result.Characters) > 0 {
				mode = 1
			}
		}
		// On recovery failure, replace fatigued party members with living,
		// permanent owned characters in inventory order. Keep the fatigued
		// member when no replacement exists so the client can show exhaustion.
		if mode != 1 && len(targets) > 0 {
			all := s.characters.RawAll()
			sort.Slice(all, func(i, j int) bool { return all[i].InvenIndex < all[j].InvenIndex })
			used := map[uint64]bool{}
			for _, v := range n.Deck {
				used[v.CharacterInvenIndex] = true
			}
			for _, v := range field {
				used[v.CharacterInvenIndex] = true
			}
			replacements := map[uint64]roster.Character{}
			for _, id := range targets {
				for _, c := range all {
					if used[c.InvenIndex] || roster.IsStoryCharacter(c) || roster.IsCharmCharacter(c) {
						continue
					}
					hp, err := s.characters.CurrentHealth(ctx, c.InvenIndex)
					if err != nil {
						return fail(err)
					}
					if hp == 0 {
						continue
					}
					replacements[id] = c
					used[c.InvenIndex] = true
					break
				}
				if _, ok := replacements[id]; !ok {
					mode = 3
				}
			}
			for i, v := range n.Deck {
				if c, ok := replacements[v.CharacterInvenIndex]; ok {
					n.Deck[i].CharacterInvenIndex = c.InvenIndex
				}
			}
			for i, v := range n.FieldDeck {
				if c, ok := replacements[v.CharacterInvenIndex]; ok {
					n.FieldDeck[i].CharacterInvenIndex = c.InvenIndex
					n.FieldDeck[i].CostumeInvenIndex = c.UseCostume
				}
			}
		}
	} else if caster != 0 {
		return fail(fmt.Errorf("deck: automatic recovery character provider unavailable"))
	}
	var out []byte
	for _, v := range n.Deck {
		b := wire.AppendVarint(nil, 1, v.CharacterInvenIndex)
		b = wire.AppendVarint(b, 2, v.CostumeInvenIndex)
		b = wire.AppendVarint(b, 3, v.Slot)
		out = wire.AppendBytes(out, 1, b)
	}
	for _, v := range n.FieldDeck {
		b := wire.AppendVarint(nil, 1, v.Slot)
		b = wire.AppendVarint(b, 2, v.CharacterInvenIndex)
		b = wire.AppendVarint(b, 3, v.CostumeInvenIndex)
		out = wire.AppendBytes(out, 2, b)
	}
	for _, c := range result.Characters {
		out = wire.AppendVarint(out, 3, c.InvenIndex)
		out = wire.AppendBytes(out, 5, roster.CharacterWire(c))
	}
	out = wire.AppendVarint(out, 4, mode)
	if result.Caster != 0 {
		out = wire.AppendVarint(out, 9, result.Caster)
	}
	out = wire.AppendVarint(out, 6, result.Experience)
	out = wire.AppendVarint(out, 7, result.Catalyst)
	out = wire.AppendVarint(out, 8, result.Disabled)
	if s.storage != nil {
		core, err := json.Marshal(n)
		if err != nil {
			return fail(err)
		}
		raw, err := json.Marshal(autoRecoveryReceipt{digest, out})
		if err != nil {
			return fail(err)
		}
		changes := []stateio.EntryMutation{{Bucket: "auto_recovery", Key: key, Payload: raw}}
		if s.characters != nil {
			raw, err = json.Marshal(settings)
			if err != nil {
				return fail(err)
			}
			changes = append(changes, stateio.EntryMutation{Bucket: "field_settings", Key: "state", Payload: raw})
		}
		if err = s.storage.SaveWithEntries(ctx.State, "deck", core, changes); err != nil {
			return fail(err)
		}
	}
	s.state = n
	return 373, out, true, nil
}
