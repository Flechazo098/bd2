package gamedata

import (
	"fmt"
	"slices"
)

type AvatarPartRule struct{ Category, Character, Maximum, UnlockTextID uint64 }
type AvatarCharacterRule struct {
	Maximum          uint64
	Collaboration    bool
	Defaults, Banned []uint64
	Rewards          []Reward
}
type AvatarProduct struct {
	Reward, Price, Premium                         Reward
	Initial, Purchasable                           bool
	Visibility, UnlockType, UnlockValue, UnlockSub uint64
}
type AvatarProfileDesign struct {
	InitialUnlockTickets                  []uint64
	SquareChannels, SquareChannelCapacity uint64
	Characters                            map[uint64]AvatarCharacterRule
	Parts                                 map[uint64]AvatarPartRule
	MotionMaximum                         map[uint64]uint64
	Shop, MotionShop                      map[uint64]AvatarProduct
	Bundles                               *AvatarRewardDesign
	PrestigeSets                          map[uint64][]Reward
}

func LoadAvatarProfileDesign(root, version string) (*AvatarProfileDesign, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	d := &AvatarProfileDesign{Characters: map[uint64]AvatarCharacterRule{}, Parts: map[uint64]AvatarPartRule{}, MotionMaximum: map[uint64]uint64{}, Shop: map[uint64]AvatarProduct{}, MotionShop: map[uint64]AvatarProduct{}}
	d.Bundles, err = loadAvatarRewardDesign(db)
	if err != nil {
		return nil, err
	}
	d.PrestigeSets = map[uint64][]Reward{}
	if err := readCashMetadata(db, "AvatarSetTable", func(raw []byte) error {
		skin, err := optionalScalar(raw, 1)
		if err != nil || skin == 0 {
			return err
		}
		id, err := optionalScalar(raw, 6)
		if err != nil {
			return err
		}
		if len(d.PrestigeSets[skin]) != 0 {
			return fmt.Errorf("gamedata: duplicate prestige avatar set for skin %d", skin)
		}
		for _, reward := range d.Bundles.Sets[id] {
			if reward.Type != 49 || reward.Count != 1 {
				return fmt.Errorf("gamedata: invalid prestige avatar member")
			}
			d.PrestigeSets[skin] = append(d.PrestigeSets[skin], Reward(reward))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := readCashMetadata(db, "SquareDefaultTable", func(raw []byte) error {
		channels, err := optionalScalar(raw, 2)
		if err != nil {
			return err
		}
		capacity, err := optionalScalar(raw, 5)
		if err != nil || channels == 0 || channels > 10000 || capacity == 0 || capacity > 1000 {
			return fmt.Errorf("gamedata: invalid square channel rules")
		}
		d.SquareChannels, d.SquareChannelCapacity = channels, capacity
		tickets, err := packedInts(raw, 1)
		if err != nil {
			return err
		}
		if slices.Contains(tickets, uint64(0)) {
			return fmt.Errorf("gamedata: invalid avatar setup ticket")
		}
		d.InitialUnlockTickets = append(d.InitialUnlockTickets, tickets...)
		return nil
	}); err != nil {
		return nil, err
	}
	if err = readCashMetadata(db, "AvatarCharTable", func(raw []byte) error {
		id, err := optionalScalar(raw, 6)
		if err != nil || id == 0 {
			return fmt.Errorf("gamedata: invalid avatar character")
		}
		maximum, err := optionalScalar(raw, 11)
		if err != nil {
			return err
		}
		defaults, err := packedInts(raw, 5)
		if err != nil {
			return err
		}
		banned, err := packedInts(raw, 2)
		if err != nil {
			return err
		}
		collaboration, err := optionalScalar(raw, 7)
		if err != nil {
			return err
		}
		bonuses, err := eventGameRewards(raw, 14, 13, 12)
		if err != nil {
			return err
		}
		var rewards []Reward
		for _, bonus := range bonuses {
			rewards = append(rewards, Reward(bonus))
		}
		d.Characters[id] = AvatarCharacterRule{Maximum: maximum, Collaboration: collaboration == 1, Defaults: defaults, Banned: banned, Rewards: rewards}
		return nil
	}); err != nil {
		return nil, err
	}
	if err = readCashMetadata(db, "AvatarItemTable", func(raw []byte) error {
		id, err := optionalScalar(raw, 3)
		if err != nil || id == 0 {
			return fmt.Errorf("gamedata: invalid avatar part")
		}
		category, err := optionalScalar(raw, 1)
		if err != nil {
			return err
		}
		character, err := optionalScalar(raw, 2)
		if err != nil {
			return err
		}
		maximum, err := optionalScalar(raw, 9)
		if err != nil {
			return err
		}
		unlockText, err := optionalScalar(raw, 8)
		if err != nil {
			return err
		}
		d.Parts[id] = AvatarPartRule{Category: category, Character: character, Maximum: maximum, UnlockTextID: unlockText}
		return nil
	}); err != nil {
		return nil, err
	}
	if err = readCashMetadata(db, "AvatarMotionTable", func(raw []byte) error {
		id, err := optionalScalar(raw, 5)
		if err != nil || id == 0 {
			return fmt.Errorf("gamedata: invalid avatar motion")
		}
		maximum, err := optionalScalar(raw, 8)
		if err != nil {
			return err
		}
		d.MotionMaximum[id] = maximum
		return nil
	}); err != nil {
		return nil, err
	}
	for _, table := range []string{"AvatarShopTable", "AvatarMotionShopTable"} {
		err = readCashMetadata(db, table, func(raw []byte) error {
			values := make(map[int]uint64)
			for field := 1; field <= 18; field++ {
				n, err := optionalScalar(raw, field)
				if err != nil {
					return err
				}
				values[field] = n
			}
			if table == "AvatarMotionShopTable" {
				if values[4] == 0 || values[3] != 50 || values[1] == 0 {
					return fmt.Errorf("gamedata: invalid motion product")
				}
				d.MotionShop[values[4]] = AvatarProduct{Reward: Reward{values[3], values[2], values[1]}, Price: Reward{values[7], values[6], values[5]}, Purchasable: true}
				return nil
			}
			if values[5] == 0 {
				return fmt.Errorf("gamedata: invalid avatar product")
			}
			d.Shop[values[5]] = AvatarProduct{Reward: Reward{values[4], values[3], values[2]}, Price: Reward{values[16], values[15], values[14]}, Premium: Reward{values[13], values[12], values[11]}, Initial: values[6] == 1, Purchasable: values[7] == 1, Visibility: values[17], UnlockType: values[9], UnlockValue: values[10], UnlockSub: values[8]}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return d, nil
}
