package app

import (
	"encoding/json"
	"errors"
	"fmt"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/storage/stateio"
)

type seedInstanceIDs struct {
	Characters map[uint64]uint64 `json:"characters"`
	Costumes   map[uint64]uint64 `json:"costumes"`
	Items      map[uint64]uint64 `json:"items"`
}

func (p *playerAssembly) seedInstances(ctx command.Context) error {
	raw, found, err := ctx.State.LoadEntry("bootstrap", "instances", "seeds")
	if err != nil {
		return err
	}
	ids := seedInstanceIDs{Characters: map[uint64]uint64{}, Costumes: map[uint64]uint64{}, Items: map[uint64]uint64{}}
	if found {
		if err := stateio.RequireExactJSONObject(raw, "characters", "costumes", "items"); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &ids); err != nil {
			return err
		}
		if ids.Characters == nil || ids.Costumes == nil || ids.Items == nil {
			return errors.New("account seed: invalid instance mapping")
		}
	} else if !p.initializeAccount {
		return errors.New("account seed: global instance mapping missing; offline migration required")
	}
	register := func(values map[uint64]uint64, old, class uint64) error {
		if old == 0 {
			return nil
		}
		if id, exists := values[old]; exists {
			if id&(roster.CharmCharacterIndexBase-1) == 0 || id&^(roster.CharmCharacterIndexBase-1) != class {
				return fmt.Errorf("account seed: invalid instance %d", old)
			}
			return nil
		}
		if found {
			return fmt.Errorf("account seed: unmapped instance %d", old)
		}
		id, err := ctx.NewInstanceID()
		if err != nil {
			return err
		}
		values[old] = id | class
		return nil
	}
	for _, character := range p.starter.Characters {
		if err := register(ids.Characters, character.InvenIndex, 0); err != nil {
			return err
		}
	}
	if err := register(ids.Characters, p.worldSeed.RewardCharacter.InvenIndex, 0); err != nil {
		return err
	}
	for _, character := range p.worldSeed.StoryCharacters {
		if err := register(ids.Characters, character.InvenIndex, roster.StoryCharacterIndexBase); err != nil {
			return err
		}
	}
	for _, costume := range p.starter.Costumes {
		if err := register(ids.Costumes, costume.InvenIndex, 0); err != nil {
			return err
		}
	}
	if err := register(ids.Costumes, p.worldSeed.RewardCostume.InvenIndex, 0); err != nil {
		return err
	}
	for _, item := range p.starter.Items {
		if err := register(ids.Items, item.InvenIndex, 0); err != nil {
			return err
		}
	}
	for _, item := range p.starter.OwnedItems {
		if err := register(ids.Items, item.InvenIndex, 0); err != nil {
			return err
		}
	}
	seen := make(map[uint64]bool)
	for _, values := range []map[uint64]uint64{ids.Characters, ids.Costumes, ids.Items} {
		for _, id := range values {
			if seen[id] {
				return fmt.Errorf("account seed: duplicate instance %d", id)
			}
			seen[id] = true
		}
	}
	if !found {
		raw, err = json.Marshal(ids)
		if err != nil {
			return err
		}
		if err := ctx.State.PutEntry("bootstrap", "instances", "seeds", raw); err != nil {
			return err
		}
	}
	remap := func(values map[uint64]uint64, old uint64) (uint64, error) {
		if old == 0 {
			return 0, nil
		}
		id, exists := values[old]
		if !exists || id == 0 {
			return 0, fmt.Errorf("account seed: unresolved instance reference %d", old)
		}
		return id, nil
	}
	character := func(value *roster.Character) error {
		value.InvenIndex, err = remap(ids.Characters, value.InvenIndex)
		if err != nil {
			return err
		}
		value.UseCostume, err = remap(ids.Costumes, value.UseCostume)
		return err
	}
	costume := func(value *roster.Costume) error {
		value.InvenIndex, err = remap(ids.Costumes, value.InvenIndex)
		if err != nil {
			return err
		}
		value.UseChar, err = remap(ids.Characters, value.UseChar)
		return err
	}
	for i := range p.starter.Characters {
		if err := character(&p.starter.Characters[i]); err != nil {
			return err
		}
	}
	if err := character(&p.worldSeed.RewardCharacter); err != nil {
		return err
	}
	for i := range p.worldSeed.StoryCharacters {
		if err := character(&p.worldSeed.StoryCharacters[i]); err != nil {
			return err
		}
	}
	for i := range p.starter.Costumes {
		if err := costume(&p.starter.Costumes[i]); err != nil {
			return err
		}
	}
	if err := costume(&p.worldSeed.RewardCostume); err != nil {
		return err
	}
	for i := range p.starter.Items {
		p.starter.Items[i].InvenIndex, err = remap(ids.Items, p.starter.Items[i].InvenIndex)
		if err != nil {
			return err
		}
	}
	for i := range p.starter.OwnedItems {
		p.starter.OwnedItems[i].InvenIndex, err = remap(ids.Items, p.starter.OwnedItems[i].InvenIndex)
		if err != nil {
			return err
		}
	}
	for i := range p.deckSeed.FieldDeck {
		entry := &p.deckSeed.FieldDeck[i]
		entry.CharacterInvenIndex, err = remap(ids.Characters, entry.CharacterInvenIndex)
		if err != nil {
			return err
		}
		entry.CostumeInvenIndex, err = remap(ids.Costumes, entry.CostumeInvenIndex)
		if err != nil {
			return err
		}
	}
	return nil
}
