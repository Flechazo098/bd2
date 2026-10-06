package player

import (
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
)

type potentialConnectReceipt struct {
	Digest string
	Body   []byte
}

func (s *CostumePotentialService) AttachConnectStore(store stateio.Store) error {
	if store == nil {
		return fmt.Errorf("player: missing potential connect store")
	}
	s.connectStore = store
	return nil
}
func (s *CostumePotentialService) BeginSession(id string) {
	s.connectMu.Lock()
	defer s.connectMu.Unlock()
	s.connectSession = id
}
func (s *CostumePotentialService) connect(request []byte) (int, []byte, bool, error) {
	s.connectMu.Lock()
	defer s.connectMu.Unlock()
	fail := func(e error) (int, []byte, bool, error) { return 267, nil, true, e }
	seq, ok, err := wire.Varint(request, 1)
	if err != nil || !ok || seq == 0 || seq > math.MaxInt32 || s.connectStore == nil || s.connectSession == "" {
		return fail(fmt.Errorf("player: invalid potential connection session/sequence"))
	}
	var receipts map[string]potentialConnectReceipt
	b, err := s.connectStore.Load("potentialconnect")
	if err != nil {
		return fail(err)
	}
	if b != nil {
		if err = json.Unmarshal(b, &receipts); err != nil || receipts == nil {
			return fail(fmt.Errorf("player: invalid potential connection receipts"))
		}
	} else {
		receipts = map[string]potentialConnectReceipt{}
	}
	for _, r := range receipts {
		hash, e := hex.DecodeString(r.Digest)
		if e != nil || len(hash) != sha256.Size {
			return fail(fmt.Errorf("player: malformed potential connection receipt digest"))
		}
		if e = wire.Walk(r.Body, func(wire.Field) error { return nil }); e != nil {
			return fail(e)
		}
	}
	seqFields := 0
	if err = wire.Walk(request, func(f wire.Field) error {
		if f.Number == 1 {
			seqFields++
			if f.Type != 0 {
				return fmt.Errorf("player: invalid potential sequence wire")
			}
		}
		return nil
	}); err != nil {
		return fail(err)
	}
	if seqFields != 1 {
		return fail(fmt.Errorf("player: duplicate potential sequence"))
	}
	key := fmt.Sprintf("%s:%d", s.connectSession, seq)
	digest := fmt.Sprintf("%x", sha256.Sum256(request))
	if prior, ok := receipts[key]; ok {
		if prior.Digest != digest {
			return fail(fmt.Errorf("player: changed potential connection replay"))
		}
		return 267, prior.Body, true, nil
	}
	var characters []Character
	seen := map[uint64]bool{}
	oldHP := map[uint64]uint64{}
	err = wire.Walk(request, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 2 || len(characters) >= 4096 {
			return fmt.Errorf("player: invalid potential connection list")
		}
		fields := map[int]bool{}
		if e := wire.Walk(f.Value, func(field wire.Field) error {
			if field.Number == 1 || field.Number == 2 {
				if field.Type != 0 || fields[field.Number] {
					return fmt.Errorf("player: duplicate or invalid connection scalar")
				}
				fields[field.Number] = true
			}
			return nil
		}); e != nil {
			return e
		}
		index, ok, e := wire.Varint(f.Value, 1)
		if e != nil || !ok || index == 0 || index > math.MaxInt64 || seen[index] {
			return fmt.Errorf("player: duplicate or invalid potential character")
		}
		costume, _, e := wire.Varint(f.Value, 2)
		if e != nil || costume == 0 || costume > math.MaxInt32 {
			return fmt.Errorf("player: potential connection requires a costume design ID")
		}
		c, owned := s.characters.Find(index)
		if !owned || IsStoryCharacter(c) || IsCharmCharacter(c) {
			return fmt.Errorf("player: potential character is not permanent owned")
		}
		unique := s.design.CharacterUnique[c.ID]
		characterType, knownType := s.design.CharacterTypes[c.ID]
		if unique == 0 || !knownType || characterType != 0 || !s.design.CostumeActive[costume] || s.design.CostumeUnique[costume] != unique || len(s.design.Nodes[costume]) == 0 {
			return fmt.Errorf("player: incompatible or unavailable potential costume")
		}
		ownedCostume := false
		for _, v := range s.collection.Costumes() {
			if v.ID == costume && v.UseChar == index {
				ownedCostume = true
				for _, id := range v.PotentialIDs {
					if _, ok := s.design.Nodes[costume][id]; !ok {
						return fmt.Errorf("player: invalid saved potential node")
					}
				}
				break
			}
		}
		if !ownedCostume {
			return fmt.Errorf("player: potential costume not owned by requested character")
		}
		hp, e := s.characters.CurrentHealth(index)
		if e != nil {
			return e
		}
		oldHP[index] = hp
		c.ConnectPotentialCostume = costume
		characters = append(characters, c)
		seen[index] = true
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if len(characters) == 0 {
		return fail(fmt.Errorf("player: empty potential connection list"))
	}
	// All links are validated before writes. The parent account transaction
	// includes both character ownership domains, HP and the response receipt.
	for _, c := range characters {
		if err = s.characters.setPotentialConnection(c.InvenIndex, c.ConnectPotentialCostume); err != nil {
			return fail(err)
		}
	}
	var out []byte
	for _, c := range characters {
		maximum, e := s.characters.MaxHealth(c.InvenIndex)
		if e != nil {
			return fail(e)
		}
		hp := min(oldHP[c.InvenIndex], maximum)
		if e = s.characters.SetCurrentHealth(c.InvenIndex, hp); e != nil {
			return fail(e)
		}
		c.HP = hp
		out = wire.AppendBytes(out, 1, CharacterWire(c))
	}
	receipts[key] = potentialConnectReceipt{Digest: digest, Body: out}
	b, err = json.Marshal(receipts)
	if err != nil {
		return fail(err)
	}
	if err = s.connectStore.Save("potentialconnect", b); err != nil {
		return fail(err)
	}
	return 267, out, true, nil
}
func (s *CharacterStore) setPotentialConnection(index, costume uint64) error {
	s.mu.Lock()
	for i, c := range s.characters {
		if c.InvenIndex == index {
			next := append([]Character(nil), s.characters...)
			next[i].ConnectPotentialCostume = costume
			if err := s.persist(next); err != nil {
				s.mu.Unlock()
				return err
			}
			s.characters = next
			s.mu.Unlock()
			return nil
		}
	}
	collection := s.collection
	s.mu.Unlock()
	if collection != nil {
		if c, ok := collection.FindCharacter(index); ok {
			c.ConnectPotentialCostume = costume
			return collection.UpdateCharacter(c.ID, c)
		}
	}
	return fmt.Errorf("player: unknown potential connection character")
}
