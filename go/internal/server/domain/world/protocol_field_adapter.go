package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/protocol/wire"
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

func nestedMessages(b []byte, number int) ([][]byte, error) {
	var out [][]byte
	e := wire.Walk(b, func(f wire.Field) error {
		if f.Number == number {
			if f.Type != 2 {
				return ErrInvalidRequest
			}
			out = append(out, append([]byte(nil), f.Value...))
		}
		return nil
	})
	return out, e
}

func (s *Service) handleOverwhelm(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, present, e := wire.Varint(request, 1)
	if e != nil || !present || seq == 0 || seq > 0x7fffffff || ctx.SessionID == "" {
		return 275, nil, true, ErrInvalidRequest
	}
	pack, e := s.CurrentPackID(ctx)
	if e != nil {
		return 275, nil, true, e
	}
	v, e := s.loadMonsterState(ctx)
	if e != nil {
		return 275, nil, true, e
	}
	identity := fmt.Sprintf("overwhelm:%s:%d", ctx.SessionID, seq)
	if prior, ok := v.Requests[identity]; ok {
		if !bytes.Equal(prior.Request, request) {
			return 275, nil, true, ErrInvalidRequest
		}
		return 275, prior.Response, true, nil
	}
	raw, e := nestedMessages(request, 2)
	if e != nil || len(raw) == 0 || len(raw) > 4096 {
		return 275, nil, true, ErrInvalidRequest
	}
	var targets []overwhelmedMonster
	seen := map[uint64]bool{}
	for _, b := range raw {
		var m overwhelmedMonster
		for f, dst := range map[int]*uint64{1: &m.Group, 2: &m.ID, 3: &m.Deck, 4: &m.Mode} {
			*dst, _, e = wire.Varint(b, f)
			if e != nil || *dst > 0x7fffffff {
				return 275, nil, true, ErrInvalidRequest
			}
		}
		if m.ID == 0 || seen[m.ID] {
			return 275, nil, true, ErrInvalidRequest
		}
		seen[m.ID] = true
		if m.Mode == 5 {
			definition, found, x := s.findFieldMonster(pack, int(m.ID))
			if x != nil || !found || definition.UseBattleSkip != 1 || definition.Type >= 2 {
				return 275, nil, true, fmt.Errorf("world: hunting monster cannot be overwhelmed")
			}
			if s.overwhelmHunting == nil {
				return 275, nil, true, fmt.Errorf("world: hunting overwhelm runtime missing")
			}
			if e = s.overwhelmHunting.ValidateBattle(ctx, pack, 5, m.ID, m.Deck); e != nil {
				return 275, nil, true, e
			}
		} else if gamedata.IsSkyWayMode(m.Mode) {
			definition, found, e := s.findFieldMonster(pack, int(m.ID))
			if e != nil || !found || definition.UseBattleSkip != 1 {
				return 275, nil, true, fmt.Errorf("world: SkyWay monster cannot be overwhelmed")
			}
			m.Instance, e = s.SkyWayBeginBattle(ctx, pack, m.Mode, m.ID, m.Deck)
			if e != nil {
				return 275, nil, true, e
			}
			m.Definition = definition
		} else {
			if m.Mode != 1 && m.Mode != 2 && m.Mode != 4 {
				return 275, nil, true, fmt.Errorf("world: unavailable overwhelm battle mode %d", m.Mode)
			}
			definition, found, e := s.findFieldMonster(pack, int(m.ID))
			if e != nil || !found || (definition.UseBattleSkip != 1 && definition.Type != 3) {
				return 275, nil, true, ErrInvalidRequest
			}
			m.Definition = definition
			if definition.Type == 2 || definition.Type == 4 {
				return 275, nil, true, fmt.Errorf("world: private field monster cannot be overwhelmed")
			}
			if e = s.authorizeMonsterMap(ctx, pack, int(m.ID)); e != nil {
				return 275, nil, true, e
			}
			validDeck := definition.Type == 3 && m.Deck == 0
			for _, d := range definition.BattleDecks {
				if d == m.Deck {
					validDeck = true
				}
			}
			if definition.BattleDeck == m.Deck {
				validDeck = true
			}
			if !validDeck {
				return 275, nil, true, fmt.Errorf("world: overwhelm deck mismatch")
			}
			if m.Mode == 2 {
				if int(m.Group) != definition.GroupID || definition.GroupID == 0 || !s.monsterEligible(pack, definition) {
					return 275, nil, true, ErrInvalidRequest
				}
				state, e := s.monsterState(&v, pack, definition)
				if e != nil {
					return 275, nil, true, e
				}
				if state.Defeated || state.Respawn > s.monsterTime().UnixMilli() {
					return 275, nil, true, fmt.Errorf("world: overwhelm monster not spawned")
				}
				m.Instance = fmt.Sprintf("fieldmonster:%s:%d", monsterKey(pack, definition.ID), state.Generation)
			} else {
				if m.Group != 0 {
					return 275, nil, true, ErrInvalidRequest
				}
				m.Instance = fmt.Sprintf("questmonster:%d:%d:%d", pack, s.questDifficulty(pack), m.ID)
			}
		}
		targets = append(targets, m)
	}
	quests, e := nestedMessages(request, 3)
	if e != nil {
		return 275, nil, true, e
	}
	var updates [][]byte
	var updated []uint64
	for _, b := range quests {
		quest, _, e := wire.Varint(b, 1)
		if e != nil {
			return 275, nil, true, e
		}
		qp, _, e := wire.Varint(b, 2)
		if e != nil || int(qp) != pack || quest == 0 || !s.canClear(ctx, pack, int(quest)) {
			return 275, nil, true, ErrInvalidRequest
		}
		values, e := intsRequest(b, 3)
		if e != nil || len(values) == 0 {
			return 275, nil, true, ErrInvalidRequest
		}
		if e = s.validateOverwhelmQuest(ctx, pack, int(quest), values, targets); e != nil {
			return 275, nil, true, e
		}
		req := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, seq), 2, quest), 3, qp)
		for _, value := range values {
			if value > 0x7fffffff {
				return 275, nil, true, ErrInvalidRequest
			}
			req = wire.AppendVarint(req, 4, value)
		}
		updates = append(updates, req)
		updated = append(updated, quest)
	}
	if s.overwhelmAuthorize == nil {
		return 275, nil, true, fmt.Errorf("world: overwhelm requires successful talent use")
	}
	if e = s.overwhelmAuthorize(ctx, identity, uint64(len(targets))); e != nil {
		return 275, nil, true, e
	}
	var response, bundle []byte
	for _, m := range targets {
		if m.Mode == 5 {
			reward, monsters, e := s.overwhelmHunting.CompleteBattle(ctx, pack, 5, m.ID, m.Deck, fmt.Sprintf("%s:%d", identity, m.ID))
			if e != nil {
				return 275, nil, true, e
			}
			bundle = append(bundle, reward...)
			for _, row := range monsters {
				response = wire.AppendBytes(response, 1, row)
			}
		} else if gamedata.IsSkyWayMode(m.Mode) {
			reward, bonus, monsters, e := s.SkyWayCompleteBattle(ctx, pack, m.Mode, m.ID, m.Deck, m.Instance, fmt.Sprintf("%s:%d", identity, m.ID))
			if e != nil {
				return 275, nil, true, e
			}
			bundle = append(bundle, reward...)
			bundle = append(bundle, bonus...)
			for _, row := range monsters {
				response = wire.AppendBytes(response, 1, row)
			}
		} else {
			if !v.Claims[m.Instance] && m.Definition.Type != 3 {
				definition := m.Definition
				definition.BattleDeck = m.Deck
				var reward []byte
				var e error
				if len(m.Costs) > 0 {
					if s.monsterRewards == nil || s.researchEconomy == nil {
						return 275, nil, true, ErrInvalidRequest
					}
					rs, x := s.monsterRewards(pack, m.Deck)
					if x != nil {
						return 275, nil, true, x
					}
					rewards := make([]gamedata.Reward, 0, len(rs))
					for _, r := range rs {
						rewards = append(rewards, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count}) //nolint:staticcheck // S1016
					}
					reward, e = s.researchEconomy.Apply(ctx, m.Instance, m.Costs, rewards)
				} else {
					reward, e = s.grantFieldMonster(ctx, pack, definition, m.Instance)
				}
				if e != nil {
					return 275, nil, true, e
				}
				bundle = append(bundle, reward...)
			}
			v.Claims[m.Instance] = true
			if m.Definition.GroupID > 0 {
				state, e := s.monsterState(&v, pack, m.Definition)
				if e != nil {
					return 275, nil, true, e
				}
				state.Defeated = true
				state.Respawn = s.nextMonsterSpawn(m.Definition)
				v.Monsters[monsterKey(pack, int(m.ID))] = state
				response = wire.AppendBytes(response, 1, monsterWire(m.Definition, state, true))
			}
		}
	}
	for i, req := range updates {
		if _, _, _, e = s.handleQuestUpdate(ctx, req); e != nil {
			return 275, nil, true, e
		}
		response = wire.AppendVarint(response, 2, updated[i])
	}
	response = wire.AppendBytes(response, 3, bundle)
	v.Requests[identity] = fieldMonsterReply{Request: append([]byte(nil), request...), Response: response}
	if e = s.saveMonsterState(ctx, v); e != nil {
		return 275, nil, true, e
	}
	return 275, response, true, nil
}

