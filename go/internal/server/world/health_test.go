package world

import (
	"strconv"
	"testing"

	"bd2server/internal/server/player"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func TestTutorialCharInfoRestoresFieldHealthWithoutExposingStoryRoster(t *testing.T) {
	for _, hp := range []uint64{0, 17} {
		t.Run(strconv.FormatUint(hp, 10), func(t *testing.T) {
			storage := stateio.NewMemory()
			starter := &player.Starter{Version: "2.35.10", Characters: []player.Character{{InvenIndex: 77, ID: 350, Level: 1, HP: 100}}}
			inventory, err := player.OpenInventory(storage, starter)
			if err != nil {
				t.Fatal(err)
			}
			all := append(append([]player.Character(nil), starter.Characters...), player.Character{InvenIndex: 88, ID: 650, HP: 100, Level: 1})
			characters, err := player.OpenCharacterStore(storage, all, inventory, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := characters.AttachMaxHealth(func(player.Character) (uint64, error) { return 100, nil }); err != nil {
				t.Fatal(err)
			}
			if err := characters.EnsurePersisted(); err != nil {
				t.Fatal(err)
			}
			if err := characters.SetCurrentHealth(77, hp); err != nil {
				t.Fatal(err)
			}
			characters, err = player.OpenCharacterStore(storage, all, inventory, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := characters.AttachMaxHealth(func(player.Character) (uint64, error) { return 100, nil }); err != nil {
				t.Fatal(err)
			}
			s := &Service{seed: Seed{PackID: 21, BattleUnlockQuestID: 26}, state: progress.NewStore(), starter: starter, characters: characters}
			attachTestStoryCatalog(s)
			code, response, handled, err := s.Handle("/CharInfo", wire.AppendVarint(nil, 1, 1))
			if err != nil || code != 9 || !handled {
				t.Fatalf("code=%d handled=%v err=%v", code, handled, err)
			}
			count := 0
			if err := wire.Walk(response, func(field wire.Field) error {
				if field.Number == 1 {
					count++
					index, _, _ := wire.Varint(field.Value, 1)
					got, _, _ := wire.Varint(field.Value, 3)
					if index != 77 || got != hp {
						t.Fatalf("tutorial character=%d hp=%d want=%d", index, got, hp)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("tutorial exposed %d characters", count)
			}
			if _, _, handled, err := s.Handle("/CharInfo", nil); !handled || err == nil {
				t.Fatal("invalid character request accepted")
			}
		})
	}
}

func TestPackInfoWithoutFormationDoesNotEmitSeedStoryRoster(t *testing.T) {
	storage := stateio.NewMemory()
	starter := &player.Starter{Version: "2.35.10"}
	inventory, err := player.OpenInventory(storage, starter)
	if err != nil {
		t.Fatal(err)
	}
	all := []player.Character{{InvenIndex: 77, ID: 350, HP: 100, Level: 1}, {InvenIndex: 88, ID: 650, HP: 100, Level: 1}}
	characters, err := player.OpenCharacterStore(storage, all, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := characters.AttachMaxHealth(func(player.Character) (uint64, error) { return 100, nil }); err != nil {
		t.Fatal(err)
	}
	if err := characters.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	if err := characters.SetCurrentHealth(77, 0); err != nil {
		t.Fatal(err)
	}
	if err := characters.SetCurrentHealth(88, 23); err != nil {
		t.Fatal(err)
	}
	characters, err = player.OpenCharacterStore(storage, all, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := characters.AttachMaxHealth(func(player.Character) (uint64, error) { return 100, nil }); err != nil {
		t.Fatal(err)
	}
	state := progress.NewStore()
	if err := state.ClearQuest(26, 21); err != nil {
		t.Fatal(err)
	}
	s := &Service{seed: Seed{PackID: 21, BattleUnlockQuestID: 26, RewardCharacter: all[0], StoryCharacters: all[1:]}, state: state, starter: starter, characters: characters}
	attachTestStoryCatalog(s)
	response, err := s.packInfoFor(21)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number == 1 {
			count++
			index, _, _ := wire.Varint(field.Value, 1)
			hp, _, _ := wire.Varint(field.Value, 3)
			want := map[uint64]uint64{77: 0, 88: 23}
			if expected, exists := want[index]; !exists || hp != expected {
				t.Fatalf("pack character %d hp=%d", index, hp)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unconditional seed story roster count=%d", count)
	}
}
