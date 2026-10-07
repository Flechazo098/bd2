package world

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

type fieldTrapState struct {
	Pack, Map, Trap int
	Enabled         bool
	Partial         []uint64
}
type fieldTrapRuntime struct {
	States   map[string]fieldTrapState
	Hits     map[string]int64
	Requests map[string]fieldMonsterReply
}

func loadFieldTrapRuntime(ctx command.Context) (fieldTrapRuntime, error) {
	v := fieldTrapRuntime{States: map[string]fieldTrapState{}, Hits: map[string]int64{}, Requests: map[string]fieldMonsterReply{}}
	raw, err := (stateio.EntrySnapshotStore{Domain: "missions", Bucket: "gameplay"}).Load(ctx.State, "field_traps")
	if err != nil || raw == nil {
		return v, err
	}
	if err := stateio.RequireExactJSONObject(raw, "States", "Hits", "Requests"); err != nil {
		return v, err
	}
	if err := json.Unmarshal(raw, &v); err != nil || v.States == nil || v.Hits == nil || v.Requests == nil {
		return v, fmt.Errorf("world: invalid trap runtime")
	}
	for key, state := range v.States {
		if state.Pack <= 0 || state.Map <= 0 || state.Trap <= 0 || key != trapKey(state.Pack, state.Map, state.Trap) {
			return v, fmt.Errorf("world: invalid saved trap identity %q", key)
		}
		seen := map[uint64]bool{}
		for _, id := range state.Partial {
			if id == 0 || id > 0x7fffffff || seen[id] {
				return v, fmt.Errorf("world: invalid saved trap switch %q", key)
			}
			seen[id] = true
		}
	}
	for key, hit := range v.Hits {
		var pack, mapID, id int
		if n, err := fmt.Sscanf(key, "%d/%d/%d", &pack, &mapID, &id); err != nil || n != 3 || pack <= 0 || mapID <= 0 || id <= 0 || key != trapKey(pack, mapID, id) || hit <= 0 {
			return v, fmt.Errorf("world: invalid saved trap hit %q", key)
		}
	}
	for key, reply := range v.Requests {
		if key == "" || len(reply.Request) == 0 {
			return v, fmt.Errorf("world: invalid saved trap reply %q", key)
		}
		if err := wire.Walk(reply.Request, func(wire.Field) error { return nil }); err != nil {
			return v, err
		}
		if err := wire.Walk(reply.Response, func(wire.Field) error { return nil }); err != nil {
			return v, err
		}
	}
	return v, nil
}
func saveFieldTrapRuntime(ctx command.Context, v fieldTrapRuntime) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return (stateio.EntrySnapshotStore{Domain: "missions", Bucket: "gameplay"}).Save(ctx.State, "field_traps", raw)
}
func trapKey(pack, mapID, id int) string { return fmt.Sprintf("%d/%d/%d", pack, mapID, id) }

func (s *Service) trapQuestEligible(pack int, quests []uint64) bool {
	if len(quests) == 0 || quests[0] == 0 {
		return true
	}
	for _, quest := range quests {
		id := int(quest)
		if id == s.firstUnclearedQuestFor(pack) {
			return true
		}
		if _, active := s.state.QuestInPack(id, pack, s.questDifficultyFor(pack, id)); active && !s.state.QuestCleared(id, pack, s.questDifficultyFor(pack, id)) {
			return true
		}
	}
	return false
}

