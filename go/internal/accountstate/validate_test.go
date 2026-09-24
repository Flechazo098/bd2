package accountstate

import (
	"slices"
	"testing"
)

func validValidationSnapshot() validationSnapshot {
	return validationSnapshot{
		formatVersion: 1, clientVersion: "2.35.10", gameDataVersion: "20260924000000",
		itemNextIndex: 11, equipmentNextIndex: 21, characterNextIndex: 31, costumeNextIndex: 41,
		itemIndices: []uint64{10}, equipmentIndices: []uint64{20}, characterIndices: []uint64{30}, costumeIndices: []uint64{40},
		equipmentGrants: []namedIndex{{identity: "quest:test", index: 20}},
		equipmentUsers:  []namedIndex{{identity: "20", index: 30}},
		quests:          []questKey{{pack: 1, quest: 2}}, cleared: []questKey{{pack: 1, quest: 2}},
		granted:    map[string]bool{"mail:test": true},
		itemGrants: []indexedGrant{{identity: "mail:test", indices: []uint64{9}}},
	}
}

func problemCodes(problems []Problem) []string {
	codes := make([]string, 0, len(problems))
	for _, problem := range problems {
		codes = append(codes, problem.Code)
	}
	return codes
}

func TestValidateSnapshotAcceptsConsumedGrantedItem(t *testing.T) {
	snapshot := validValidationSnapshot()
	// Index 9 is intentionally absent from itemIndices: grant_items is an
	// issuance ledger and remains valid after the stack is consumed.
	if problems := validateSnapshot(snapshot); len(problems) != 0 {
		t.Fatalf("unexpected problems: %#v", problems)
	}
}

func TestValidateSnapshotPortsEveryExternalValidatorRule(t *testing.T) {
	snapshot := validValidationSnapshot()
	snapshot.formatVersion = 2
	snapshot.clientVersion = ""
	snapshot.gameDataVersion = ""
	snapshot.itemIndices = []uint64{10, 10}
	snapshot.equipmentIndices = []uint64{20, 20}
	snapshot.characterIndices = []uint64{30, 30}
	snapshot.costumeIndices = []uint64{40, 40}
	snapshot.itemNextIndex = 10
	snapshot.equipmentNextIndex = 20
	snapshot.characterNextIndex = 30
	snapshot.costumeNextIndex = 40
	snapshot.equipmentGrants = []namedIndex{{identity: "missing", index: 999}}
	snapshot.equipmentUsers = []namedIndex{{identity: "20", index: 999}}
	snapshot.quests = []questKey{{}, {pack: 2, quest: 3}, {pack: 2, quest: 3}}
	snapshot.cleared = []questKey{{pack: 1}, {pack: 4, quest: 5}, {pack: 4, quest: 5}}
	snapshot.granted = map[string]bool{}
	snapshot.itemGrants = []indexedGrant{{identity: "unmarked", indices: []uint64{0, 10, 10}}}

	want := []string{
		"snapshot.format_version", "snapshot.client_version", "snapshot.game_data_version",
		"inventory.duplicate_index", "equipment.duplicate_index", "characters.duplicate_index", "costumes.duplicate_index",
		"inventory.next_index", "equipment.next_index", "collection.next_character_index", "collection.next_costume_index",
		"equipment.grant_missing_equipment", "equipment.unknown_user",
		"progress.invalid_quest_key", "progress.invalid_cleared_key",
		"progress.duplicate_quest_key", "progress.duplicate_cleared_key",
		"inventory.grant_without_marker", "inventory.grant_invalid_index", "inventory.grant_invalid_index", "inventory.grant_invalid_index",
		"inventory.grant_duplicate_index",
	}
	got := problemCodes(validateSnapshot(snapshot))
	if !slices.Equal(got, want) {
		t.Fatalf("problem codes:\n got %v\nwant %v", got, want)
	}
}

func TestValidateSnapshotGrantAndOwnershipRules(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*validationSnapshot)
		code string
	}{
		{name: "unissued item index", edit: func(s *validationSnapshot) { s.itemGrants[0].indices = []uint64{s.itemNextIndex} }, code: "inventory.grant_invalid_index"},
		{name: "missing equipment", edit: func(s *validationSnapshot) { s.equipmentGrants[0].index = 999 }, code: "equipment.grant_missing_equipment"},
		{name: "unknown equipped character", edit: func(s *validationSnapshot) { s.equipmentUsers[0].index = 999 }, code: "equipment.unknown_user"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := validValidationSnapshot()
			test.edit(&snapshot)
			if got := problemCodes(validateSnapshot(snapshot)); !slices.Contains(got, test.code) {
				t.Fatalf("codes %v do not contain %s", got, test.code)
			}
		})
	}
}
