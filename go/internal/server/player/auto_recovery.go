package player

import (
	"bd2server/internal/server/wire"
	"fmt"
	"math"
	"sort"
)

// AutoRecoveryResult uses Define_AutoReviveDisabledType's protocol values.
type AutoRecoveryResult struct {
	Caster, Experience, Catalyst, Disabled uint64
	Characters                             []Character
}

// AutoRecover selects a living permanent fatigue-recovery caster. The selected
// caster is preferred; another owned caster can take over when it is fatigued.
func (s *TalentUseService) AutoRecover(seq, caster uint64, targets []uint64) (AutoRecoveryResult, error) {
	r := AutoRecoveryResult{Caster: caster, Catalyst: s.wallet.CatalystBalance()}
	if s.context == nil {
		return r, fmt.Errorf("player: automatic recovery context unavailable")
	}
	pack, _, battle, err := s.context()
	if err != nil {
		return r, err
	}
	if battle {
		return r, fmt.Errorf("player: automatic recovery during battle")
	}
	all := s.characters.RawAll()
	sort.SliceStable(all, func(i, j int) bool {
		left, right := all[i].InvenIndex == caster, all[j].InvenIndex == caster
		if left != right {
			return left
		}
		return all[i].InvenIndex < all[j].InvenIndex
	})
	var selected Character
	var cost uint64
	for _, c := range all {
		if IsStoryCharacter(c) || IsCharmCharacter(c) {
			continue
		}
		meta, ok := s.design.Characters[c.ID]
		if !ok || meta.BannedPacks[pack] {
			continue
		}
		rule, ok := s.design.Rules[[2]uint64{meta.Group, c.TalentLevel}]
		if !ok || rule.Class != 10 {
			continue
		}
		hp, e := s.characters.CurrentHealth(c.InvenIndex)
		if e != nil {
			return r, e
		}
		if hp == 0 {
			continue
		}
		selected = c
		if len(targets) > 0 && rule.Catalyst > math.MaxUint64/uint64(len(targets)) {
			return r, fmt.Errorf("player: automatic recovery cost overflow")
		}
		cost = rule.Catalyst * uint64(len(targets))
		break
	}
	if selected.InvenIndex == 0 {
		r.Disabled = 1
		return r, nil
	}
	r.Caster = selected.InvenIndex
	if len(targets) == 0 {
		return r, nil
	}
	if cost > r.Catalyst {
		r.Disabled = 2
		return r, nil
	}
	request := wire.AppendVarint(nil, 1, seq)
	request = wire.AppendVarint(request, 2, r.Caster)
	for _, id := range targets {
		request = wire.AppendVarint(request, 3, id)
	}
	_, body, _, err := s.healing(request, "auto-recovery")
	if err != nil {
		return r, err
	}
	r.Experience, _, err = wire.Varint(body, 2)
	if err != nil {
		return r, err
	}
	r.Catalyst = s.wallet.CatalystBalance()
	for _, id := range targets {
		c, ok := s.characters.Find(id)
		if !ok {
			return r, fmt.Errorf("player: recovered character unavailable")
		}
		hp, e := s.characters.CurrentHealth(id)
		if e != nil {
			return r, e
		}
		c.HP = hp
		r.Characters = append(r.Characters, c)
	}
	return r, nil
}
