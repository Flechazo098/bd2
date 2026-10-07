package roster

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/storage/stateio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"time"
)

type characterSnapshot struct {
	Version        string   `json:"version"`
	CharacterOrder []uint64 `json:"character_order"`
}

// CharacterStore owns mutable character growth separately from immutable
// character design data. The seed supplies the initially owned instances.
type CharacterStore struct {
	store           stateio.ScopedEntryStore
	characters      []Character
	persisted       map[uint64]bool
	persistedOrder  []uint64
	persistedCore   bool
	inventory       *assets.Inventory
	gameDataRoot    string
	gameDataVersion string
	grow            func(Character, []gamedata.GrowthMaterial) (uint64, uint64, []gamedata.GrowthMaterial, error)
	collection      *CollectionStore
	wallet          *assets.Wallet
	maxHealth       func(command.Context, Character) (uint64, error)
	promoteGrowth   func(Character, []gamedata.PromotionCost) (gamedata.PromotionGrowthResult, error)
	talentGrowth    *gamedata.TalentGrowthDesign
	immortal        *gamedata.ImmortalDesign
	immortalReplies map[string]talentUpgradeReply

	talentReplies map[string]map[string]talentUpgradeReply
	talentApplied map[string]talentUpgradeReply
}

type talentUpgradeReply struct {
	Digest string `json:"digest"`
	Code   int    `json:"code"`
	Body   []byte `json:"body,omitempty"`
}

func (s *CharacterStore) AttachWallet(ctx command.Context, wallet *assets.Wallet) error {
	if wallet == nil {
		return errors.New("player: nil growth wallet")
	}

	s.wallet = wallet
	return nil
}

func (s *CharacterStore) AttachImmortalDesign(ctx command.Context, design *gamedata.ImmortalDesign) error {
	if design == nil {
		return errors.New("player: nil immortal design")
	}
	s.immortal = design
	return nil
}

func (s *CharacterStore) AttachTalentGrowth(ctx command.Context, design *gamedata.TalentGrowthDesign) error {
	if design == nil {
		return errors.New("player: nil talent growth design")
	}

	s.talentGrowth = design
	return nil
}

// AttachMaxHealth makes growth and post-battle revival consume the same
// GameData/collection calculator as AllCharRefresh and BattleEnter.
func (s *CharacterStore) AttachMaxHealth(ctx command.Context, maxHealth func(command.Context, Character) (uint64, error)) error {
	if maxHealth == nil {
		return errors.New("player: nil maximum health calculator")
	}

	s.maxHealth = maxHealth
	return nil
}

func (s *CharacterStore) AttachCollection(ctx command.Context, collection *CollectionStore) error {
	if collection == nil {
		return errors.New("player: nil collection store")
	}

	s.collection = collection
	return nil
}