func (s *Service) handleFieldTraps(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	code := 63
	switch path {
	case "/FieldTrapInfo":
		code = 171
	case "/InteractionTrigger":
		code = 172
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > 0x7fffffff {
		return code, nil, true, fmt.Errorf("%w: %s invalid sequence", ErrInvalidRequest, path)
	}
	id, found, err := wire.Varint(request, 2)
	if err != nil || !found || id == 0 || id > 0x7fffffff {
		return code, nil, true, fmt.Errorf("%w: %s invalid object id", ErrInvalidRequest, path)
	}
	v, err := loadFieldTrapRuntime(ctx)
	if err != nil {
		return code, nil, true, err
	}
	identity := ""
	if path != "/FieldTrapInfo" {
		if ctx.SessionID == "" {
			return code, nil, true, fmt.Errorf("%w: %s trap/trigger %d missing session", ErrInvalidRequest, path, id)
		}
		identity = fmt.Sprintf("%s:%s:%d", path, ctx.SessionID, seq)
		if prior, found := v.Requests[identity]; found {
			if !bytes.Equal(prior.Request, request) {
				return code, nil, true, fmt.Errorf("%w: %s trap/trigger %d changed retry", ErrInvalidRequest, path, id)
			}
			return code, prior.Response, true, nil
		}
	}
	pack, err := s.CurrentPackID(ctx)
	if path == "/FieldTrapInfo" {
		pack = int(id)
		err = nil
	}
	if err != nil {
		return code, nil, true, err
	}
	mapID := 0
	if position, found := s.state.Position(); found && position.PackID == pack {
		mapID = position.Position.MapID
	}
	fail := func(reason string) (int, []byte, bool, error) {
		return code, nil, true, fmt.Errorf("%w: %s pack %d map %d trap/trigger %d %s", ErrInvalidRequest, path, pack, mapID, id, reason)
	}
	if !s.packUnlocked(ctx, pack) {
		return fail("pack unavailable")
	}
	if s.trapLoader == nil {
		return fail("trap design unavailable")
	}
	design, err := s.trapLoader(pack)
	if err != nil {
		return code, nil, true, fmt.Errorf("world: %s pack %d map %d trap/trigger %d load design: %w", path, pack, mapID, id, err)
	}
	if path == "/FieldTrapInfo" {
		requested, _, err := wire.Varint(request, 3)
		if err != nil || requested > 0x7fffffff {
			return fail("invalid map filter")
		}
		if requested != 0 && !slices.Contains(design.MapIDs, int(requested)) {
			return fail(fmt.Sprintf("requested map %d outside pack", requested))
		}
		var keys []string
		for key, state := range v.States {
			if state.Pack == pack && (requested == 0 || state.Map == int(requested)) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		var response []byte
		for _, key := range keys {
			state := v.States[key]
			trap, found := design.Traps[state.Trap]
			if !found || trap.ResetType != 0 || !slices.Contains(trap.Maps, state.Map) {
				return fail("saved persistent trap absent from design")
			}
			row := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, uint64(pack)), 2, uint64(state.Map)), 3, uint64(state.Trap))
			if state.Enabled {
				row = wire.AppendVarint(row, 4, 1)
			}
			for _, id := range state.Partial {
				row = wire.AppendVarint(row, 5, id)
			}
			response = wire.AppendBytes(response, 1, row)
		}
		return code, response, true, nil
	}
	finish := func(response []byte) (int, []byte, bool, error) {
		v.Requests[identity] = fieldMonsterReply{Request: slices.Clone(request), Response: slices.Clone(response)}
		if err := saveFieldTrapRuntime(ctx, v); err != nil {
			return code, nil, true, err
		}
		return code, response, true, nil
	}
	if !slices.Contains(design.MapIDs, mapID) {
		return fail("current position missing or outside pack")
	}
	if s.battleActive != nil && s.battleActive(ctx) {
		return fail("active battle")
	}
	var response []byte
	if path == "/InteractionTrigger" {
		trigger, found := design.Triggers[int(id)]
		if !found || !slices.Contains(trigger.Maps, mapID) {
			return fail("trigger not placed in current map")
		}
		if trigger.Type != 1 || trigger.AutoInteraction != 0 || !s.trapQuestEligible(pack, trigger.QuestRange) {
			return fail("trigger is not an available manual trap switch")
		}
		changed := false
		for _, sw := range design.Switches {
			if sw.ObjectType != 15 || !slices.Contains(sw.Objects, id) {
				continue
			}
			var targets []gamedata.FieldTrap
			for _, trap := range design.Traps {
				if trap.SwitchID == sw.ID && trap.ResetType == 0 && slices.Contains(trap.Maps, mapID) {
					targets = append(targets, trap)
				}
			}
			if len(targets) == 0 {
				continue
			}
			partial := v.States[trapKey(pack, mapID, targets[0].ID)].Partial
			if sw.OrderType == 1 && (len(partial) >= len(sw.Objects) || sw.Objects[len(partial)] != id) {
				return fail("switch activation order does not match design")
			}
			if !slices.Contains(partial, id) {
				partial = append(slices.Clone(partial), id)
			}
			complete := true
			for _, required := range sw.Objects {
				complete = complete && slices.Contains(partial, required)
			}
			for _, trap := range targets {
				key := trapKey(pack, mapID, trap.ID)
				state, found := v.States[key]
				if !found {
					state = fieldTrapState{Pack: pack, Map: mapID, Trap: trap.ID, Enabled: trap.DefaultEnabled}
				}
				state.Partial = slices.Clone(partial)
				if complete {
					state.Enabled = !state.Enabled
					state.Partial = nil
				}
				v.States[key] = state
			}
			changed = true
		}
		if !changed {
			return fail("trigger has no persistent trap switch in current map")
		}
	} else {
		trap, found := design.Traps[int(id)]
		if !found || !slices.Contains(trap.Maps, mapID) {
			return fail("trap not placed in current map")
		}
		if !s.trapQuestEligible(pack, trap.QuestRange) {
			return fail("trap quest range inactive")
		}
		if state, found := v.States[trapKey(pack, mapID, int(id))]; found && trap.ResetType == 0 && !state.Enabled {
			return finish(nil)
		}
		key := trapKey(pack, mapID, int(id))
		now := s.monsterTime().UnixMilli()
		if last, found := v.Hits[key]; found && now-last < int64(trap.CoolSeconds)*1000 {
			return finish(nil)
		}
		if trap.FieldBuff != 0 && (s.decks == nil || s.decks.FieldControlType() != 2 || !design.StoryModeImmune) {
			if s.monsterDamage == nil {
				return fail("field health runtime unavailable")
			}
			rows, err := s.monsterDamage(ctx, pack, trap.FieldBuff, identity)
			if err != nil {
				return code, nil, true, fmt.Errorf("world: TrapDamage pack %d map %d trap %d buff %d: %w", pack, mapID, id, trap.FieldBuff, err)
			}
			for _, row := range rows {
				response = wire.AppendBytes(response, 1, row)
			}
		}
		v.Hits[key] = now
	}
	return finish(response)
}
