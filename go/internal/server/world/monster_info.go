package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"
)

// AttachFieldMonsterState uses the gameplay snapshot store, which participates
// in the account transaction. No in-memory lifetime survives a rollback.
func (s *Service) AttachFieldMonsterState(store stateio.Store) error {
	if store == nil {
		return fmt.Errorf("world: missing monster state store")
	}
	s.monsterStore = store
	return nil
}

func (s *Service) handleMonsterInfo(request []byte) (int, []byte, bool, error) {
	seq, ok, err := wire.Varint(request, 1)
	if err != nil || !ok || seq == 0 {
		return 0, nil, true, ErrInvalidRequest
	}
	groups := map[int]bool{}
	err = wire.Walk(request, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 0 && f.Type != 2 {
			return ErrInvalidRequest
		}
		data := f.Value
		for len(data) > 0 {
			v, n := binary.Uvarint(data)
			if n <= 0 || v == 0 || v > uint64(^uint32(0)>>1) {
				return ErrInvalidRequest
			}
			groups[int(v)] = true
			if len(groups) > 4096 {
				return ErrInvalidRequest
			}
			data = data[n:]
		}
		return nil
	})
	if err != nil {
		return 0, nil, true, err
	}
	pack, err := s.CurrentPackID()
	if err != nil {
		return 0, nil, true, err
	}
	if !s.packUnlocked(pack) || s.monsterLoader == nil {
		return 0, nil, true, fmt.Errorf("%w: unavailable monster pack", ErrInvalidRequest)
	}
	design, err := s.monsterLoader(pack)
	if err != nil {
		return 0, nil, true, err
	}
	var response []byte
	starts := map[string]int64{}
	if s.monsterStore != nil {
		b, e := s.monsterStore.Load("fieldmonsters")
		if e != nil {
			return 0, nil, true, e
		}
		if b != nil {
			if e = json.Unmarshal(b, &starts); e != nil || starts == nil {
				return 0, nil, true, fmt.Errorf("world: invalid monster lifetime state")
			}
		}
		for _, t := range starts {
			if t <= 0 {
				return 0, nil, true, fmt.Errorf("world: invalid monster start time")
			}
		}
	}
	changed := false
	difficulty := 0
	if selection, ok := s.state.Selection(pack); ok {
		difficulty = selection.Difficulty
	}
	for _, m := range design {
		if !groups[m.GroupID] {
			continue
		}
		active := m.QuestID == 0 || s.state.QuestCleared(m.QuestID, pack, difficulty)
		row := wire.AppendVarint(nil, 1, uint64(m.ID))
		row = wire.AppendVarint(row, 5, uint64(m.GroupID))
		if m.BattleDeck != 0 {
			row = wire.AppendVarint(row, 2, m.BattleDeck)
		}
		if active && m.LifeSeconds > 0 {
			if s.monsterStore == nil {
				return 0, nil, true, fmt.Errorf("world: monster lifetime state unavailable")
			}
			key := fmt.Sprintf("%d/%d/%d", pack, difficulty, m.ID)
			start, ok := starts[key]
			if !ok {
				start = time.Now().UnixMilli()
				starts[key] = start
				changed = true
			}
			end := start + int64(m.LifeSeconds)*1000
			row = wire.AppendVarint(row, 4, uint64(end))
			active = time.Now().UnixMilli() < end
		}
		if active {
			row = wire.AppendVarint(row, 6, 1)
		}
		response = wire.AppendBytes(response, 1, row)
	}
	if changed {
		b, e := json.Marshal(starts)
		if e != nil {
			return 0, nil, true, e
		}
		if e = s.monsterStore.Save("fieldmonsters", b); e != nil {
			return 0, nil, true, e
		}
	}
	return 51, response, true, nil
}

func (s *Service) attachFieldMonsterDesign(root, version string) {
	s.monsterLoader = func(pack int) ([]gamedata.FieldMonsterDesign, error) {
		return gamedata.LoadFieldMonsters(root, version, pack)
	}
}