func monsterWire(m gamedata.FieldMonsterDesign, state fieldMonsterState, eligible bool) []byte {
	b := wire.AppendVarint(nil, 1, uint64(m.ID))
	if m.BattleDeck != 0 {
		b = wire.AppendVarint(b, 2, m.BattleDeck)
	}
	if state.Respawn > 0 {
		b = wire.AppendVarint(b, 3, uint64(state.Respawn))
	}
	if state.LifeEnd > 0 {
		b = wire.AppendVarint(b, 4, uint64(state.LifeEnd))
	}
	b = wire.AppendVarint(b, 5, uint64(m.GroupID))
	if eligible {
		b = wire.AppendVarint(b, 6, 1)
	}
	return b
}

func (s *Service) monsterRows(ctx command.Context, pack int, filter map[int]bool) ([][]byte, error) {
	if s.monsterLoader == nil {
		return nil, fmt.Errorf("%w: missing field monster design", ErrInvalidRequest)
	}
	design, e := s.monsterLoader(pack)
	if e != nil {
		return nil, e
	}
	v, e := s.loadMonsterState(ctx)
	if e != nil {
		return nil, e
	}
	var rows [][]byte
	changed := false
	for _, m := range design {
		if m.GroupID == 0 {
			continue
		}
		if filter != nil && !filter[m.ID] {
			continue
		}
		eligible := s.monsterEligible(pack, m)
		if eligible {
			available, err := s.rewardMonsterAvailable(ctx, pack, m.ID)
			if err != nil {
				return nil, err
			}
			eligible = available
		}
		state := fieldMonsterState{}
		if eligible {
			state, e = s.monsterState(&v, pack, m)
			if e != nil {
				return nil, e
			}
			changed = true
		}
		rows = append(rows, monsterWire(m, state, eligible))
	}
	if changed && s.monsterStore != nil {
		if e = s.saveMonsterState(ctx, v); e != nil {
			return nil, e
		}
	}
	return rows, nil
}

func (s *Service) handleMonsterInfo(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, present, e := wire.Varint(request, 1)
	if e != nil || !present || seq == 0 || seq > 0x7fffffff {
		return 0, nil, true, ErrInvalidRequest
	}
	groups, e := intsRequest(request, 2)
	if e != nil {
		return 0, nil, true, e
	}
	wanted := map[int]bool{}
	for _, g := range groups {
		if g == 0 || g > 0x7fffffff {
			return 0, nil, true, ErrInvalidRequest
		}
		wanted[int(g)] = true
	}
	pack, e := s.CurrentPackID(ctx)
	if e != nil || !s.packUnlocked(ctx, pack) || s.monsterLoader == nil {
		return 0, nil, true, ErrInvalidRequest
	}
	if s.skyway != nil && pack == s.skyway.design.Pack {
		v, err := s.skyway.load(ctx)
		if err != nil {
			return 51, nil, true, err
		}
		stage, ok := s.skyway.design.Stage(v.Run.Group, v.Run.ID)
		if !ok {
			return 51, nil, true, nil
		}
		var out []byte
		for _, row := range s.skyway.monsters(stage, v.Run) {
			out = wire.AppendBytes(out, 1, row)
		}
		return 51, out, true, nil
	}
	design, e := s.monsterLoader(pack)
	if e != nil {
		return 0, nil, true, e
	}
	filter := map[int]bool{}
	for _, m := range design {
		if wanted[m.GroupID] {
			filter[m.ID] = true
		}
	}
	rows, e := s.monsterRows(ctx, pack, filter)
	if e != nil {
		return 0, nil, true, e
	}
	var b []byte
	for _, r := range rows {
		b = wire.AppendBytes(b, 1, r)
	}
	return 51, b, true, nil
}

