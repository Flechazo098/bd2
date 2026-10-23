// Package startergift selects onboarding gifts from versioned policy and design
// data. Issued mail remains an immutable account record.
package startergift

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"bd2server/internal/server/design/gamedata"
)

const (
	LimitedCostumes = "starter:limited-costumes:not-current-pickup:v1"
	PrestigeSkins   = "starter:prestige-skins:not-current-purchase:v1"
	LimitedAvatars  = "starter:limited-avatars:external-sources:v1"
	PrestigeAvatars = "starter:prestige-avatar:"
)

type PartSource struct {
	ID           uint64 `json:"id"`
	UnlockTextID uint64 `json:"unlock_text_id"`
	Source       string `json:"source"`
}

type Definition struct {
	Identity string       `json:"identity"`
	Title    string       `json:"title"`
	Body     string       `json:"body"`
	Parts    []PartSource `json:"external_avatar_parts,omitempty"`
}

type Catalog struct {
	Version         string       `json:"version"`
	GameDataVersion string       `json:"gamedata_version"`
	Gifts           []Definition `json:"gifts"`
}

type Gift struct {
	Identity, Title, Body string
	Rewards               []gamedata.Reward
}

type Selection struct {
	Limited       *gamedata.LimitedCostumeCatalog
	Prestige      *gamedata.PrestigeSkinCatalog
	Avatars       *gamedata.AvatarProfileDesign
	Featured      map[uint64]bool
	CashAvailable func(gamedata.CashProductKey) bool
	OwnedPrestige map[uint64]bool
}

func (c Catalog) Resolve(selection Selection) ([]Gift, error) {
	if len(c.Gifts) != 4 || selection.Limited == nil || selection.Prestige == nil || selection.Avatars == nil || selection.Avatars.Bundles == nil {
		return nil, fmt.Errorf("startergift: incomplete catalog or design")
	}
	seen := map[string]bool{}
	var gifts []Gift
	for _, definition := range c.Gifts {
		if seen[definition.Identity] || definition.Title == "" || definition.Body == "" {
			return nil, fmt.Errorf("startergift: invalid gift definition %q", definition.Identity)
		}
		seen[definition.Identity] = true
		gift := Gift{Identity: definition.Identity, Title: definition.Title, Body: definition.Body}
		switch definition.Identity {
		case LimitedCostumes:
			for _, id := range selection.Limited.Excluding(selection.Featured) {
				costume, ok := selection.Limited.Character(id)
				if !ok || costume.CostumeMaxLevel != 5 {
					return nil, fmt.Errorf("startergift: limited costume %d does not support +5", id)
				}
				gift.Rewards = append(gift.Rewards, gamedata.Reward{Type: 11, ID: id, Count: 6})
			}
		case PrestigeSkins:
			for _, id := range selection.Prestige.Giftable(selection.CashAvailable) {
				gift.Rewards = append(gift.Rewards, gamedata.Reward{Type: 45, ID: id, Count: 1})
			}
		case LimitedAvatars:
			var err error
			gift.Rewards, err = avatarRewards(definition.Parts, selection)
			if err != nil {
				return nil, err
			}
		case PrestigeAvatars:
			defaults := defaultAvatarParts(selection.Avatars)
			ids := make([]uint64, 0, len(selection.OwnedPrestige))
			for id, owned := range selection.OwnedPrestige {
				if owned && len(selection.Avatars.PrestigeSets[id]) != 0 {
					ids = append(ids, id)
				}
			}
			slices.Sort(ids)
			for _, id := range ids {
				if selection.Prestige.Skins[id] == 0 {
					return nil, fmt.Errorf("startergift: unknown owned prestige skin %d", id)
				}
				linked := Gift{Identity: PrestigeAvatars + strconv.FormatUint(id, 10) + ":v1", Title: gift.Title, Body: gift.Body}
				for _, reward := range selection.Avatars.PrestigeSets[id] {
					if !defaults[reward.ID] {
						linked.Rewards = append(linked.Rewards, reward)
					}
				}
				if len(linked.Rewards) == 0 {
					continue
				}
				if err := ValidateRewards(linked.Identity, linked.Rewards); err != nil {
					return nil, err
				}
				gifts = append(gifts, linked)
			}
		default:
			return nil, fmt.Errorf("startergift: unknown gift identity %q", definition.Identity)
		}
		if definition.Identity != LimitedAvatars && len(definition.Parts) != 0 {
			return nil, fmt.Errorf("startergift: avatar sources on unrelated gift")
		}
		if len(gift.Rewards) != 0 {
			if err := ValidateRewards(gift.Identity, gift.Rewards); err != nil {
				return nil, err
			}
			gifts = append(gifts, gift)
		}
	}
	return gifts, nil
}

