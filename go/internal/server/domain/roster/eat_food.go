package roster

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// FoodService participates in the account request transaction: consuming the
// stack, saving health and recording the replay response commit together.
type FoodService struct {
	store      stateio.ScopedEntryStore
	design     *gamedata.FoodDesign
	inventory  *assets.Inventory
	characters *CharacterStore

	currentPack  func(command.Context) (int, error)
	battleActive func(command.Context) bool
}

type foodReply struct {
	Digest string `json:"digest"`
	Body   []byte `json:"body"`
}

// AttachContext supplies authoritative pack and active-battle state; the client
// omits PackId in ordinary recovery requests, which means the current pack.
func (s *FoodService) AttachContext(ctx command.Context, currentPack func(command.Context) (int, error), battleActive func(command.Context) bool) error {
	if currentPack == nil || battleActive == nil {
		return errors.New("player: incomplete food context")
	}

	s.currentPack = currentPack
	s.battleActive = battleActive
	return nil
}

// MaxHealth reads the shared stat calculator without replacing it with field HP.
func (s *CharacterStore) MaxHealth(ctx command.Context, index uint64) (uint64, error) {
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

	calculate := s.maxHealth

	if calculate != nil {
		return calculate(ctx, character)
	}
	if character.HP == 0 {
		return 0, errors.New("player: maximum health unavailable")
	}
	return character.HP, nil
}

// CurrentHealth resolves persisted current HP independently from maximum HP.
// A missing current_hp entry retains the owned character record's HP, including
// zero. Recomputing equipment or other maximum-health stats must never heal it.
func (s *CharacterStore) CurrentHealth(ctx command.Context, index uint64) (uint64, error) {
	var saved Character
	for _, character := range s.RawAll() {
		if character.InvenIndex == index {
			saved = character
			break
		}
	}
	if saved.InvenIndex == 0 {
		return 0, fmt.Errorf("player: unknown health character %d", index)
	}
	return s.savedCurrentHealth(ctx, saved)
}

func (s *CharacterStore) savedCurrentHealth(ctx command.Context, saved Character) (uint64, error) {
	if s.store == nil {
		return saved.HP, nil
	}
	data, found, err := s.store.LoadEntry(ctx.State, "characters", "current_hp", strconv.FormatUint(saved.InvenIndex, 10))
	if err != nil {
		return 0, err
	}
	if !found {
		return saved.HP, nil
	}
	var hp uint64
	if json.Unmarshal(data, &hp) != nil {
		return 0, errors.New("player: invalid saved current health")
	}
	return hp, nil
}

// SetCurrentHealth accepts server-validated battle results and recovery only.
// Food requests never accept health from the caller.
func (s *CharacterStore) SetCurrentHealth(ctx command.Context, index, hp uint64) error {
	maximum, err := s.MaxHealth(ctx, index)
	if err != nil {
		return err
	}
	if hp > maximum {
		return errors.New("player: current health exceeds maximum")
	}
	raw, _ := json.Marshal(hp)
	return s.store.PutEntry(ctx.State, "characters", "current_hp", strconv.FormatUint(index, 10), raw)
}

func (s *CharacterStore) resetCurrentHealth(ctx command.Context, index uint64) error {
	_, err := s.store.DeleteEntry(ctx.State, "characters", "current_hp", strconv.FormatUint(index, 10))
	return err
}

func (s *FoodService) recoverCharacter(ctx command.Context, index uint64, items []assets.Item) (Character, error) {
	character, found := s.characters.Find(ctx, index)
	if !found {
		return Character{}, errors.New("player: EatFood character is not owned")
	}
	maximum, err := s.characters.MaxHealth(ctx, index)
	if err != nil {
		return Character{}, err
	}
	current, err := s.characters.CurrentHealth(ctx, index)
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
	if current > maximum {
		current = maximum
	}
	if total >= maximum-current {
		character.HP = maximum
	} else {
		character.HP = current + total
	}
	return character, nil
}