func (s *Service) CompleteFieldMonsterBattle(ctx command.Context, pack int, id uint64, instance string) ([]byte, error) {
	m, found, e := s.findFieldMonster(pack, int(id))
	if e != nil {
		return nil, e
	}
	if !found {
		return nil, ErrInvalidRequest
	}
	v, e := s.loadMonsterState(ctx)
	if e != nil {
		return nil, e
	}
	key := monsterKey(pack, m.ID)
	state, exists := v.Monsters[key]
	if !exists {
		return nil, ErrInvalidRequest
	}
	if !v.Claims[instance] {
		if instance != fmt.Sprintf("fieldmonster:%s:%d", key, state.Generation) {
			return nil, fmt.Errorf("world: stale monster battle")
		}
		state.Defeated = true
		state.Respawn = s.nextMonsterSpawn(m)
		v.Monsters[key] = state
		v.Claims[instance] = true
		if e = s.saveMonsterState(ctx, v); e != nil {
			return nil, e
		}
	}
	return monsterWire(m, state, s.monsterEligible(pack, m)), nil
}

func (s *Service) handleFieldMonsterRegen(ctx command.Context, request []byte) (int, []byte, bool, error) {
	id, e := requestPack(request)
	if e != nil {
		return 0, nil, true, e
	}
	pack, e := s.CurrentPackID(ctx)
	if e != nil || !s.packUnlocked(ctx, pack) {
		return 0, nil, true, ErrInvalidRequest
	}
	m, found, e := s.findFieldMonster(pack, id)
	if e != nil || !found {
		return 0, nil, true, ErrInvalidRequest
	}
	v, e := s.loadMonsterState(ctx)
	if e != nil {
		return 0, nil, true, e
	}
	seq, _, _ := wire.Varint(request, 1)
	identity := fmt.Sprintf("regen:%s:%d", ctx.SessionID, seq)
	if ctx.SessionID == "" {
		return 0, nil, true, fmt.Errorf("world: missing monster session")
	}
	if reply, ok := v.Requests[identity]; ok {
		if !bytes.Equal(request, reply.Request) {
			return 0, nil, true, ErrInvalidRequest
		}
		return 139, reply.Response, true, nil
	}
	state, e := s.monsterState(&v, pack, m)
	if e != nil {
		return 0, nil, true, e
	}
	if !state.Defeated {
		state.Defeated = true
		state.Respawn = s.nextMonsterSpawn(m)
		v.Monsters[monsterKey(pack, m.ID)] = state
	}
	response := wire.AppendBytes(nil, 1, monsterWire(m, state, s.monsterEligible(pack, m)))
	v.Requests[identity] = fieldMonsterReply{Request: append([]byte(nil), request...), Response: response}
	if e = s.saveMonsterState(ctx, v); e != nil {
		return 0, nil, true, e
	}
	return 139, response, true, nil
}

func fieldObjectInt(raw []byte, field int, required bool) (int, error) {
	v, found, err := wire.Varint(raw, field)
	if err != nil || required && (!found || v == 0) || v > uint64(^uint32(0)>>1) {
		return 0, ErrInvalidRequest
	}
	return int(v), nil
}

// The client batches LostCoin collections. Validate the complete list before
// drawing or settling any component; the dispatcher commits the whole request.
func (s *Service) handleFieldObjectRewardList(ctx command.Context, request []byte) (int, []byte, bool, error) {
	pack, err := requestPack(request)
	if err != nil {
		return 260, nil, true, err
	}
	design, err := s.fieldObjectDesign(ctx, pack)
	if err != nil {
		return 260, nil, true, err
	}
	type selection struct{ group, id int }
	var selections []selection
	seen := map[int]bool{}
	err = wire.Walk(request, func(f wire.Field) error {
		if f.Number != 3 {
			return nil
		}
		if f.Type != 2 || len(selections) >= 100 {
			return ErrInvalidRequest
		}
		group, e := fieldObjectInt(f.Value, 3, true)
		if e != nil {
			return e
		}
		id, e := fieldObjectInt(f.Value, 4, true)
		if e != nil {
			return e
		}
		obj, ok := design.Objects[id]
		if !ok || obj.GroupID != group || obj.BuffID != 0 || obj.MonsterID != 0 || len(obj.Rewards) == 0 && obj.Type != 6 {
			return ErrInvalidRequest
		}
		if !seen[id] {
			seen[id] = true
			selections = append(selections, selection{group, id})
		}
		return nil
	})
	if err != nil || len(selections) == 0 || !s.packUnlocked(ctx, pack) || !s.fieldObjectCurrentPack(pack) {
		return 260, nil, true, ErrInvalidRequest
	}
	var bundle []byte
	for _, selection := range selections {
		part, e := s.openFieldObject(ctx, pack, selection.group, selection.id)
		if e != nil {
			return 260, nil, true, e
		}
		bundle = append(bundle, part...)
	}
	return 260, wire.AppendBytes(nil, 1, bundle), true, nil
}

