package eventplay

import (
	"bd2server/internal/common/util"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/progression/conditions"
	"bd2server/internal/server/protocol/wire"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
)

func (s *Service) survivalRule(game uint64) ([]byte, error) {
	row, err := s.design.Row("PackEventMiniGameTable", 8, game)
	if err != nil {
		return nil, err
	}
	return s.design.Row("FieldMiniGameSurvivalTable", 9, num(row, 4))
}

func (s *Service) survivalChar(game, id uint64) ([]byte, error) {
	rule, err := s.survivalRule(game)
	if err != nil {
		return nil, err
	}
	for _, row := range s.design.Rows("FieldMiniGameCharTable", 11, num(rule, 7)) {
		if num(row, 13) == id {
			return row, nil
		}
	}
	return nil, fmt.Errorf("eventplay: survival character outside configured group")
}

func (s *Service) survivalMap(group, id uint64) ([]byte, error) {
	for _, row := range s.design.Rows("FieldMiniGameMapTable", 2, group) {
		if num(row, 3) == id {
			return row, nil
		}
	}
	return nil, fmt.Errorf("eventplay: survival map missing")
}

func survivalCleared(st *snapshot, uid, group, id uint64) bool {
	for _, r := range st.Records {
		if r.UID == uid && r.Family == "Survival" && r.Clears[fmt.Sprintf("map:%d:%d", group, id)] {
			return true
		}
	}
	return false
}

