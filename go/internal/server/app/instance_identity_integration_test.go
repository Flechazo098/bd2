//go:build integration

package app

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
)

// CommonPacket.GetCharInfo(long) consults supporter caches before owned characters.
// CharDBInfo/CostumeDBInfo/EquipDBInfo link by instance index, not design ID.
func TestTwoAccountsKeepDistinctOwnedIdentitiesWhenBorrowingSameCharacter(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	type supporter struct{ character, costume, equipment uint64 }
	var supporters [2]supporter
	var snapshots [2]map[uint64]string
	owners := map[string]*playerInstance{}
	t.Cleanup(func() {
		for _, owner := range owners {
			closeNicknamePlayer(t, owner)
		}
	})
	closeOwner := func(account string) {
		closeNicknamePlayer(t, owners[account])
		delete(owners, account)
	}
	weaponIDs := make([]uint64, 0)
	for id, slot := range factory.design.equipmentSlots {
		if slot == 1 {
			weaponIDs = append(weaponIDs, id)
		}
	}
	slices.Sort(weaponIDs)
	if len(weaponIDs) == 0 {
		t.Fatal("selected GameData has no weapon")
	}
	// Current GameData CostumeTable 101 belongs to UniqueCharId 1; the selected
	// starter seed owns neither this costume nor its permanent character.
	const costumeID = 101
	design, found := factory.design.rewardCostumes.Character(costumeID)
	if !found {
		t.Fatal("selected GameData has no grantable costume", costumeID)
	}
	for i, account := range accounts {
		owner := openNicknamePlayer(t, factory, account)
		owners[account] = owner
		initial := inventoryIdentitySnapshot(t, owner)
		if i > 0 {
			assertInventoryIdentityDisjoint(t, snapshots[0], initial)
		}
		tx, err := owner.repository.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ctx := command.Context{State: tx, InstanceIDs: factory.instanceIDs()}
		grant, err := owner.assembly.collection.GrantCostumes(ctx, "same-character-award", []uint64{costumeID}, factory.design.rewardCostumes)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if len(grant.CharacterIndices) != 1 || len(grant.CostumeIndices) != 1 {
			_ = tx.Rollback()
			t.Fatal("first costume acquisition did not create its character and costume")
		}
		character, found := owner.assembly.collection.FindCharacter(grant.CharacterIndices[0])
		if !found || character.ID != design.ID || character.CostumeID != costumeID || character.UseCostume != grant.CostumeIndices[0] {
			_ = tx.Rollback()
			t.Fatal("acquired character does not reference its actual owned costume")
		}
		equipment, err := owner.assembly.ownedEquipment.GrantOnce(ctx, "same-weapon-award", weaponIDs[0])
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		request := append(append(appScalar(1, 1), appScalar(2, equipment.InvenIndex)...), appScalar(3, character.InvenIndex)...)
		if _, _, handled, err := owner.assembly.ownedEquipment.Handle(ctx, "/EquipUse", request); err != nil || !handled {
			_ = tx.Rollback()
			t.Fatalf("equip actual owned weapon: handled=%t error=%v", handled, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		supporters[i] = supporter{character.InvenIndex, character.UseCostume, equipment.InvenIndex}
		snapshots[i] = inventoryIdentitySnapshot(t, owner)
		for index, identity := range initial {
			if snapshots[i][index] != identity {
				t.Fatalf("grant changed an existing starter identity: %d", index)
			}
		}
		closeOwner(account)
	}
	assertInventoryIdentityDisjoint(t, snapshots[0], snapshots[1])

	// Recreate the allocator and application against the same SQLite account files.
	factory = &PlayerFactory{options: factory.options, seeds: factory.seeds, design: factory.design, profiles: factory.profiles}
	for i, account := range accounts {
		owner := openNicknamePlayer(t, factory, account)
		owners[account] = owner
		if !maps.Equal(snapshots[i], inventoryIdentitySnapshot(t, owner)) {
			t.Fatal("account reopen changed inventory identities or ownership links")
		}
		closeOwner(account)
	}
	shared, err := openSharedRuntime(context.Background(), factory.options, factory.design)
	if err != nil {
		t.Fatal(err)
	}
	factory.shared, factory.pvpSource = shared, shared.pvp
	registry := newPlayerRegistry(factory, time.Minute)
	t.Cleanup(func() {
		if err := registry.Close(context.Background()); err != nil {
			t.Error(err)
		}
		if err := shared.store.Close(); err != nil {
			t.Error(err)
		}
	})
	clients := []*friendHTTPClient{friendHTTPLogin(t, registry, accounts[0]), friendHTTPLogin(t, registry, accounts[1])}
	for i, client := range clients {
		fields := append(append(appScalar(2, 1), appScalar(3, supporters[i].character)...), appScalar(4, supporters[i].costume)...)
		client.send("/SupporterRegister", fields, 435, 0)
		drainFriendPublications(t, registry, shared, accounts[i])
	}
	borrower, lender := clients[0], clients[1]
	borrower.send("/FriendSend", appScalar(2, lender.owner), 205, 0)
	lender.send("/FriendAccept", appScalar(2, borrower.owner), 206, 0)
	borrower.send("/SupporterBattleInfo", appScalar(2, 1), 439, 0)
	borrowed := borrower.send("/SupporterBorrow", append(appScalar(2, lender.owner), appScalar(3, 1)...), 437, 0)
	bundles := appRows(borrowed, 3)
	if len(bundles) != 1 {
		t.Fatal("borrow omitted the native supporter bundle")
	}
	characters, costumes, equipment := appRows(bundles[0], 1), appRows(bundles[0], 2), appRows(bundles[0], 3)
	if len(characters) != 1 || appValue(characters[0], 1) != supporters[1].character || appValue(characters[0], 2) != design.ID || appValue(characters[0], 7) != supporters[1].costume {
		t.Fatal("borrowed character does not preserve lender ownership and costume references")
	}
	if len(costumes) != 1 || appValue(costumes[0], 1) != supporters[1].costume || appValue(costumes[0], 2) != costumeID || appValue(costumes[0], 4) != supporters[1].character {
		t.Fatal("borrowed costume does not point to lender's actual character")
	}
	if len(equipment) != 1 || appValue(equipment[0], 1) != supporters[1].equipment || appValue(equipment[0], 2) != supporters[1].character {
		t.Fatal("borrowed equipment does not point to lender's actual character")
	}
	for _, rows := range [][][]byte{characters, costumes, equipment} {
		for _, row := range rows {
			if _, own := snapshots[0][appValue(row, 1)]; own {
				t.Fatal("supporter cache would shadow borrower's owned inventory")
			}
		}
	}
}

func inventoryIdentitySnapshot(t *testing.T, owner *playerInstance) map[uint64]string {
	t.Helper()
	tx, err := owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := command.Context{State: tx}
	identities := map[uint64]string{}
	add := func(index uint64, identity string) {
		t.Helper()
		if index == 0 {
			t.Fatal("owned instance has no identity", identity)
		}
		if previous, exists := identities[index]; exists {
			t.Fatalf("instance %d resolves to both %s and %s", index, previous, identity)
		}
		identities[index] = identity
	}
	for _, character := range owner.assembly.worldService.CharacterService().RawAll() {
		if !roster.IsStoryCharacter(character) && !roster.IsCharmCharacter(character) {
			add(character.InvenIndex, fmt.Sprintf("character:%d:costume:%d", character.ID, character.UseCostume))
		}
	}
	for _, costume := range owner.assembly.collection.Costumes() {
		add(costume.InvenIndex, fmt.Sprintf("costume:%d:character:%d", costume.ID, costume.UseChar))
	}
	for _, item := range owner.assembly.ownedItems.All(ctx) {
		if item.InvenIndex != 0 {
			add(item.InvenIndex, fmt.Sprintf("item:%d:%d", item.Type, item.ID))
		}
	}
	for _, equipment := range owner.assembly.ownedEquipment.All(ctx) {
		add(equipment.InvenIndex, fmt.Sprintf("equipment:%d:character:%d", equipment.ID, equipment.UseChar))
	}
	if len(identities) == 0 {
		t.Fatal("new account has no inventory")
	}
	return identities
}

func assertInventoryIdentityDisjoint(t *testing.T, left, right map[uint64]string) {
	t.Helper()
	for id, leftIdentity := range left {
		if rightIdentity, exists := right[id]; exists {
			t.Fatalf("accounts share instance %d: %s and %s", id, leftIdentity, rightIdentity)
		}
	}
}
