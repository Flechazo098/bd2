package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
	"bytes"
	"fmt"
)

func (s *Service) grantFieldMonster(pack int, m gamedata.FieldMonsterDesign, identity string) ([]byte, error) {
	if s.researchEconomy == nil {
		return nil, fmt.Errorf("world: monster economy unavailable")
	}
	var rewards []gamedata.Reward
	if m.Reward.Type != 0 && m.Reward.Count > 0 {
		rewards = append(rewards, m.Reward)
	} else if m.BattleDeck != 0 {
		if s.monsterRewards == nil {
			return nil, fmt.Errorf("world: monster reward loader unavailable")
		}
		rows, e := s.monsterRewards(pack, m.BattleDeck)
		if e != nil {
			return nil, e
		}
		for _, r := range rows {
			rewards = append(rewards, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count})
		}
	}
	return s.researchEconomy.Apply(identity, nil, rewards)
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
func (s *Service) handleFieldMonsterEvent(path string, request []byte) (int, []byte, bool, error) {
	seqCheck, present, seqErr := wire.Varint(request, 1)
	if seqErr != nil || !present || seqCheck == 0 || seqCheck > 0x7fffffff {
		return 0, nil, true, ErrInvalidRequest
	}
	code := 83
	if path == "/FieldMonsterDamage" {
		code = 85
	}
	id, e := requestPack(request)
	if e != nil {
		return code, nil, true, e
	}
	pack, e := s.CurrentPackID()
	if e != nil || !s.packUnlocked(pack) {
		return code, nil, true, ErrInvalidRequest
	}
	m, found, e := s.findFieldMonster(pack, id)
	if e != nil || !found || !s.monsterEligible(pack, m) {
		return code, nil, true, ErrInvalidRequest
	}
	if path == "/FieldMonsterEvent" && m.Type != 2 && m.Type != 3 {
		return code, nil, true, ErrInvalidRequest
	}
	if e = s.authorizeMonsterMap(pack, id); e != nil {
		return code, nil, true, e
	}
	v, e := s.loadMonsterState()
	if e != nil {
		return code, nil, true, e
	}
	seq, _, _ := wire.Varint(request, 1)
	if s.monsterSession == "" {
		return code, nil, true, fmt.Errorf("world: missing monster session")
	}
	key := fmt.Sprintf("%s:%s:%d", path, s.monsterSession, seq)
	if r, ok := v.Requests[key]; ok {
		if !bytes.Equal(r.Request, request) {
			return code, nil, true, ErrInvalidRequest
		}
		return code, r.Response, true, nil
	}
	state, e := s.monsterState(&v, pack, m)
	if e != nil {
		return code, nil, true, e
	}
	var b []byte
	active := !state.Defeated && state.Respawn <= s.monsterTime().UnixMilli()
	instance := fmt.Sprintf("fieldmonster:%s:%d", monsterKey(pack, s.questDifficulty(pack), id), state.Generation)
	if active {
		if m.Type == 2 && path == "/FieldMonsterEvent" && !v.Claims[instance] {
			bundle, e := s.grantFieldMonster(pack, m, instance)
			if e != nil {
				return code, nil, true, e
			}
			b, e = monsterBundleFields(bundle, 3, 4)
			if e != nil {
				return code, nil, true, e
			}
			v.Claims[instance] = true
		}
		dash, _, _ := wire.Varint(request, 3)
		if m.FieldBuff > 0 && (path == "/FieldMonsterDamage" || dash == 0) {
			if s.monsterDamage == nil {
				return code, nil, true, fmt.Errorf("world: monster damage runtime unavailable")
			}
			rows, e := s.monsterDamage(pack, m.FieldBuff, key)
			if e != nil {
				return code, nil, true, e
			}
			for _, r := range rows {
				b = wire.AppendBytes(b, 1, r)
			}
		}
		if path == "/FieldMonsterEvent" {
			state.Defeated = true
			state.Respawn = s.nextMonsterSpawn(m)
			v.Monsters[monsterKey(pack, s.questDifficulty(pack), id)] = state
		}
	}
	if path == "/FieldMonsterEvent" {
		b = wire.AppendBytes(b, 2, monsterWire(m, state, true))
	}
	v.Requests[key] = fieldMonsterReply{Request: append([]byte(nil), request...), Response: b}
	if e = s.saveMonsterState(v); e != nil {
		return code, nil, true, e
	}
	return code, b, true, nil
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
func (s *Service) ApplyTalentMonsterSummon(identity string, c player.Character, r gamedata.TalentUseRule, ids []uint64) ([]byte, error) {
	if identity == "" || len(ids) == 0 || r.Class != 20 {
		return nil, ErrInvalidRequest
	}
	pack, e := s.CurrentPackID()
	if e != nil {
		return nil, e
	}
	v, e := s.loadMonsterState()
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
		if e = s.authorizeMonsterMap(pack, int(id)); e != nil {
			return nil, e
		}
		state, e := s.monsterState(&v, pack, m)
		if e != nil {
			return nil, e
		}
		if state.Defeated || state.Respawn > s.monsterTime().UnixMilli() {
			return nil, fmt.Errorf("world: summon target is not spawned")
		}
		instance := fmt.Sprintf("fieldmonster:%s:%d", monsterKey(pack, s.questDifficulty(pack), m.ID), state.Generation)
		if !v.Claims[instance] {
			bundle, e := s.grantFieldMonster(pack, m, instance)
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
		v.Monsters[monsterKey(pack, s.questDifficulty(pack), m.ID)] = state
		b = wire.AppendBytes(b, 9, monsterWire(m, state, true))
	}
	if e = s.saveMonsterState(v); e != nil {
		return nil, e
	}
	return b, nil
}