func (s *Service) handleFieldObjectPreview(ctx command.Context, request []byte) (int, []byte, bool, error) {
	pack, err := requestPack(request)
	if err != nil || !s.packUnlocked(ctx, pack) {
		return 144, nil, true, ErrInvalidRequest
	}
	ids, err := s.openedFieldObjects(ctx, pack)
	if err != nil {
		return 144, nil, true, err
	}
	var response []byte
	for _, id := range ids {
		response = wire.AppendBytes(response, 1, wire.AppendVarint(nil, 1, uint64(id)))
	}
	research, err := s.state.ResearchObjects(ctx, pack)
	if err != nil {
		return 144, nil, true, err
	}
	for _, id := range research {
		response = wire.AppendVarint(response, 2, uint64(id))
	}
	return 144, response, true, nil
}

// Respawn is a projection of the reset schedule, never an instruction to clear
// receipts. A request before the reset cannot make an object collectible again.
func (s *Service) handleFieldObjectRespawn(ctx command.Context, request []byte) (int, []byte, bool, error) {
	pack, err := requestPack(request)
	if err != nil || !s.packUnlocked(ctx, pack) {
		return 30, nil, true, ErrInvalidRequest
	}
	group, err := fieldObjectInt(request, 3, true)
	if err != nil {
		return 30, nil, true, err
	}
	kind, err := fieldObjectInt(request, 4, false)
	if err != nil || kind != 0 && kind != 6 {
		return 30, nil, true, ErrInvalidRequest
	}
	design, err := s.fieldObjectDesign(ctx, pack)
	if err != nil {
		return 30, nil, true, err
	}
	var ids []int
	reset := -1
	for id, obj := range design.Objects {
		if obj.GroupID == group {
			ids = append(ids, id)
			if reset != -1 && reset != obj.ResetType {
				return 30, nil, true, fmt.Errorf("world: inconsistent field reset group")
			}
			reset = obj.ResetType
		}
	}
	if len(ids) == 0 {
		return 30, nil, true, ErrInvalidRequest
	}
	sort.Ints(ids)
	var response []byte
	for _, id := range ids {
		obj := design.Objects[id]
		period, e := s.fieldObjectPeriodFor(pack, obj)
		if e != nil {
			return 30, nil, true, e
		}
		opened, e := s.state.FieldRewardOpened(ctx, pack, id, period)
		if e != nil {
			return 30, nil, true, e
		}
		if opened {
			response = wire.AppendBytes(response, 1, wire.AppendVarint(nil, 1, uint64(id)))
		}
	}
	if reset == 0 || reset == 3 { //nolint:staticcheck // QF1003
		next, e := s.fieldReset.Next(reset, s.monsterTime())
		if e != nil {
			return 30, nil, true, e
		}
		row := wire.AppendVarint(nil, 1, uint64(group))
		row = wire.AppendVarint(row, 2, uint64(next.UnixMilli()))
		response = wire.AppendBytes(response, 2, row)
	} else if reset == 2 {
		resolver, ok := s.eventFieldPacks.(interface {
			FieldObjectEventPeriod(int) (string, int64, error)
		})
		if !ok {
			return 30, nil, true, ErrInvalidRequest
		}
		_, end, err := resolver.FieldObjectEventPeriod(pack)
		if err != nil {
			return 30, nil, true, err
		}
		row := wire.AppendVarint(wire.AppendVarint(nil, 1, uint64(group)), 2, uint64(end))
		response = wire.AppendBytes(response, 2, row)
	}
	return 30, response, true, nil
}

func (s *Service) openFieldObjectResponse(ctx command.Context, pack, group, id int) ([]byte, error) {
	design, err := s.fieldObjectDesign(ctx, pack)
	if err != nil {
		return nil, err
	}
	obj, ok := design.Objects[id]
	if !ok || obj.GroupID != group || !s.packUnlocked(ctx, pack) || !s.fieldObjectCurrentPack(pack) {
		return nil, ErrInvalidRequest
	}
	if err := s.validateFieldObjectMap(ctx, pack, obj.MapID); err != nil {
		return nil, err
	}
	period, err := s.fieldObjectPeriodFor(pack, obj)
	if err != nil {
		return nil, err
	}
	opened, err := s.state.FieldRewardOpened(ctx, pack, id, period)
	if err != nil {
		return nil, err
	}
	if opened {
		return wire.AppendBytes(nil, 1, nil), nil
	}
	var effects []byte
	if obj.BuffID != 0 {
		buff, exists := s.fieldBuffs[uint64(obj.BuffID)]
		if !exists {
			return nil, fmt.Errorf("world: missing field object buff %d", obj.BuffID)
		}
		row, chars, e := s.applyFieldObjectBuff(ctx, pack, buff)
		if e != nil {
			return nil, e
		}
		effects = wire.AppendBytes(effects, 2, row)
		for _, char := range chars {
			effects = wire.AppendBytes(effects, 3, char)
		}
	} else if obj.MonsterID != 0 && len(obj.Rewards) == 0 {
		if s.monsterLoader == nil || s.monsterStore == nil {
			return nil, fmt.Errorf("world: dynamic field monster runtime unavailable")
		}
		monsters, e := s.monsterLoader(pack)
		if e != nil {
			return nil, e
		}
		found := false
		for _, monster := range monsters {
			if monster.ID != obj.MonsterID {
				continue
			}
			if !s.monsterEligible(pack, monster) {
				return nil, ErrInvalidRequest
			}
			snapshot, e := s.loadMonsterState(ctx)
			if e != nil {
				return nil, e
			}
			state, e := s.monsterState(&snapshot, pack, monster)
			if e != nil {
				return nil, e
			}
			if e = s.saveMonsterState(ctx, snapshot); e != nil {
				return nil, e
			}
			effects = wire.AppendBytes(effects, 4, monsterWire(monster, state, true))
			found = true
			break
		}
		if !found {
			return nil, fmt.Errorf("world: missing field object monster %d", obj.MonsterID)
		}
	}
	if obj.BuffID != 0 {
		if err := s.state.MarkFieldRewardOpened(ctx, pack, id, period); err != nil {
			return nil, err
		}
		return append(wire.AppendBytes(nil, 1, nil), effects...), nil
	}
	bundle, err := s.openFieldObject(ctx, pack, group, id)
	if err != nil {
		return nil, err
	}
	return append(wire.AppendBytes(nil, 1, bundle), effects...), nil
}