func (s *Service) startSurvival(req []byte, a *Run, st *snapshot) error {
	a.Char, a.MapGroup, a.Mode = num(req, 3), num(req, 4), num(req, 4)
	rule, err := s.survivalRule(a.Game)
	if err != nil {
		return err
	}
	char, err := s.survivalChar(a.Game, a.Char)
	if err != nil {
		return err
	}
	found := false
	for _, row := range s.design.Rows("FieldMiniGameMapGroupTable", 1, num(rule, 15)) {
		if num(row, 2) == a.MapGroup {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("eventplay: survival map group outside season")
	}
	mapRow, err := s.survivalMap(a.MapGroup, a.Stage)
	if err != nil {
		return err
	}
	if num(mapRow, 11) == 0 && (a.MapGroup > 1 && !survivalCleared(st, a.UID, a.MapGroup-1, 1) || a.Stage > 1 && !survivalCleared(st, a.UID, a.MapGroup, a.Stage-1)) {
		return fmt.Errorf("eventplay: survival map difficulty locked")
	}
	a.HP = num(char, 20)
	a.Rerolls = num(char, 19)
	a.MaxHP = a.HP
	a.Level = 1
	a.CoinRate, a.ExpRate = doubleField(char, 10, 1), doubleField(char, 8, 1)
	a.Regen = 1
	for _, group := range s.design.Tables["FieldMiniGameUpgradeGroupTable"] {
		id := num(group, 1)
		level := st.Upgrades[fmt.Sprintf("%d:%d", a.UID, id)]
		if level == 0 {
			level = 1
		}
		for _, row := range s.design.Rows("FieldMiniGameUpgradeTable", 1, id) {
			if num(row, 2) != level {
				continue
			}
			value := doubleField(row, 4, 0)
			switch num(group, 2) {
			case 1:
				a.HP += uint64(value)
			case 2:
				a.Regen += value
			case 8:
				a.ExpRate *= value
			case 9:
				a.CoinRate *= value
			case 12:
				a.Rerolls += uint64(value)
			}
		}
	}
	a.MaxHP = a.HP
	a.LastProgress = a.Started
	a.Killed = map[uint64]uint64{}
	a.Picked = map[uint64]uint64{}
	a.Boxes = map[uint64]uint64{}
	a.SpecialItems = map[uint64]uint64{}
	a.Skills = []uint64{num(char, 3)}
	a.SkillLevels = map[uint64]uint64{num(char, 3): 1}
	random := util.NewWell512(seqSeed(a.Started))
	a.RandomState, a.RandomIndex = random.State, random.Index
	return nil
}

func (s *Service) survivalPassive(a Run, stat uint64) float64 {
	value := float64(1)
	for group, level := range a.SkillLevels {
		for _, row := range s.design.Rows("FieldMiniGameSkillTable", 7, group) {
			if num(row, 8) == level && num(row, 18) == stat {
				value = doubleField(row, 17, 1)
			}
		}
	}
	return value
}

func (s *Service) survivalUnlockInfo(st *snapshot, uid uint64) []byte {
	c, err := s.calendar(uid, "Survival")
	if err != nil {
		return nil
	}
	rule, err := s.survivalRule(c.ID)
	if err != nil {
		return nil
	}
	var out []byte
	for _, row := range s.design.Rows("FieldMiniGameCharTable", 11, num(rule, 7)) {
		out = wire.AppendVarint(out, 2, num(row, 13))
	}
	for _, row := range s.design.Rows("FieldMiniGameMapGroupTable", 1, num(rule, 15)) {
		out = wire.AppendVarint(out, 3, num(row, 2))
	}
	ledger := st.Records[recKey(uid, 0, 0, "Survival")]
	for _, row := range s.design.Tables["FieldMiniGameCollectionTable"] {
		id := num(row, 3)
		if ledger.Clears[fmt.Sprintf("collection:%d", id)] {
			out = wire.AppendBytes(out, 9, wire.AppendVarint(nil, 1, id))
		}
	}
	return out
}

func (s *Service) collectSurvival(st *snapshot, a Run, typ, value uint64) {
	ledger := playRecord(st, a.UID, a.Game, 0, 0, a.Family)
	for _, row := range s.design.Tables["FieldMiniGameCollectionTable"] {
		if num(row, 7) == typ && num(row, 1) == value {
			ledger.Clears[fmt.Sprintf("collection:%d", num(row, 3))] = true
		}
	}
	st.Records[recKey(a.UID, 0, 0, a.Family)] = ledger
}

func (s *Service) survivalDropBox(a *Run, id uint64) error {
	row, err := s.design.Row("FieldMiniGameSurvivalBoxTable", 2, id)
	if err != nil {
		return err
	}
	items, err := list(row, 3)
	if err != nil {
		return err
	}
	if num(row, 1) > 0 {
		ratios, e := list(row, 4)
		if e != nil {
			return e
		}
		if len(ratios) != len(items) {
			return fmt.Errorf("eventplay: malformed survival drop ratios")
		}
		var total uint64
		for _, weight := range ratios {
			total += weight
		}
		if total == 0 {
			return fmt.Errorf("eventplay: empty survival drop ratios")
		}
		random := util.Well512{State: a.RandomState, Index: a.RandomIndex}
		draw := uint64(float32(random.Next() % total))
		a.RandomState, a.RandomIndex = random.State, random.Index
		for i, weight := range ratios {
			if draw < weight {
				items = []uint64{items[i]}
				break
			}
			draw -= weight
		}
	}
	for _, item := range items {
		if item != math.MaxUint64 {
			a.Drops = append(a.Drops, survivalDrop{item, uint64(s.now().UnixMilli())})
		}
	}
	return nil
}

func (s *Service) survivalProgress(req []byte, a *Run) (uint64, uint64, error) {
	mapRow, err := s.survivalMap(a.MapGroup, a.Stage)
	if err != nil {
		return 0, 0, err
	}
	monsters := s.design.Rows("FieldMiniGameMonsterTable", 2, num(mapRow, 4))
	elapsed := uint64(0)
	now := uint64(s.now().UnixMilli())
	if now > a.Started {
		elapsed = (now - a.Started) / 1000
	}
	err = wire.Walk(req, func(f wire.Field) error {
		if f.Number != 6 {
			return nil
		}
		if f.Type != 2 {
			return fmt.Errorf("%w: field %d has wire type %d, want a message", wire.ErrMalformed, f.Number, f.Type)
		}
		id, count := num(f.Value, 1), num(f.Value, 2)
		if count == 0 {
			return fmt.Errorf("eventplay: empty kill count")
		}
		var row []byte
		for _, m := range monsters {
			if num(m, 5) == id {
				row = m
			}
		}
		if row == nil {
			return fmt.Errorf("eventplay: monster outside survival map")
		}
		start, perSecond, duration, repeats, gap := num(row, 16), num(row, 12), num(row, 14), num(row, 11), num(row, 15)
		if elapsed < start {
			return fmt.Errorf("eventplay: monster not spawned yet")
		}
		maxSpawn := uint64(0)
		if perSecond == 0 {
			maxSpawn = 1
		} else {
			if duration == 0 {
				duration = 1
			}
			if repeats == 0 {
				repeats = 1
			}
			for wave := uint64(0); wave < repeats; wave++ {
				begins := start + wave*(duration+gap)
				if elapsed < begins {
					break
				}
				seconds := min(duration, elapsed-begins+1)
				maxSpawn += seconds * perSecond
			}
		}
		if a.Killed[id] > maxSpawn || count > maxSpawn-a.Killed[id] {
			return fmt.Errorf("eventplay: kill count exceeds designed spawns")
		}
		a.Killed[id] += count
		boxes, e := list(row, 6)
		if e != nil {
			return e
		}
		for _, box := range boxes {
			a.Boxes[box] += count
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	boxes, err := list(req, 7)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range boxes {
		if a.Boxes[id] == 0 {
			return 0, 0, fmt.Errorf("eventplay: drop box exceeds committed monster deaths")
		}
		a.Boxes[id]--
		if err := s.survivalDropBox(a, id); err != nil {
			return 0, 0, err
		}
	}
	rule, err := s.survivalRule(a.Game)
	if err != nil {
		return 0, 0, err
	}
	limit := num(rule, 10)
	if limit > 0 && uint64(len(a.Drops)) > limit {
		a.Drops = a.Drops[len(a.Drops)-int(limit):]
	}
	available := a.Drops[:0]
	for _, drop := range a.Drops {
		row, e := s.design.Row("FieldMiniGameSurvivalItemTable", 1, drop.ID)
		if e != nil {
			return 0, 0, e
		}
		if ttl := num(row, 6); ttl == 0 || now <= drop.Created+ttl*1000 {
			available = append(available, drop)
		}
	}
	a.Drops = available
	exp, coin, healing := uint64(0), uint64(0), uint64(0)
	err = wire.Walk(req, func(f wire.Field) error {
		if f.Number != 5 {
			return nil
		}
		if f.Type != 2 {
			return fmt.Errorf("%w: field %d has wire type %d, want a message", wire.ErrMalformed, f.Number, f.Type)
		}
		id, count := num(f.Value, 1), num(f.Value, 2)
		if count == 0 || count > math.MaxInt32 {
			return fmt.Errorf("eventplay: invalid pickup count")
		}
		remaining := count
		kept := a.Drops[:0]
		for _, drop := range a.Drops {
			if drop.ID == id && remaining > 0 {
				remaining--
			} else {
				kept = append(kept, drop)
			}
		}
		if remaining > 0 {
			return fmt.Errorf("eventplay: item count exceeds sampled drops")
		}
		a.Drops = kept
		row, e := s.design.Row("FieldMiniGameSurvivalItemTable", 1, id)
		if e != nil {
			return e
		}
		value := num(row, 4)
		if value > math.MaxInt32/count {
			return fmt.Errorf("eventplay: pickup overflow")
		}
		a.Picked[id] += count
		switch num(row, 3) {
		case 0:
			healing += value * count
		case 3:
			exp += uint64(math.Floor(float64(value)*math.Round(a.ExpRate*s.survivalPassive(*a, 7)*1000)/1000)) * count
		case 4:
			coin += uint64(math.Floor(float64(value)*math.Round(a.CoinRate*s.survivalPassive(*a, 8)*1000)/1000)) * count
		case 5:
			a.SpecialItems[id] += count
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	if exp != num(req, 3) || coin != num(req, 4) {
		return 0, 0, fmt.Errorf("eventplay: survival exp/coin differs from picked items")
	}
	regen := uint64(0)
	if now > a.LastProgress {
		regen = uint64(math.Floor(a.Regen+s.survivalPassive(*a, 10)-1)) * ((now - a.LastProgress) / 5000)
	}
	hp := num(req, 2)
	if hp > uint64(float64(a.MaxHP)*s.survivalPassive(*a, 1)) || hp > a.HP+healing+regen {
		return 0, 0, fmt.Errorf("eventplay: invalid survival HP")
	}
	a.HP = hp
	a.LastProgress = now
	a.Exp += exp
	a.Coin += coin
	a.LevelExp += exp
	if a.Exp > math.MaxInt32 || a.Coin > math.MaxInt32 {
		return 0, 0, fmt.Errorf("eventplay: survival totals overflow")
	}
	char, err := s.survivalChar(a.Game, a.Char)
	if err != nil {
		return 0, 0, err
	}
	for {
		var levelRow []byte
		nextExists := false
		for _, row := range s.design.Rows("FieldMiniGameCharLevelTable", 1, num(char, 2)) {
			if num(row, 2) == a.Level {
				levelRow = row
			}
			if num(row, 2) == a.Level+1 {
				nextExists = true
			}
		}
		if !nextExists || num(levelRow, 3) == 0 || a.LevelExp < num(levelRow, 3) {
			break
		}
		a.LevelExp -= num(levelRow, 3)
		a.Level++
		a.SkillCredits++
	}
	return exp, coin, nil
}

func (s *Service) survivalSkillCandidates(a Run) ([]uint64, error) {
	rule, err := s.survivalRule(a.Game)
	if err != nil {
		return nil, err
	}
	char, err := s.survivalChar(a.Game, a.Char)
	if err != nil {
		return nil, err
	}
	capacity := num(rule, 21)
	used := map[uint64]uint64{}
	for id := range a.SkillLevels {
		row, e := s.design.Row("FieldMiniGameSkillGroupTable", 1, id)
		if e != nil {
			return nil, e
		}
		used[num(row, 5)]++
	}
	var candidates []uint64
	for _, row := range s.design.Tables["FieldMiniGameSkillGroupTable"] {
		id, typ := num(row, 1), num(row, 5)
		if typ != 1 && typ != 2 {
			continue
		}
		if unique := num(row, 7); unique > 0 && unique != num(char, 13) {
			continue
		}
		if a.SkillLevels[id] >= num(row, 2) || a.SkillLevels[id] == 0 && used[typ] >= capacity {
			continue
		}
		if typ == 1 && a.SkillLevels[id]+1 == num(row, 2) && num(row, 8) > 0 && a.SkillLevels[num(row, 8)] == 0 {
			continue
		}
		candidates = append(candidates, id)
	}
	slices.Sort(candidates)
	return candidates, nil
}

func (s *Service) survivalSelect(a *Run) ([]byte, error) {
	if a.SkillCredits == 0 {
		return nil, fmt.Errorf("eventplay: no earned skill selection")
	}
	if len(a.Offers) > 0 {
		if a.Rerolls == 0 {
			return nil, fmt.Errorf("eventplay: no remaining rerolls")
		}
		a.Rerolls--
	}
	candidates, err := s.survivalSkillCandidates(*a)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		for _, row := range s.design.Tables["FieldMiniGameSkillGroupTable"] {
			if num(row, 5) == 0 {
				candidates = append(candidates, num(row, 1))
			}
		}
	}
	a.OfferNonce++
	for i := len(candidates) - 1; i > 0; i-- {
		hash := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d:%d", a.UID, a.Started, a.OfferNonce, i)))
		j := int(binary.LittleEndian.Uint64(hash[:8]) % uint64(i+1))
		candidates[i], candidates[j] = candidates[j], candidates[i]
	}
	if len(candidates) > 3 {
		candidates = candidates[:3]
	}
	a.Offers = candidates
	out := wire.AppendVarint(nil, 1, a.Rerolls)
	for _, id := range a.Offers {
		out = wire.AppendVarint(out, 2, id)
	}
	return out, nil
}

func (s *Service) survivalSkillUp(ctx command.Context, req []byte, a *Run, st *snapshot) ([]byte, error) {
	id, item := num(req, 2), num(req, 3)
	if item > 0 {
		row, err := s.design.Row("FieldMiniGameSurvivalItemTable", 1, item)
		if err != nil {
			return nil, err
		}
		if id != 0 || num(row, 3) != 5 || a.SpecialItems[item] == 0 {
			return nil, fmt.Errorf("eventplay: survival skill item not collected")
		}
		a.SpecialItems[item]--
		var out []byte
		for n := uint64(0); n < num(row, 4); n++ {
			candidates, err := s.survivalSkillCandidates(*a)
			if err != nil {
				return nil, err
			}
			if len(candidates) == 0 {
				for _, fallback := range s.design.Tables["FieldMiniGameSkillGroupTable"] {
					if num(fallback, 5) == 0 {
						b, e := s.survivalFallback(a, fallback)
						if e != nil {
							return nil, e
						}
						out = append(out, b...)
						break
					}
				}
				continue
			}
			a.OfferNonce++
			hash := sha256.Sum256([]byte(fmt.Sprintf("item:%d:%d:%d", a.Started, item, a.OfferNonce)))
			pick := candidates[int(binary.LittleEndian.Uint64(hash[:8])%uint64(len(candidates)))]
			a.SkillLevels[pick]++
			if !has(a.Skills, pick) {
				a.Skills = append(a.Skills, pick)
			}
			v := wire.AppendVarint(nil, 1, pick)
			v = wire.AppendVarint(v, 2, a.SkillLevels[pick])
			out = wire.AppendBytes(out, 1, v)
			s.collectSurvival(st, *a, 1, pick)
			s.collectSurvival(st, *a, 0, pick)
		}
		return out, nil
	}
	if a.SkillCredits == 0 || !has(a.Offers, id) {
		return nil, fmt.Errorf("eventplay: skill not earned or offered")
	}
	group, err := s.design.Row("FieldMiniGameSkillGroupTable", 1, id)
	if err != nil {
		return nil, err
	}
	a.SkillCredits--
	a.Offers = nil
	if num(group, 5) == 0 {
		return s.survivalFallback(a, group)
	}
	a.SkillLevels[id]++
	if !has(a.Skills, id) {
		a.Skills = append(a.Skills, id)
	}
	s.collectSurvival(st, *a, 0, id)
	s.collectSurvival(st, *a, 1, id)
	if a.SkillLevels[id] == num(group, 2) && s.onProgress != nil {
		maxed, err := s.maxedSurvivalSkills(*a)
		if err != nil {
			return nil, err
		}
		if err := s.onProgress(ctx, conditions.Event{Type: conditions.MiniGameSurvivalSkillMaxCount, Sub: maxed, Count: 1}); err != nil {
			return nil, err
		}
		if err := s.onProgress(ctx, conditions.Event{Type: conditions.MiniGameSurvivalSkillMax, Sub: id, Count: 1}); err != nil {
			return nil, err
		}
	}
	v := wire.AppendVarint(nil, 1, id)
	v = wire.AppendVarint(v, 2, a.SkillLevels[id])
	return wire.AppendBytes(nil, 1, v), nil
}

func (s *Service) endSurvivalRewards(ctx command.Context, req []byte, a Run, st *snapshot, key string, r *record) ([]byte, error) {
	if num(req, 5) != a.Coin || num(req, 6) != a.Exp {
		return nil, fmt.Errorf("eventplay: survival end counters differ from committed progress")
	}
	var kills uint64
	for _, count := range a.Killed {
		kills += count
	}
	if num(req, 4) != kills {
		return nil, fmt.Errorf("eventplay: survival end kill count differs from committed progress")
	}
	mapRow, err := s.survivalMap(a.MapGroup, a.Stage)
	if err != nil {
		return nil, err
	}
	var rewards []gamedata.BattleReward
	clear := num(req, 2) == 0 && num(req, 3) >= num(mapRow, 9)
	identity := fmt.Sprintf("map:%d:%d", a.MapGroup, a.Stage)
	if clear && !r.Clears[identity] {
		rr, err := gamedata.EventPlayRewards(mapRow, 8, 7, 6)
		if err != nil {
			return nil, err
		}
		rewards = append(rewards, rr...)
		r.Clears[identity] = true
	}
	if a.Coin > 0 {
		rewards = append(rewards, gamedata.BattleReward{Type: 43, Count: a.Coin})
	}
	var out []byte
	if len(rewards) > 0 {
		bundle, err := s.grant(ctx, "eventplay:"+key, rewards)
		if err != nil {
			return nil, err
		}
		out = wire.AppendBytes(out, 1, bundle)
	}
	out = wire.AppendVarint(out, 2, a.Coin)
	out = wire.AppendVarint(out, 3, a.Exp)
	for id := range a.SkillLevels {
		s.collectSurvival(st, a, 0, id)
		s.collectSurvival(st, a, 1, id)
	}
	for id := range a.Picked {
		row, err := s.design.Row("FieldMiniGameSurvivalItemTable", 1, id)
		if err != nil {
			return nil, err
		}
		s.collectSurvival(st, a, 2, num(row, 3))
	}
	return out, nil
}

func (s *Service) survivalFallback(a *Run, group []byte) ([]byte, error) {
	var row []byte
	for _, r := range s.design.Rows("FieldMiniGameSkillTable", 7, num(group, 1)) {
		if num(r, 8) == 1 {
			row = r
		}
	}
	if row == nil {
		return nil, fmt.Errorf("eventplay: fallback skill missing")
	}
	valueFloat := doubleField(row, 17, 0)
	if valueFloat < 0 || valueFloat > math.MaxInt32 {
		return nil, fmt.Errorf("eventplay: invalid fallback skill value")
	}
	value := uint64(valueFloat)
	if num(group, 4) == 2002 {
		if a.Coin > math.MaxInt32-value {
			return nil, fmt.Errorf("eventplay: survival coin overflow")
		}
		a.Coin += value
		return wire.AppendVarint(nil, 2, value), nil
	}
	maximum := uint64(float64(a.MaxHP) * s.survivalPassive(*a, 1))
	heal := uint64(0)
	if maximum > a.HP {
		heal = min(value, maximum-a.HP)
	}
	a.HP += heal
	return wire.AppendVarint(nil, 3, heal), nil
}

// maxedSurvivalSkills counts the skills of one run at their configured
// FieldMiniGameSkillGroupTable maximum level.
func (s *Service) maxedSurvivalSkills(a Run) (uint64, error) {
	var maxed uint64
	for id, level := range a.SkillLevels {
		group, err := s.design.Row("FieldMiniGameSkillGroupTable", 1, id)
		if err != nil {
			return 0, err
		}
		if level >= num(group, 2) {
			maxed++
		}
	}
	return maxed, nil
}
