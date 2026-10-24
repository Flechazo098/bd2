//go:build integration

package world

import (
	"slices"
	"testing"

	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

type lifeQuestLedger struct {
	identities []string
	rewards    [][]gamedata.Reward
}

func (l *lifeQuestLedger) Apply(_ command.Context, identity string, _ []gamedata.Reward, rewards []gamedata.Reward) ([]byte, error) {
	l.identities = append(l.identities, identity)
	l.rewards = append(l.rewards, rewards)
	var bundle []byte
	for _, r := range rewards {
		bundle = wire.AppendBytes(bundle, 6, wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 2, r.ID), 3, r.Type), 4, r.Count))
	}
	return bundle, nil
}

// Pack 3011 is the farm. Its fifteen Life quests form one chain: the current
// one is always active, QuestUpdate records LifeQuestController's progress in
// QuestDBInfo.value, and QuestClear pays life rewards through the life economy
// and activates the next quest.
func TestLifeQuestChainActivatesAdvancesAndPaysLifeRewards(t *testing.T) {
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	packs, err := gamedata.LoadFieldPacks(versions.Resolve("data/resources/GameData"), versions.GameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	farm := packs[3011]
	if farm.Type != gamedata.LifePackType || len(farm.LifeQuests) != 15 {
		t.Fatalf("pack 3011 type=%d life quests=%d, want the 15-quest farm chain", farm.Type, len(farm.LifeQuests))
	}
	for i, quest := range farm.LifeQuests {
		if quest.ID != i+1 {
			t.Fatalf("chain position %d is quest %d", i, quest.ID)
		}
	}

	ctx := command.Context{State: stateio.NewMemory()}
	collection, err := roster.OpenCollectionStore(ctx, stateio.RootStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var tickets []assets.Item
	if farm.TicketID != 0 {
		tickets = append(tickets, assets.Item{InvenIndex: 1, Type: 19, ID: farm.TicketID, Count: 1})
	}
	inventory, err := assets.OpenInventory(ctx, stateio.RootStore{}, tickets)
	if err != nil {
		t.Fatal(err)
	}
	ledger := &lifeQuestLedger{}
	service := &Service{
		state:            progress.NewStore(),
		collection:       collection,
		inventory:        inventory,
		fieldPackEconomy: ledger,
		storyCatalog:     &gamedata.StoryCatalog{Packs: map[int]gamedata.StoryPack{}},
		fieldPacks:       map[int]gamedata.FieldPack{3011: farm},
		squadLevel:       func() (uint64, error) { return 999, nil },
	}
	service.setCurrentPack(3011)
	before := len(inventory.All(ctx))

	active := func() []int {
		t.Helper()
		var ids []int
		for _, row := range service.lifeQuestWires(3011) {
			id, _, _ := wire.Varint(row, 1)
			ids = append(ids, int(id))
		}
		return ids
	}
	value := func(quest int) uint64 {
		t.Helper()
		for _, row := range service.lifeQuestWires(3011) {
			if id, _, _ := wire.Varint(row, 1); int(id) == quest {
				v, _, _ := wire.Varint(row, 2)
				return v
			}
		}
		t.Fatalf("quest %d is not active", quest)
		return 0
	}
	call := func(path string, quest int, extra ...uint64) []byte {
		t.Helper()
		request := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, uint64(quest)), 3, 3011)
		for _, v := range extra {
			request = wire.AppendVarint(request, 4, v)
		}
		_, body, handled, err := service.Handle(ctx, path, request)
		if err != nil || !handled {
			t.Fatalf("%s quest %d: handled=%t err=%v", path, quest, handled, err)
		}
		return body
	}

	if err := service.ensureLifeQuest(ctx, 3011); err != nil {
		t.Fatal(err)
	}
	if got := active(); !slices.Equal(got, []int{1}) {
		t.Fatalf("entering the farm activated %v, want quest 1", got)
	}
	if err := service.ensureLifeQuest(ctx, 3011); err != nil || !slices.Equal(active(), []int{1}) {
		t.Fatalf("re-entering changed the active quest: %v (%v)", active(), err)
	}
	call("/QuestUpdate", 1, 1)
	if got := value(1); got != 1 {
		t.Fatalf("quest 1 progress=%d, want 1", got)
	}

	body := call("/QuestClear", 1)
	if len(ledger.rewards) != 1 || ledger.identities[0] != "pack3011:quest1:life" ||
		!slices.Equal(ledger.rewards[0], []gamedata.Reward{{Type: 64, Count: 50}}) {
		t.Fatalf("quest 1 life rewards: %v %v, want 50 life coins once", ledger.identities, ledger.rewards)
	}
	if got := active(); !slices.Equal(got, []int{2}) {
		t.Fatalf("after quest 1 the active quests are %v, want quest 2", got)
	}
	next, _, _ := wire.Bytes(body, 2)
	if id, _, _ := wire.Varint(next, 1); id != 2 {
		t.Fatalf("QuestClear named next quest %d, want 2", id)
	}
	bundle, _, _ := wire.Bytes(body, 1)
	shown := false
	_ = wire.Walk(bundle, func(f wire.Field) error {
		if f.Number == 6 {
			typ, _, _ := wire.Varint(f.Value, 3)
			count, _, _ := wire.Varint(f.Value, 4)
			shown = shown || typ == 64 && count == 50
		}
		return nil
	})
	if !shown {
		t.Fatalf("QuestClear reward bundle does not show the life coins: %x", bundle)
	}
	if got := len(inventory.All(ctx)); got != before {
		t.Fatalf("life rewards reached the ordinary inventory: %d items, want %d", got, before)
	}
}