func (s *Service) applyFieldObjectBuff(ctx command.Context, pack int, buff gamedata.FieldBuffDesign) ([]byte, [][]byte, error) {
	if buff.ID == 0 || buff.Type > 5 || buff.TargetType > 2 || math.IsNaN(buff.Value) || math.IsInf(buff.Value, 0) || buff.Value < 0 || math.IsNaN(buff.Time) || math.IsInf(buff.Time, 0) || buff.Time < 0 {
		return nil, nil, fmt.Errorf("world: invalid field object buff")
	}
	row := wire.AppendVarint(nil, 1, buff.ID)
	if buff.Type <= 1 {
		if buff.Time <= 0 || buff.Time > float64(^uint32(0)>>1) {
			return nil, nil, fmt.Errorf("world: invalid persistent field buff duration")
		}
		if buff.Type == 0 {
			row = wire.AppendVarint(row, 2, uint64(buff.Time))
		} else {
			row = wire.AppendVarint(row, 3, uint64(s.monsterTime().UnixMilli()+int64(buff.Time*1000)))
		}
		prior, err := s.state.FieldBuffs(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, raw := range prior {
			id, _, e := wire.Varint(raw, 1)
			if e != nil {
				return nil, nil, e
			}
			old, found := s.fieldBuffs[id]
			if !found {
				return nil, nil, fmt.Errorf("world: saved field buff absent from design")
			}
			if old.Type == buff.Type {
				if e := s.state.RemoveFieldBuff(ctx, id); e != nil {
					return nil, nil, e
				}
			}
		}
		if err := s.state.SaveFieldBuff(ctx, buff.ID, row); err != nil {
			return nil, nil, err
		}
		return row, nil, nil
	}
	if buff.Type == 4 || buff.Type == 5 {
		chars, err := s.applyMonsterFieldDamage(ctx, pack, buff.ID, "")
		return row, chars, err
	}
	if s.characters == nil || s.decks == nil {
		return nil, nil, fmt.Errorf("world: field healing runtime unavailable")
	}
	indices, err := s.fieldBuffTargets(ctx, pack, buff.TargetType)
	if err != nil {
		return nil, nil, err
	}
	var chars [][]byte
	for _, index := range indices {
		c, found := s.characters.Find(ctx, index)
		if !found {
			return nil, nil, fmt.Errorf("world: field healing character missing")
		}
		hp, err := s.characters.CurrentHealth(ctx, index)
		if err != nil {
			return nil, nil, err
		}
		if hp == 0 {
			continue
		}
		max, err := s.characters.MaxHealth(ctx, index)
		if err != nil {
			return nil, nil, err
		}
		amount := buff.Value
		if buff.Type == 3 {
			amount *= float64(max)
		}
		remaining := max
		if hp < max && amount < float64(max-hp) {
			remaining = hp + uint64(amount)
		}
		if err = s.characters.SetCurrentHealth(ctx, index, remaining); err != nil {
			return nil, nil, err
		}
		c.HP = remaining
		chars = append(chars, roster.CharacterWire(c))
	}
	return row, chars, nil
}

// ConsumeFieldBattleBuff runs inside the successful BattleEnter transaction.
// A persisted battle identity makes retries and reconnects safe.
func (s *Service) ConsumeFieldBattleBuff(ctx command.Context, identity string) error {
	if identity == "" {
		return ErrInvalidRequest
	}
	rows, err := s.state.FieldBuffs(ctx)
	if err != nil {
		return err
	}
	var active []byte
	for _, row := range rows {
		id, _, e := wire.Varint(row, 1)
		if e != nil {
			return e
		}
		buff, found := s.fieldBuffs[id]
		if !found {
			return fmt.Errorf("world: saved battle field buff absent from design")
		}
		if buff.Type == 0 {
			active = row
			break
		}
	}
	if active == nil {
		return nil
	}
	claimed, err := s.state.ClaimFieldBuffBattle(ctx, identity)
	if err != nil || !claimed {
		return err
	}
	id, _, err := wire.Varint(active, 1)
	if err != nil {
		return err
	}
	count, _, err := wire.Varint(active, 2)
	if err != nil {
		return err
	}
	if count <= 1 {
		return s.state.RemoveFieldBuff(ctx, id)
	}
	updated := wire.AppendVarint(nil, 1, id)
	updated = wire.AppendVarint(updated, 2, count-1)
	return s.state.SaveFieldBuff(ctx, id, updated)
}

func (s *Service) handleFieldObjectInfo(ctx command.Context, request []byte) (int, []byte, bool, error) {
	pack, err := requestPack(request)
	if err != nil {
		return 0, nil, true, err
	}
	if !s.packUnlocked(ctx, pack) {
		return 0, nil, true, fmt.Errorf("%w: unavailable field pack", ErrInvalidRequest)
	}
	ids, err := s.openedFieldObjects(ctx, pack)
	if err != nil {
		return 0, nil, true, err
	}
	var response []byte
	for _, id := range ids {
		response = wire.AppendBytes(response, 1, wire.AppendVarint(nil, 1, uint64(id)))
	}
	actions, err := s.fieldActionInfo(ctx, pack)
	if err != nil {
		return 0, nil, true, err
	}
	response = append(response, actions...)
	return 28, response, true, nil
}

func (s *Service) handleFieldObjectReward(ctx command.Context, request []byte) (int, []byte, bool, error) {
	pack, err := requestPack(request)
	if err != nil {
		return 0, nil, true, err
	}
	group, _, err := wire.Varint(request, 3)
	if err != nil || group == 0 || group > uint64(^uint32(0)>>1) {
		return 0, nil, true, ErrInvalidRequest
	}
	id, _, err := wire.Varint(request, 4)
	if err != nil || id == 0 || id > uint64(^uint32(0)>>1) {
		return 0, nil, true, ErrInvalidRequest
	}
	response, err := s.openFieldObjectResponse(ctx, pack, int(group), int(id))
	if err != nil {
		return 0, nil, true, fmt.Errorf("field object reward pack=%d group=%d object=%d: %w", pack, group, id, err)
	}
	return 29, response, true, nil
}

func (s *Service) openFieldObject(ctx command.Context, pack, group, id int) ([]byte, error) {
	design, err := s.fieldObjectDesign(ctx, pack)
	if err != nil {
		return nil, err
	}
	obj, exists := design.Objects[id]
	if !exists || obj.GroupID != group || !s.packUnlocked(ctx, pack) || !s.fieldObjectCurrentPack(pack) {
		return nil, fmt.Errorf("%w: unavailable field object", ErrInvalidRequest)
	}
	if err := s.validateFieldObjectMap(ctx, pack, obj.MapID); err != nil {
		return nil, err
	}
	period, err := s.fieldObjectPeriodFor(pack, obj)
	if err != nil {
		return nil, err
	}
	opened, err := s.state.FieldRewardOpened(ctx, pack, id, period)
	if err != nil {
		return nil, err
	}
	if opened {
		return []byte{}, nil
	}
	// Buff and dynamic-monster objects have no loot group. Their state change
	// belongs to the same dispatcher transaction as this consumed-object marker.
	if len(obj.Rewards) == 0 {
		if obj.BuffID == 0 && obj.MonsterID == 0 && obj.QuestID == 0 && obj.Type != 5 && obj.Type != 6 {
			return nil, fmt.Errorf("%w: empty field object", ErrInvalidRequest)
		}
		return nil, s.state.MarkFieldRewardOpened(ctx, pack, id, period)
	}
	if s.wallet == nil || s.inventory == nil {
		return nil, fmt.Errorf("world: field reward stores unavailable")
	}
	var rewards []gamedata.Reward
	var itemRewards []gamedata.BattleReward
	var equipmentRewards []assets.Equipment
	// Validate every branch's type and quantity before drawing or writing a
	// receipt. LoadFieldObjects validates every equipment option tree, including
	// branches with zero weight, before installing the catalog.
	for _, r := range obj.Rewards {
		if r.Count == 0 || r.Count > uint64(^uint32(0)>>1) {
			return nil, fmt.Errorf("world: invalid field reward count")
		}
		switch r.Type {
		case 2, 3, 4, 12, 20:
		case 5, 7, 8, 9, 13, 14, 17, 19, 27, 29:
			if r.ID == 0 {
				return nil, fmt.Errorf("world: invalid field item")
			}
		case 10:
			if r.ID == 0 || r.Count > 100 || s.equipment == nil || design.Equipment == nil {
				return nil, fmt.Errorf("world: invalid field equipment")
			}
		default:
			return nil, fmt.Errorf("%w: unsupported field reward type %d", ErrInvalidRequest, r.Type)
		}
	}
	selected, err := obj.Draw()
	if err != nil {
		return nil, err
	}
	if design.RewardGraph != nil {
		selected, err = design.RewardGraph.ResolveGranted(selected)
		if err != nil {
			return nil, err
		}
	}
	for _, r := range selected {
		if r.Count == 0 {
			return nil, fmt.Errorf("world: empty field reward")
		}
		switch r.Type {
		case 2, 3, 4, 12, 20:
			rewards = append(rewards, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count}) //nolint:staticcheck // S1016
		case 5, 7, 8, 9, 13, 14, 17, 19, 27, 29:
			if r.ID == 0 {
				return nil, fmt.Errorf("world: invalid field item")
			}
			itemRewards = append(itemRewards, r)
		case 10:
			if s.equipment == nil || design.Equipment == nil || r.Count > 100 || len(equipmentRewards)+int(r.Count) > 100 {
				return nil, fmt.Errorf("world: field equipment reward unavailable")
			}
			for n := uint64(0); n < r.Count; n++ {
				main, sub, private, e := design.Equipment.RollOptions(r.ID)
				if e != nil {
					return nil, e
				}
				entry := assets.Equipment{ID: r.ID, Rank: []uint64{0, 0, 0}}
				for _, option := range main {
					entry.MainOption = append(entry.MainOption, assets.EquipmentOption{GroupID: option.GroupID, ID: option.ID})
				}
				for _, option := range sub {
					entry.SubOption = append(entry.SubOption, assets.EquipmentOption{GroupID: option.GroupID, ID: option.ID})
				}
				if private != nil {
					entry.PrivateOption = &assets.EquipmentOption{GroupID: private.GroupID, ID: private.ID}
				}
				equipmentRewards = append(equipmentRewards, entry)
			}
		default:
			return nil, fmt.Errorf("%w: unsupported field reward type %d", ErrInvalidRequest, r.Type)
		}
	}
	identity := fmt.Sprintf("field-reward:%d:%d:%s", pack, id, period)
	if _, err = s.wallet.GrantQuestOnce(ctx, identity, rewards); err != nil {
		return nil, err
	}
	items, err := s.inventory.GrantOnce(ctx, identity, itemRewards)
	if err != nil {
		return nil, err
	}
	for i, entry := range equipmentRewards {
		equipmentRewards[i], err = s.equipment.GrantGeneratedOnce(ctx, fmt.Sprintf("%s:equipment:%d", identity, i), entry)
		if err != nil {
			return nil, err
		}
	}
	if err = s.state.MarkFieldRewardOpened(ctx, pack, id, period); err != nil {
		return nil, err
	}
	var bundle []byte
	for _, r := range rewards {
		bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(assets.Item{ID: r.ID, Type: r.Type, Count: r.Count}))
	}
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(item))
	}
	for _, entry := range equipmentRewards {
		bundle = wire.AppendBytes(bundle, 4, assets.EquipmentWire(entry))
	}
	return bundle, nil
}

