package player

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

// FoodService participates in the account request transaction: consuming the
// stack, saving health and recording the replay response commit together.
type FoodService struct {
	mu           sync.Mutex
	store        stateio.AtomicEntryStore
	design       *gamedata.FoodDesign
	inventory    *Inventory
	characters   *CharacterStore
	session      string
	currentPack  func() (int, error)
	battleActive func() bool
}

type foodReply struct {
	Digest string `json:"digest"`
	Body   []byte `json:"body"`
}

func OpenFoodService(store stateio.Store, design *gamedata.FoodDesign, inventory *Inventory, characters *CharacterStore) (*FoodService, error) {
	entries, ok := store.(stateio.AtomicEntryStore)
	if !ok || design == nil || inventory == nil || characters == nil {
		return nil, errors.New("player: incomplete food configuration")
	}
	s := &FoodService{store: entries, design: design, inventory: inventory, characters: characters}
	health, err := entries.ListEntries("characters", "current_hp")
	if err != nil {
		return nil, err
	}
	for key, raw := range health {
		index, err := strconv.ParseUint(key, 10, 64)
		var hp uint64
		if err != nil || index == 0 || json.Unmarshal(raw, &hp) != nil {
			return nil, errors.New("player: malformed saved current health")
		}
		if _, err := characters.MaxHealth(index); err != nil {
			return nil, err
		}
	}
	replies, err := entries.ListEntries("characters", "food_requests")
	if err != nil {
		return nil, err
	}
	for key, raw := range replies {
		var reply foodReply
		decodedKey, keyErr := hex.DecodeString(key)
		if keyErr != nil || len(decodedKey) != sha256.Size || json.Unmarshal(raw, &reply) != nil || len(reply.Body) == 0 {
			return nil, errors.New("player: malformed saved food replay")
		}
		digest, err := hex.DecodeString(reply.Digest)
		if err != nil || len(digest) != sha256.Size {
			return nil, errors.New("player: malformed saved food replay digest")
		}
		if err := wire.Walk(reply.Body, func(field wire.Field) error { return nil }); err != nil {
			return nil, errors.New("player: malformed saved food replay body")
		}
	}
	return s, nil
}

func (s *FoodService) BeginSession(id string) { s.mu.Lock(); defer s.mu.Unlock(); s.session = id }

