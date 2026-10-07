package world

import (
	"bd2server/internal/server/domain/command"
	"encoding/json"
	"fmt"
)

func (s *Service) takeFieldMonsterDamageTick(ctx command.Context, instance string) (bool, error) {
	if s.monsterStore == nil {
		return false, fmt.Errorf("world: monster damage persistence unavailable")
	}
	data, err := s.monsterStore.Load(ctx.State, "field_monster_damage")
	if err != nil {
		return false, err
	}
	ticks := map[string]int64{}
	if data != nil {
		if err := json.Unmarshal(data, &ticks); err != nil || ticks == nil {
			return false, fmt.Errorf("world: invalid monster damage ticks")
		}
		for id, timestamp := range ticks {
			if id == "" || timestamp <= 0 {
				return false, fmt.Errorf("world: invalid monster damage tick")
			}
		}
	}
	now := s.monsterTime().UnixMilli()
	// FieldMonsterController.IProcessHitDotDamage waits one second per hit.
	if last := ticks[instance]; last != 0 && now-last < 1000 {
		return false, nil
	}
	ticks[instance] = now
	data, err = json.Marshal(ticks)
	if err != nil {
		return false, err
	}
	if err := s.monsterStore.Save(ctx.State, "field_monster_damage", data); err != nil {
		return false, err
	}
	return true, nil
}