func OpenCharacterStore(ctx command.Context, store stateio.Store, seed []Character, inventory *assets.Inventory, gameDataRoot, gameDataVersion string) (*CharacterStore, error) {
	if store == nil || inventory == nil {
		return nil, errors.New("player: invalid character store configuration")
	}
	entries, ok := store.(stateio.ScopedEntryStore)
	if !ok {
		return nil, errors.New("player: character store requires atomic entries")
	}
	s := &CharacterStore{store: entries, inventory: inventory, characters: append([]Character(nil), seed...), persisted: make(map[uint64]bool), gameDataRoot: gameDataRoot, gameDataVersion: gameDataVersion, talentReplies: make(map[string]map[string]talentUpgradeReply), talentApplied: make(map[string]talentUpgradeReply)}
	s.grow = func(character Character, materials []gamedata.GrowthMaterial) (uint64, uint64, []gamedata.GrowthMaterial, error) {
		return gamedata.CharacterGrowth(s.gameDataRoot, s.gameDataVersion, int(character.ID), character.Level, character.Exp, materials)
	}
	s.promoteGrowth = func(character Character, submitted []gamedata.PromotionCost) (gamedata.PromotionGrowthResult, error) {
		return gamedata.CharacterGrowthPromotions(s.gameDataRoot, s.gameDataVersion, int(character.ID), character.Level, character.Exp, submitted)
	}
	data, err := entries.Load(ctx.State, "characters")
	if err != nil {
		return nil, err
	}
	if data == nil {
		orphaned, err := entries.ListEntries(ctx.State, "characters", "characters")
		if err != nil {
			return nil, fmt.Errorf("player: list character entries: %w", err)
		}
		if len(orphaned) != 0 {
			return nil, errors.New("player: character entries exist without core")
		}
		applied, err := entries.ListEntries(ctx.State, "characters", "talent_upgrades")
		if err != nil {
			return nil, err
		}
		if len(applied) != 0 {
			return nil, errors.New("player: talent upgrade entries exist without character core")
		}
		return s, validateCharacters(s.characters)
	}
	saved, loaded, err := loadCharacterEntries(ctx, entries, data)
	if err != nil {
		return nil, err
	}
	s.characters = loaded
	s.persistedOrder = append([]uint64{}, saved.CharacterOrder...)
	s.persistedCore = true
	seen := make(map[uint64]bool, len(s.characters))
	for _, character := range s.characters {
		seen[character.InvenIndex] = true
		s.persisted[character.InvenIndex] = true
	}
	for _, character := range seed {
		if !seen[character.InvenIndex] {
			return nil, fmt.Errorf("player: current character state omits seeded inventory index %d", character.InvenIndex)
		}
	}
	if err := s.loadTalentUpgradeLedger(ctx, entries); err != nil {
		return nil, err
	}
	if err := validateCharacters(s.characters); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *CharacterStore) loadTalentUpgradeLedger(ctx command.Context, entries stateio.ScopedEntryStore) error {
	rows, err := entries.ListEntries(ctx.State, "characters", "talent_upgrades")
	if err != nil {
		return err
	}
	for key, payload := range rows {
		parts := strings.Split(key, ":")
		if len(parts) != 2 {
			return fmt.Errorf("player: invalid talent upgrade ledger key %q", key)
		}
		index, indexErr := strconv.ParseUint(parts[0], 10, 64)
		level, levelErr := strconv.ParseUint(parts[1], 10, 64)
		var reply talentUpgradeReply
		if indexErr != nil || levelErr != nil || index == 0 || level < 2 || json.Unmarshal(payload, &reply) != nil ||
			reply.Code != talentSkillUpgradePacketCode || len(reply.Digest) != 64 {
			return fmt.Errorf("player: invalid talent upgrade ledger entry %q", key)
		}
		if _, err := hex.DecodeString(reply.Digest); err != nil {
			return fmt.Errorf("player: invalid talent upgrade digest %q", key)
		}
		s.talentApplied[key] = reply
	}
	return nil
}

func (s *CharacterStore) EnsurePersisted(ctx command.Context) error {

	return s.persist(ctx, append([]Character(nil), s.characters...))
}

func validateCharacters(characters []Character) error {
	seen := make(map[uint64]bool, len(characters))
	for _, character := range characters {
		if character.InvenIndex == 0 || character.ID == 0 || character.Level == 0 || seen[character.InvenIndex] {
			return errors.New("player: invalid saved character")
		}
		seen[character.InvenIndex] = true
	}
	return nil
}

func (s *CharacterStore) All(ctx command.Context) []Character {
	characters := s.RawAll()
	active := characters[:0]
	for _, c := range characters {
		if !CharacterExpired(c, time.Now()) {
			active = append(active, c)
		}
	}
	characters = active
	for i := range characters {
		if hp, err := s.savedCurrentHealth(ctx, characters[i]); err == nil {
			characters[i].HP = hp
		}
	}
	return characters
}

// RawAll is for account-ownership queries used by the stat calculator itself.
// Calling All() from that calculator would recursively calculate its inputs.
func (s *CharacterStore) RawAll() []Character {

	characters := append([]Character(nil), s.characters...)
	collection := s.collection

	if collection != nil {
		characters = append(characters, collection.Characters()...)
	}
	return characters
}

func (s *CharacterStore) Find(ctx command.Context, inventoryIndex uint64) (Character, bool) {
	for _, character := range s.RawAll() {
		if character.InvenIndex == inventoryIndex {
			if CharacterExpired(character, time.Now()) {
				return Character{}, false
			}
			if hp, err := s.savedCurrentHealth(ctx, character); err == nil {
				character.HP = hp
			}
			return character, true
		}
	}
	return Character{}, false
}