func avatarRewards(sources []PartSource, selection Selection) ([]gamedata.Reward, error) {
	design := selection.Avatars
	unique := map[[2]uint64]bool{}
	add := func(reward gamedata.Reward) error {
		if reward.Count != 1 || reward.Type != 49 || !design.Bundles.Items[reward.Type][reward.ID] {
			return fmt.Errorf("startergift: invalid limited avatar member %d:%d", reward.Type, reward.ID)
		}
		unique[[2]uint64{reward.Type, reward.ID}] = true
		return nil
	}
	seen := map[uint64]bool{}
	for _, source := range sources {
		part, ok := design.Parts[source.ID]
		product, exists := design.Shop[source.ID]
		if !ok || !exists || source.ID == 0 || seen[source.ID] || source.Source == "" || source.UnlockTextID == 0 ||
			part.UnlockTextID != source.UnlockTextID || part.Character != 0 || part.Maximum != 1 || product.Purchasable ||
			product.UnlockValue != 0 || product.Reward != (gamedata.Reward{Type: 49, ID: source.ID, Count: 1}) {
			return nil, fmt.Errorf("startergift: external avatar source %d does not match current design", source.ID)
		}
		seen[source.ID] = true
		if err := add(product.Reward); err != nil {
			return nil, err
		}
	}
	keys := make([][2]uint64, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b [2]uint64) int {
		if a[0] != b[0] {
			return int(a[0]) - int(b[0])
		}
		return int(a[1]) - int(b[1])
	})
	rewards := make([]gamedata.Reward, 0, len(keys))
	for _, key := range keys {
		rewards = append(rewards, gamedata.Reward{Type: key[0], ID: key[1], Count: 1})
	}
	return rewards, nil
}

func defaultAvatarParts(design *gamedata.AvatarProfileDesign) map[uint64]bool {
	defaults := map[uint64]bool{}
	for _, character := range design.Characters {
		for _, id := range character.Defaults {
			defaults[id] = true
		}
		for _, reward := range character.Rewards {
			if reward.Type == 49 {
				defaults[reward.ID] = true
			}
		}
	}
	for _, product := range design.Shop {
		if !product.Initial {
			continue
		}
		if product.Reward.Type == 49 {
			defaults[product.Reward.ID] = true
		}
		if product.Reward.Type == 62 {
			for _, reward := range design.Bundles.Sets[product.Reward.ID] {
				if reward.Type == 49 {
					defaults[reward.ID] = true
				}
			}
		}
	}
	return defaults
}

// Saved attachments need not match today's selection: issuing and claiming a
// gift freezes its reward list even when shop availability later changes.
func ValidateRewards(identity string, rewards []gamedata.Reward) error {
	seen := map[[2]uint64]bool{}
	if len(rewards) == 0 {
		return fmt.Errorf("startergift: empty gift %q", identity)
	}
	for _, reward := range rewards {
		key := [2]uint64{reward.Type, reward.ID}
		valid := identity == LimitedCostumes && reward.Type == 11 && reward.Count == 6 ||
			identity == PrestigeSkins && reward.Type == 45 && reward.Count == 1 ||
			IsAvatarGift(identity) && reward.Type == 49 && reward.Count == 1
		if !valid || reward.ID == 0 || seen[key] {
			return fmt.Errorf("startergift: invalid saved gift %q reward %d:%d", identity, reward.Type, reward.ID)
		}
		seen[key] = true
	}
	return nil
}

func IsAvatarGift(identity string) bool {
	if identity == LimitedAvatars {
		return true
	}
	if !strings.HasPrefix(identity, PrestigeAvatars) || !strings.HasSuffix(identity, ":v1") {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(identity, PrestigeAvatars), ":v1")
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n != 0 && strconv.FormatUint(n, 10) == id
}