// AttachContext supplies authoritative pack and active-battle state; the client
// omits PackId in ordinary recovery requests, which means the current pack.
func (s *FoodService) AttachContext(currentPack func() (int, error), battleActive func() bool) error {
	if currentPack == nil || battleActive == nil {
		return errors.New("player: incomplete food context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentPack = currentPack
	s.battleActive = battleActive
	return nil
}

func (s *FoodService) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path == "/EatFoodAuto" {
		return s.eatFoodAuto(request)
	}
	if path != "/EatFood" {
		return 0, nil, false, nil
	}
	var seq, pack, index uint64
	seenHeaders := map[int]bool{}
	parseErr := wire.Walk(request, func(field wire.Field) error {
		if field.Number == 4 {
			return nil
		}
		target := map[int]*uint64{1: &seq, 2: &pack, 3: &index}[field.Number]
		if target == nil || field.Type != 0 || seenHeaders[field.Number] {
			return errors.New("player: invalid EatFood header")
		}
		seenHeaders[field.Number] = true
		*target, _ = binary.Uvarint(field.Value)
		return nil
	})
	if parseErr != nil || seq == 0 || seq > math.MaxInt32 || pack > math.MaxInt32 || index == 0 || index > math.MaxInt64 {
		return 0, nil, true, errors.New("player: invalid EatFood request")
	}
	items, err := equipmentRequestItems(request, 4, "EatFood")
	if err != nil || len(items) == 0 {
		return 0, nil, true, errors.New("player: EatFood requires food stacks")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == "" || s.currentPack == nil || s.battleActive == nil {
		return 0, nil, true, errors.New("player: EatFood session or context unavailable")
	}
	keyDigest := sha256.Sum256([]byte(s.session + ":EatFood:" + strconv.FormatUint(seq, 10)))
	key := hex.EncodeToString(keyDigest[:])
	digest := sha256.Sum256(request)
	digestString := hex.EncodeToString(digest[:])
	prior, found, err := s.store.LoadEntry("characters", "food_requests", key)
	if err != nil {
		return 0, nil, true, err
	}
	if found {
		var reply foodReply
		if json.Unmarshal(prior, &reply) != nil || reply.Digest != digestString {
			return 0, nil, true, errors.New("player: EatFood sequence reused with different request")
		}
		return 22, append([]byte(nil), reply.Body...), true, nil
	}
	currentPack, err := s.currentPack()
	if err != nil || currentPack <= 0 || (pack != 0 && pack != uint64(currentPack)) {
		return 0, nil, true, errors.New("player: EatFood pack unavailable")
	}
	if s.battleActive() {
		return 0, nil, true, errors.New("player: cannot eat food during battle")
	}
	character, err := s.recoverCharacter(index, items)
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.inventory.CanConsume(items); err != nil {
		return 0, nil, true, err
	}
	body := wire.AppendBytes(nil, 1, CharacterWire(character))
	if err := s.inventory.Consume(items); err != nil {
		return 0, nil, true, err
	}
	healthRaw, _ := json.Marshal(character.HP)
	replyRaw, _ := json.Marshal(foodReply{Digest: digestString, Body: body})
	if err := s.store.SaveWithEntries("characters", nil, []stateio.EntryMutation{{Bucket: "current_hp", Key: strconv.FormatUint(index, 10), Payload: healthRaw}, {Bucket: "food_requests", Key: key, Payload: replyRaw}}); err != nil {
		return 0, nil, true, err
	}
	return 22, body, true, nil
}

// MaxHealth reads the shared stat calculator without replacing it with field HP.
func (s *CharacterStore) MaxHealth(index uint64) (uint64, error) {
	var character Character
	for _, candidate := range s.RawAll() {
		if candidate.InvenIndex == index {
			character = candidate
			break
		}
	}
	if character.InvenIndex == 0 {
		return 0, fmt.Errorf("player: unknown health character %d", index)
	}
	s.mu.Lock()
	calculate := s.maxHealth
	s.mu.Unlock()
	if calculate != nil {
		return calculate(character)
	}
	if character.HP == 0 {
		return 0, errors.New("player: maximum health unavailable")
	}
	return character.HP, nil
}

func (s *CharacterStore) CurrentHealth(index uint64) (uint64, error) {
	maximum, err := s.MaxHealth(index)
	if err != nil {
		return 0, err
	}
	return s.currentHealthAtMaximum(index, maximum)
}

func (s *CharacterStore) currentHealthAtMaximum(index, maximum uint64) (uint64, error) {
	if s.store == nil {
		return maximum, nil
	}
	data, found, err := s.store.LoadEntry("characters", "current_hp", strconv.FormatUint(index, 10))
	if err != nil || !found {
		return maximum, err
	}
	var hp uint64
	if json.Unmarshal(data, &hp) != nil {
		return 0, errors.New("player: invalid saved current health")
	}
	if hp > maximum {
		hp = maximum
	}
	return hp, nil
}

// SetCurrentHealth accepts server-validated battle results and recovery only.
// Food requests never accept health from the caller.
func (s *CharacterStore) SetCurrentHealth(index, hp uint64) error {
	maximum, err := s.MaxHealth(index)
	if err != nil {
		return err
	}
	if hp > maximum {
		return errors.New("player: current health exceeds maximum")
	}
	raw, _ := json.Marshal(hp)
	return s.store.PutEntry("characters", "current_hp", strconv.FormatUint(index, 10), raw)
}

func (s *CharacterStore) resetCurrentHealth(index uint64) error {
	_, err := s.store.DeleteEntry("characters", "current_hp", strconv.FormatUint(index, 10))
	return err
}

func (s *FoodService) eatFoodAuto(request []byte) (int, []byte, bool, error) {
	var seq uint64
	var targets []struct {
		index uint64
		items []Item
	}
	seenSequence := false
	err := wire.Walk(request, func(field wire.Field) error {
		switch field.Number {
		case 1:
			if field.Type != 0 || seenSequence {
				return errors.New("player: invalid EatFoodAuto sequence")
			}
			seenSequence = true
			seq, _ = binary.Uvarint(field.Value)
		case 2:
			if field.Type != 2 {
				return errors.New("player: invalid EatFoodAuto target")
			}
			var index uint64
			seenIndex := false
			if err := wire.Walk(field.Value, func(inner wire.Field) error {
				if inner.Number == 2 {
					return nil
				}
				if inner.Number != 1 || inner.Type != 0 || seenIndex {
					return errors.New("player: invalid EatFoodAuto character")
				}
				seenIndex = true
				index, _ = binary.Uvarint(inner.Value)
				return nil
			}); err != nil {
				return err
			}
			if index == 0 || index > math.MaxInt64 {
				return errors.New("player: invalid EatFoodAuto character index")
			}
			items, err := equipmentRequestItems(field.Value, 2, "EatFoodAuto")
			if err != nil {
				return err
			}
			if len(items) == 0 {
				return errors.New("player: EatFoodAuto character requires food stacks")
			}
			targets = append(targets, struct {
				index uint64
				items []Item
			}{index, items})
		default:
			return errors.New("player: unknown EatFoodAuto field")
		}
		return nil
	})
	if err != nil || seq == 0 || seq > math.MaxInt32 || len(targets) == 0 {
		return 0, nil, true, errors.New("player: invalid EatFoodAuto request")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == "" || s.currentPack == nil || s.battleActive == nil {
		return 0, nil, true, errors.New("player: EatFoodAuto context unavailable")
	}
	keyDigest := sha256.Sum256([]byte(s.session + ":EatFoodAuto:" + strconv.FormatUint(seq, 10)))
	key := hex.EncodeToString(keyDigest[:])
	digest := sha256.Sum256(request)
	digestString := hex.EncodeToString(digest[:])
	prior, found, err := s.store.LoadEntry("characters", "food_requests", key)
	if err != nil {
		return 0, nil, true, err
	}
	if found {
		var reply foodReply
		if json.Unmarshal(prior, &reply) != nil || reply.Digest != digestString {
			return 0, nil, true, errors.New("player: EatFoodAuto sequence reused with different request")
		}
		return 27, append([]byte(nil), reply.Body...), true, nil
	}
	pack, err := s.currentPack()
	if err != nil || pack <= 0 || s.battleActive() {
		return 0, nil, true, errors.New("player: EatFoodAuto unavailable during battle or outside pack")
	}
	seenCharacters := map[uint64]bool{}
	var items []Item
	var body []byte
	var changes []stateio.EntryMutation
	for _, target := range targets {
		if seenCharacters[target.index] {
			return 0, nil, true, errors.New("player: duplicate EatFoodAuto character")
		}
		seenCharacters[target.index] = true
		character, err := s.recoverCharacter(target.index, target.items)
		if err != nil {
			return 0, nil, true, err
		}
		items = append(items, target.items...)
		raw, _ := json.Marshal(character.HP)
		changes = append(changes, stateio.EntryMutation{Bucket: "current_hp", Key: strconv.FormatUint(target.index, 10), Payload: raw})
		info := wire.AppendVarint(nil, 1, target.index)
		info = wire.AppendVarint(info, 2, character.HP)
		body = wire.AppendBytes(body, 1, info)
	}
	// CanConsume accounts for a shared stack requested by several characters.
	if err := s.inventory.CanConsume(items); err != nil {
		return 0, nil, true, err
	}
	if err := s.inventory.Consume(items); err != nil {
		return 0, nil, true, err
	}
	replyRaw, _ := json.Marshal(foodReply{Digest: digestString, Body: body})
	changes = append(changes, stateio.EntryMutation{Bucket: "food_requests", Key: key, Payload: replyRaw})
	if err := s.store.SaveWithEntries("characters", nil, changes); err != nil {
		return 0, nil, true, err
	}
	return 27, body, true, nil
}

func (s *FoodService) recoverCharacter(index uint64, items []Item) (Character, error) {
	character, found := s.characters.Find(index)
	if !found {
		return Character{}, errors.New("player: EatFood character is not owned")
	}
	maximum, err := s.characters.MaxHealth(index)
	if err != nil {
		return Character{}, err
	}
	current, err := s.characters.CurrentHealth(index)
	if err != nil {
		return Character{}, err
	}
	var total uint64
	seen := map[uint64]bool{}
	for _, item := range items {
		if item.Type != 5 || item.InvenIndex == 0 || item.Count == 0 || item.Count > math.MaxInt32 || seen[item.InvenIndex] {
			return Character{}, errors.New("player: invalid EatFood inventory stack")
		}
		seen[item.InvenIndex] = true
		food, exists := s.design.Foods[item.ID]
		if !exists {
			return Character{}, errors.New("player: unknown EatFood dish")
		}
		value, err := food.Recovery(character.ID, maximum, item.Count)
		if err != nil {
			return Character{}, err
		}
		if value > math.MaxUint64-total {
			return Character{}, errors.New("player: EatFood recovery overflow")
		}
		total += value
	}
	if total >= maximum-current {
		character.HP = maximum
	} else {
		character.HP = current + total
	}
	return character, nil
}
