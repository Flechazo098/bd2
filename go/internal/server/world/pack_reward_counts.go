package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"encoding/binary"
	"fmt"
)

func (s *Service) AttachResearchRuntime(root, version string, economy interface {
	Apply(string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}) error {
	if economy == nil {
		return fmt.Errorf("world: nil research economy")
	}
	chars, err := gamedata.LoadResearchCharacters(root, version)
	if err != nil {
		return err
	}
	s.researchCharacters = chars
	s.researchEconomy = economy
	s.researchDesigns = map[int]gamedata.FieldResearchDesign{}
	s.researchLoader = func(pack int) (gamedata.FieldResearchDesign, error) {
		return gamedata.LoadFieldResearch(root, version, pack)
	}
	return nil
}
func (s *Service) researchDesign(pack int) (gamedata.FieldResearchDesign, error) {
	if d, ok := s.researchDesigns[pack]; ok {
		return d, nil
	}
	if s.researchLoader == nil {
		return gamedata.FieldResearchDesign{}, fmt.Errorf("world: research design unavailable")
	}
	d, err := s.researchLoader(pack)
	if err != nil {
		return d, err
	}
	s.researchDesigns[pack] = d
	return d, nil
}
func intsRequest(raw []byte, number int) ([]uint64, error) {
	var out []uint64
	err := wire.Walk(raw, func(f wire.Field) error {
		if f.Number != number {
			return nil
		}
		if f.Type == 0 {
			v, n := binary.Uvarint(f.Value)
			if n <= 0 {
				return wire.ErrMalformed
			}
			out = append(out, v)
			return nil
		}
		if f.Type != 2 {
			return ErrInvalidRequest
		}
		for raw := f.Value; len(raw) > 0; {
			v, n := binary.Uvarint(raw)
			if n <= 0 {
				return wire.ErrMalformed
			}
			out = append(out, v)
			raw = raw[n:]
		}
		return nil
	})
	return out, err
}
func (s *Service) handleFieldResearch(request []byte) (int, []byte, bool, error) {
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 {
		return 59, nil, true, ErrInvalidRequest
	}
	pack, err := requestPack(request)
	if err != nil {
		return 59, nil, true, err
	}
	id, _, err := wire.Varint(request, 3)
	if err != nil || id == 0 || !s.packUnlocked(pack) || s.state.ActivePackID() != pack {
		return 59, nil, true, ErrInvalidRequest
	}
	design, err := s.researchDesign(pack)
	if err != nil {
		return 59, nil, true, err
	}
	obj, ok := design.Objects[int(id)]
	if !ok || obj.CollectionID == 0 && obj.Reward.Type == 0 {
		return 59, nil, true, ErrInvalidRequest
	}
	position, saved := s.state.Position()
	mapOK := false
	for _, mapID := range obj.Maps {
		if saved && position.PackID == pack && position.Position.MapID == mapID {
			mapOK = true
		}
	}
	if !mapOK {
		return 59, nil, true, fmt.Errorf("%w: research outside current map", ErrInvalidRequest)
	}
	// Quest interactions use QuestUpdate, while FieldObjectResearch grants the
	// object's collection/reward after its quest interaction is no longer active.
	for _, quest := range obj.InteractionQuests {
		if _, active := s.state.QuestInPack(quest, pack, s.questDifficulty(pack)); active && !s.state.QuestCleared(quest, pack, s.questDifficulty(pack)) {
			return 59, nil, true, fmt.Errorf("%w: research belongs to active quest", ErrInvalidRequest)
		}
	}
	if obj.Type == 1 {
		eligible := false
		// This server validates a learned research talent. Client animation and
		// temporary highlight flags are presentation state rather than authority.
		if s.characters != nil {
			for _, c := range s.characters.All() {
				if c.TalentLevel > 0 && s.researchCharacters[c.ID] {
					eligible = true
					break
				}
			}
		}
		if !eligible {
			return 59, nil, true, fmt.Errorf("%w: research talent unavailable", ErrInvalidRequest)
		}
	}
	prior, err := s.state.ResearchObjects(pack)
	if err != nil {
		return 59, nil, true, err
	}
	for _, v := range prior {
		if v == int(id) {
			items, found, e := s.state.ResearchObjectReply(pack, int(id))
			if e != nil {
				return 59, nil, true, e
			}
			if !found {
				return 59, nil, true, fmt.Errorf("world: researched object reward receipt missing")
			}
			return 59, append(wire.AppendVarint(nil, 1, seq), items...), true, nil
		}
	}
	if s.researchEconomy == nil {
		return 59, nil, true, fmt.Errorf("world: research economy unavailable")
	}
	var rewards []gamedata.Reward
	if obj.Reward.Type != 0 && obj.Reward.Count > 0 {
		rewards = append(rewards, obj.Reward)
	}
	if obj.CollectionID > 0 {
		rewards = append(rewards, gamedata.Reward{Type: 17, ID: uint64(obj.CollectionID), Count: 1})
	}
	bundle, err := s.researchEconomy.Apply(fmt.Sprintf("research:%d:%d", pack, id), nil, rewards)
	if err != nil {
		return 59, nil, true, err
	}
	var items []byte
	err = wire.Walk(bundle, func(f wire.Field) error {
		if f.Number == 1 && f.Type == 2 {
			items = wire.AppendBytes(items, 2, f.Value)
		}
		return nil
	})
	if err != nil {
		return 59, nil, true, err
	}
	if err = s.state.MarkResearchObject(pack, int(id), items); err != nil {
		return 59, nil, true, err
	}
	return 59, append(wire.AppendVarint(nil, 1, seq), items...), true, nil
}
func fieldCountType(obj gamedata.FieldRewardObject) uint64 {
	if obj.Type == 3 {
		return 2
	}
	if obj.Type == 6 {
		return 4
	}
	return 3
}
func (s *Service) handlePackRewardCounts(request []byte) (int, []byte, bool, error) {
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 {
		return 226, nil, true, ErrInvalidRequest
	}
	packs, err := intsRequest(request, 2)
	if err != nil || len(packs) == 0 {
		return 226, nil, true, ErrInvalidRequest
	}
	types, err := intsRequest(request, 3)
	if err != nil {
		return 226, nil, true, err
	}
	if len(types) == 0 {
		types = []uint64{1, 2, 3, 4}
	}
	var out []byte
	seen := map[[2]uint64]bool{}
	for _, p := range packs {
		pack := int(p)
		if pack <= 0 || !s.packUnlocked(pack) {
			return 226, nil, true, ErrInvalidRequest
		}
		for _, t := range types {
			if t < 1 || t > 4 {
				return 226, nil, true, ErrInvalidRequest
			}
			key := [2]uint64{p, t}
			if seen[key] {
				continue
			}
			seen[key] = true
			var count, max uint64
			if t == 1 {
				d, e := s.researchDesign(pack)
				if e != nil {
					return 226, nil, true, e
				}
				ids, e := s.state.ResearchObjects(pack)
				if e != nil {
					return 226, nil, true, e
				}
				for _, o := range d.Objects {
					if o.CollectionID > 0 || o.Reward.Type > 0 {
						max++
					}
				}
				for _, id := range ids {
					if o, ok := d.Objects[id]; ok && (o.CollectionID > 0 || o.Reward.Type > 0) {
						count++
					}
				}
			} else {
				d, e := s.fieldObjectDesign(pack)
				if e != nil {
					return 226, nil, true, e
				}
				for _, o := range d.Objects {
					if fieldCountType(o) != t {
						continue
					}
					max++
					period, e := s.fieldObjectPeriod(o)
					if e != nil {
						continue
					}
					opened, e := s.state.FieldRewardOpened(pack, o.ID, period)
					if e != nil {
						return 226, nil, true, e
					}
					if opened {
						count++
					}
				}
			}
			info := wire.AppendVarint(nil, 1, t)
			info = wire.AppendVarint(info, 2, p)
			info = wire.AppendVarint(info, 3, count)
			info = wire.AppendVarint(info, 4, max)
			out = wire.AppendBytes(out, 1, info)
		}
	}
	return 226, out, true, nil
}
