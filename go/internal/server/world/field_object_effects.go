package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
	"fmt"
	"math"
)

func (s *Service) openFieldObjectResponse(pack, group, id int) ([]byte, error) {
	design, err := s.fieldObjectDesign(pack)
	if err != nil {
		return nil, err
	}
	obj, ok := design.Objects[id]
	if !ok || obj.GroupID != group || !s.packUnlocked(pack) || !s.fieldObjectCurrentPack(pack) {
		return nil, ErrInvalidRequest
	}
	if err := s.validateFieldObjectMap(pack, obj.MapID); err != nil {
		return nil, err
	}
	period, err := s.fieldObjectPeriodFor(pack, obj)
	if err != nil {
		return nil, err
	}
	opened, err := s.state.FieldRewardOpened(pack, id, period)
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
		row, chars, e := s.applyFieldObjectBuff(pack, buff)
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
			snapshot, e := s.loadMonsterState()
			if e != nil {
				return nil, e
			}
			state, e := s.monsterState(&snapshot, pack, monster)
			if e != nil {
				return nil, e
			}
			if e = s.saveMonsterState(snapshot); e != nil {
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
		if err := s.state.MarkFieldRewardOpened(pack, id, period); err != nil {
			return nil, err
		}
		return append(wire.AppendBytes(nil, 1, nil), effects...), nil
	}
	bundle, err := s.openFieldObject(pack, group, id)
	if err != nil {
		return nil, err
	}
	return append(wire.AppendBytes(nil, 1, bundle), effects...), nil
}

func (s *Service) applyFieldObjectBuff(pack int, buff gamedata.FieldBuffDesign) ([]byte, [][]byte, error) {
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
		prior, err := s.state.FieldBuffs()
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
				if e := s.state.RemoveFieldBuff(id); e != nil {
					return nil, nil, e
				}
			}
		}
		if err := s.state.SaveFieldBuff(buff.ID, row); err != nil {
			return nil, nil, err
		}
		return row, nil, nil
	}
	if buff.Type == 4 || buff.Type == 5 {
		chars, err := s.applyMonsterFieldDamage(pack, buff.ID, "")
		return row, chars, err
	}
	if s.characters == nil || s.decks == nil {
		return nil, nil, fmt.Errorf("world: field healing runtime unavailable")
	}
	indices, err := s.fieldBuffTargets(pack, buff.TargetType)
	if err != nil {
		return nil, nil, err
	}
	var chars [][]byte
	for _, index := range indices {
		c, found := s.characters.Find(index)
		if !found {
			return nil, nil, fmt.Errorf("world: field healing character missing")
		}
		hp, err := s.characters.CurrentHealth(index)
		if err != nil {
			return nil, nil, err
		}
		if hp == 0 {
			continue
		}
		max, err := s.characters.MaxHealth(index)
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
		if err = s.characters.SetCurrentHealth(index, remaining); err != nil {
			return nil, nil, err
		}
		c.HP = remaining
		chars = append(chars, player.CharacterWire(c))
	}
	return row, chars, nil
}

// ConsumeFieldBattleBuff runs inside the successful BattleEnter transaction.
// A persisted battle identity makes retries and reconnects safe.
func (s *Service) ConsumeFieldBattleBuff(identity string) error {
	if identity == "" {
		return ErrInvalidRequest
	}
	rows, err := s.state.FieldBuffs()
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
	claimed, err := s.state.ClaimFieldBuffBattle(identity)
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
		return s.state.RemoveFieldBuff(id)
	}
	updated := wire.AppendVarint(nil, 1, id)
	updated = wire.AppendVarint(updated, 2, count-1)
	return s.state.SaveFieldBuff(id, updated)
}