func monsterBundleFields(bundle []byte, itemField, equipField int) ([]byte, error) {
	var b []byte
	err := wire.Walk(bundle, func(f wire.Field) error {
		if f.Type == 2 && f.Number == 1 {
			b = wire.AppendBytes(b, itemField, f.Value)
		} else if f.Type == 2 && f.Number == 4 {
			b = wire.AppendBytes(b, equipField, f.Value)
		}
		return nil
	})
	return b, err
}

func (s *Service) handleFieldMonsterEvent(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	code := 83
	if path == "/FieldMonsterDamage" {
		code = 85
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > 0x7fffffff {
		return code, nil, true, fmt.Errorf("%w: %s invalid sequence", ErrInvalidRequest, path)
	}
	id, err := requestPack(request)
	if err != nil {
		return code, nil, true, fmt.Errorf("%w: %s invalid monster id", err, path)
	}
	dash, _, err := wire.Varint(request, 3)
	if err != nil || dash > 1 {
		return code, nil, true, fmt.Errorf("%w: %s monster %d invalid dash flag", ErrInvalidRequest, path, id)
	}
	if ctx.SessionID == "" {
		return code, nil, true, fmt.Errorf("world: %s monster %d missing session", path, id)
	}
	v, err := s.loadMonsterState(ctx)
	if err != nil {
		return code, nil, true, err
	}
	key := fmt.Sprintf("%s:%s:%d", path, ctx.SessionID, seq)
	if prior, ok := v.Requests[key]; ok {
		if !bytes.Equal(prior.Request, request) {
			return code, nil, true, fmt.Errorf("%w: %s monster %d changed retry", ErrInvalidRequest, path, id)
		}
		return code, prior.Response, true, nil
	}
	pack, err := s.CurrentPackID(ctx)
	if err != nil {
		return code, nil, true, fmt.Errorf("world: %s monster %d resolve pack: %w", path, id, err)
	}
	fail := func(reason string) (int, []byte, bool, error) {
		return code, nil, true, fmt.Errorf("%w: %s pack %d monster %d %s", ErrInvalidRequest, path, pack, id, reason)
	}
	if !s.packUnlocked(ctx, pack) {
		return fail("pack unavailable")
	}
	m, known, err := s.findFieldMonster(pack, id)
	if err != nil {
		return code, nil, true, fmt.Errorf("world: %s pack %d monster %d design: %w", path, pack, id, err)
	}
	if !known {
		return fail("absent from design")
	}
	if m.GroupID == 0 || m.Type > 6 {
		return fail(fmt.Sprintf("type %d has no supported regeneration", m.Type))
	}
	if path == "/FieldMonsterDamage" && (m.Type != 4 || m.FieldBuff == 0 || m.LifeSeconds == 0) {
		return fail(fmt.Sprintf("type %d has no continuous field damage", m.Type))
	}
	if s.monsterMaps == nil {
		return fail(fmt.Sprintf("type %d missing scene design", m.Type))
	}
	maps, err := s.monsterMaps(pack)
	if err != nil {
		return code, nil, true, fmt.Errorf("world: %s pack %d monster %d type %d scene design: %w", path, pack, id, m.Type, err)
	}
	if len(maps[id]) == 0 {
		return fail(fmt.Sprintf("type %d has no scene placement", m.Type))
	}
	eligible := s.monsterEligible(pack, m)
	state := fieldMonsterState{}
	if eligible {
		available, err := s.rewardMonsterAvailable(ctx, pack, id)
		if err != nil {
			return code, nil, true, fmt.Errorf("world: %s pack %d monster %d type %d reward availability: %w", path, pack, id, m.Type, err)
		}
		eligible = available
	}
	if eligible {
		state, err = s.monsterState(&v, pack, m)
		if err != nil {
			return code, nil, true, err
		}
	}
	// DisableMonster sends this same event for refreshes without a wire flag.
	// Ordinary battle monsters only synchronize; their victories settle elsewhere.
	collision := m.Type == 2 || m.Type == 3
	active := eligible && !state.Defeated && state.Respawn <= s.monsterTime().UnixMilli()
	var response []byte
	if active && (path == "/FieldMonsterDamage" || collision) {
		if err := s.authorizeMonsterMap(ctx, pack, id); err != nil {
			return code, nil, true, fmt.Errorf("%s pack %d monster %d type %d collision authorization: %w", path, pack, id, m.Type, err)
		}
		instance := fmt.Sprintf("fieldmonster:%s:%d", monsterKey(pack, id), state.Generation)
		if path == "/FieldMonsterEvent" && m.Type == 2 && !v.Claims[instance] {
			bundle, err := s.grantFieldMonster(ctx, pack, m, instance)
			if err != nil {
				return code, nil, true, err
			}
			response, err = monsterBundleFields(bundle, 3, 4)
			if err != nil {
				return code, nil, true, err
			}
			v.Claims[instance] = true
		}
		damage := path == "/FieldMonsterDamage" || (m.Type == 3 && (dash != 1 || m.CrashType != 1))
		if damage && m.FieldBuff > 0 {
			if path == "/FieldMonsterDamage" {
				damage, err = s.takeFieldMonsterDamageTick(ctx, instance)
				if err != nil {
					return code, nil, true, err
				}
			}
			if damage {
				if s.monsterDamage == nil {
					return code, nil, true, fmt.Errorf("world: %s pack %d monster %d type %d damage runtime unavailable", path, pack, id, m.Type)
				}
				rows, err := s.monsterDamage(ctx, pack, m.FieldBuff, key)
				if err != nil {
					return code, nil, true, err
				}
				for _, row := range rows {
					response = wire.AppendBytes(response, 1, row)
				}
			}
		}
		if path == "/FieldMonsterEvent" {
			state.Defeated = true
			state.Respawn = s.nextMonsterSpawn(m)
			v.Monsters[monsterKey(pack, id)] = state
		}
	}
	// RecvFieldMonsterEvent refreshes every returned monster. Echoing an inactive
	// row calls DisableMonster again and resends this event without a refresh flag.
	if path == "/FieldMonsterEvent" && active && collision {
		response = wire.AppendBytes(response, 2, monsterWire(m, state, eligible))
	}
	v.Requests[key] = fieldMonsterReply{Request: append([]byte(nil), request...), Response: response}
	if err := s.saveMonsterState(ctx, v); err != nil {
		return code, nil, true, err
	}
	return code, response, true, nil
}

// Current clients receive collision rewards in FieldMonsterEventResponse;
// FieldMonsterReward has no sender in this version and no pending reward queue.
func (s *Service) handleFieldMonsterReward(request []byte) (int, []byte, bool, error) {
	seq, present, e := wire.Varint(request, 1)
	if e != nil || !present || seq == 0 {
		return 84, nil, true, ErrInvalidRequest
	}
	return 84, []byte{}, true, nil
}

func (s *Service) ApplyTalentMonsterSummon(ctx command.Context, identity string, c roster.Character, r gamedata.TalentUseRule, ids []uint64) ([]byte, error) {
	if identity == "" || len(ids) == 0 || r.Class != 20 {
		return nil, ErrInvalidRequest
	}
	pack, e := s.CurrentPackID(ctx)
	if e != nil {
		return nil, e
	}
	v, e := s.loadMonsterState(ctx)
	if e != nil {
		return nil, e
	}
	seen := map[uint64]bool{}
	var b []byte
	for _, id := range ids {
		if seen[id] {
			return nil, ErrInvalidRequest
		}
		seen[id] = true
		m, found, e := s.findFieldMonster(pack, int(id))
		if e != nil || !found || m.Type != 2 || !s.monsterEligible(pack, m) {
			return nil, ErrInvalidRequest
		}
		if e = s.authorizeMonsterMap(ctx, pack, int(id)); e != nil {
			return nil, e
		}
		state, e := s.monsterState(&v, pack, m)
		if e != nil {
			return nil, e
		}
		if state.Defeated || state.Respawn > s.monsterTime().UnixMilli() {
			return nil, fmt.Errorf("world: summon target is not spawned")
		}
		instance := fmt.Sprintf("fieldmonster:%s:%d", monsterKey(pack, m.ID), state.Generation)
		if !v.Claims[instance] {
			bundle, e := s.grantFieldMonster(ctx, pack, m, instance)
			if e != nil {
				return nil, e
			}
			extra, e := monsterBundleFields(bundle, 3, 4)
			if e != nil {
				return nil, e
			}
			b = append(b, extra...)
			v.Claims[instance] = true
		}
		state.Defeated = true
		state.Respawn = s.nextMonsterSpawn(m)
		v.Monsters[monsterKey(pack, m.ID)] = state
		b = wire.AppendBytes(b, 9, monsterWire(m, state, true))
	}
	if e = s.saveMonsterState(ctx, v); e != nil {
		return nil, e
	}
	return b, nil
}

func (s *Service) fieldBuffInfo(ctx command.Context) ([]byte, error) {
	rows, err := s.state.FieldBuffs(ctx)
	if err != nil {
		return nil, err
	}
	now := s.monsterTime().UnixMilli()
	var out []byte
	for _, row := range rows {
		end, _, err := wire.Varint(row, 3)
		if err != nil {
			return nil, err
		}
		if end != 0 && (end > 0x7fffffffffffffff || int64(end) <= now) {
			continue
		}
		out = wire.AppendBytes(out, 8, row)
	}
	return out, nil
}

func (s *Service) handleFieldObjectPosition(ctx command.Context, request []byte) (int, []byte, bool, error) {
	pack, err := requestPack(request)
	if err != nil {
		return 95, nil, true, err
	}
	group, _, err := wire.Varint(request, 3)
	if err != nil || group == 0 || group > 0x7fffffff {
		return 95, nil, true, ErrInvalidRequest
	}
	id, _, err := wire.Varint(request, 4)
	if err != nil || id == 0 || id > 0x7fffffff {
		return 95, nil, true, ErrInvalidRequest
	}
	position, found, err := wire.Bytes(request, 5)
	if err != nil || !found {
		return 95, nil, true, ErrInvalidRequest
	}
	mapID, _, err := wire.Varint(position, 1)
	if err != nil || mapID == 0 || mapID > 0x7fffffff {
		return 95, nil, true, ErrInvalidRequest
	}
	if err = wire.Walk(position, func(f wire.Field) error {
		if f.Number >= 2 && f.Number <= 4 {
			if f.Type != 5 {
				return ErrInvalidRequest
			}
			value := math.Float32frombits(binary.LittleEndian.Uint32(f.Value))
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return ErrInvalidRequest
			}
		}
		return nil
	}); err != nil {
		return 95, nil, true, err
	}
	design, err := s.fieldObjectDesign(ctx, pack)
	if err != nil {
		return 95, nil, true, err
	}
	obj, exists := design.Actions[int(id)]
	if !exists || obj.GroupID != int(group) || obj.Type != 1 || !s.packUnlocked(ctx, pack) || !s.fieldObjectCurrentPack(pack) {
		return 95, nil, true, fmt.Errorf("%w: unavailable field action object", ErrInvalidRequest)
	}
	currentMap, err := s.currentFieldMap(ctx, pack)
	if err != nil || currentMap != int(mapID) {
		return 95, nil, true, fmt.Errorf("%w: field action outside current map", ErrInvalidRequest)
	}
	cleared := obj.QuestID > 0 && s.state.QuestCleared(obj.QuestID, pack, s.questDifficultyFor(pack, obj.QuestID))
	if obj.QuestID > 0 && (obj.QuestEnableType == 1 && !cleared || obj.QuestEnableType == 2 && cleared) {
		return 95, nil, true, fmt.Errorf("%w: field action unavailable for quest", ErrInvalidRequest)
	}
	if err = s.state.SaveFieldActionPosition(ctx, pack, int(id), progress.FieldActionPosition{Position: position, QuestCleared: cleared}); err != nil {
		return 95, nil, true, err
	}
	return 95, []byte{}, true, nil
}

func (s *Service) fieldActionInfo(ctx command.Context, pack int) ([]byte, error) {
	positions, err := s.state.FieldActionPositions(ctx, pack)
	if err != nil || len(positions) == 0 {
		return nil, err
	}
	design, err := s.fieldObjectDesign(ctx, pack)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(positions))
	for id := range positions {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var out []byte
	for _, id := range ids {
		obj, found := design.Actions[id]
		if !found {
			return nil, fmt.Errorf("world: saved field action absent from design")
		}
		stored := positions[id]
		// Client resets authored action positions when the related quest ends.
		// Omit that stale position on reconnect so the prefab uses its origin.
		if obj.QuestID > 0 && stored.QuestCleared != s.state.QuestCleared(obj.QuestID, pack, s.questDifficultyFor(pack, obj.QuestID)) {
			continue
		}
		info := wire.AppendBytes(wire.AppendVarint(nil, 1, uint64(id)), 2, stored.Position)
		out = wire.AppendBytes(out, 2, info)
	}
	return out, nil
}
