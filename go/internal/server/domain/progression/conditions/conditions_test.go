package conditions

import (
	"maps"
	"testing"
)

func TestMatches(t *testing.T) {
	tests := []struct {
		name  string
		spec  Spec
		event Event
		want  bool
	}{
		{"different type", Spec{Type: MonsterKill}, Event{Type: HuntingBattle}, false},
		{"exact sub", Spec{Type: EquipmentUpgrade, SubType: 2}, Event{Type: EquipmentUpgrade, Sub: 2}, true},
		{"exact sub differs", Spec{Type: EquipmentUpgrade, SubType: 2}, Event{Type: EquipmentUpgrade, Sub: 3}, false},
		{"more check accepts higher", Spec{Type: EquipmentUpgrade, SubType: 2, SubTypeMoreCheck: 1}, Event{Type: EquipmentUpgrade, Sub: 3}, true},
		{"more check accepts equal", Spec{Type: EquipmentUpgrade, SubType: 2, SubTypeMoreCheck: 1}, Event{Type: EquipmentUpgrade, Sub: 2}, true},
		{"more check rejects lower", Spec{Type: EquipmentUpgrade, SubType: 2, SubTypeMoreCheck: 1}, Event{Type: EquipmentUpgrade, Sub: 1}, false},
		{"zero sub accepts any", Spec{Type: MonsterKill}, Event{Type: MonsterKill, Sub: 7}, true},
		{"rule accepts higher", Spec{Type: EquipMakeGrade, SubType: 2}, Event{Type: EquipMakeGrade, Sub: 3}, true},
		{"rule accepts equal", Spec{Type: EquipMakeGrade, SubType: 2}, Event{Type: EquipMakeGrade, Sub: 2}, true},
		{"rule rejects lower", Spec{Type: EquipMakeGrade, SubType: 2}, Event{Type: EquipMakeGrade, Sub: 1}, false},
		{"rule zero sub accepts any", Spec{Type: EquipMakeGrade}, Event{Type: EquipMakeGrade, Sub: 4}, true},
		{"exact rule distinguishes zero", Spec{Type: FieldSpawnEventReward}, Event{Type: FieldSpawnEventReward, Sub: 0}, true},
		{"exact rule rejects other sub", Spec{Type: FieldSpawnEventReward}, Event{Type: FieldSpawnEventReward, Sub: 1}, false},
		{"no params accepts any", Spec{Type: BuyItemInShop}, Event{Type: BuyItemInShop, Param: 9}, true},
		{"zero params accept any", Spec{Type: BuyItemInShop, Params: []uint64{0}}, Event{Type: BuyItemInShop, Param: 9}, true},
		{"params membership", Spec{Type: BuyItemInShop, Params: []uint64{3, 7}}, Event{Type: BuyItemInShop, Param: 7}, true},
		{"params rejects others", Spec{Type: BuyItemInShop, Params: []uint64{3, 7}}, Event{Type: BuyItemInShop, Param: 5}, false},
		{"param threshold accepts higher", Spec{Type: MiniGameSurvivalLifeTime, Params: []uint64{300}}, Event{Type: MiniGameSurvivalLifeTime, Param: 600}, true},
		{"param threshold rejects lower", Spec{Type: MiniGameSurvivalLifeTime, Params: []uint64{300}}, Event{Type: MiniGameSurvivalLifeTime, Param: 299}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.spec.Matches(test.event); got != test.want {
				t.Fatalf("Matches(%+v, %+v) = %v, want %v", test.spec, test.event, got, test.want)
			}
		})
	}
}

func TestSubTypeReached(t *testing.T) {
	tests := []struct {
		name  string
		spec  Spec
		value uint64
		want  bool
	}{
		{"zero sub accepts any", Spec{Type: MonsterKill}, 9, true},
		{"exact", Spec{Type: EquipmentUpgrade, SubType: 2}, 2, true},
		{"exact rejects higher", Spec{Type: EquipmentUpgrade, SubType: 2}, 3, false},
		{"more check", Spec{Type: EquipmentUpgrade, SubType: 2, SubTypeMoreCheck: 1}, 3, true},
		{"more check equal", Spec{Type: EquipmentUpgrade, SubType: 2, SubTypeMoreCheck: 1}, 2, true},
		{"rule threshold", Spec{Type: MiniGameSurvivalKillCount, SubType: 1000}, 1500, true},
		{"rule threshold equal", Spec{Type: MiniGameSurvivalKillCount, SubType: 1000}, 1000, true},
		{"rule threshold rejects lower", Spec{Type: MiniGameSurvivalKillCount, SubType: 1000}, 999, false},
		{"exact rule zero", Spec{Type: FieldSpawnEventReward}, 0, true},
		{"exact rule rejects one", Spec{Type: FieldSpawnEventReward}, 1, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.spec.SubTypeReached(test.value); got != test.want {
				t.Fatalf("SubTypeReached(%+v, %d) = %v, want %v", test.spec, test.value, got, test.want)
			}
		})
	}
}

func TestParamAccepts(t *testing.T) {
	tests := []struct {
		name  string
		spec  Spec
		value uint64
		want  bool
	}{
		{"empty", Spec{Type: BuyItemInShop}, 5, true},
		{"all zero", Spec{Type: BuyItemInShop, Params: []uint64{0, 0}}, 5, true},
		{"membership", Spec{Type: BuyItemInShop, Params: []uint64{5, 6}}, 5, true},
		{"not member", Spec{Type: BuyItemInShop, Params: []uint64{5, 6}}, 7, false},
		{"threshold", Spec{Type: MiniGameSurvivalLifeTime, Params: []uint64{300}}, 300, true},
		{"threshold higher", Spec{Type: MiniGameSurvivalLifeTime, Params: []uint64{300}}, 600, true},
		{"threshold lower", Spec{Type: MiniGameSurvivalLifeTime, Params: []uint64{300}}, 299, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.spec.ParamAccepts(test.value); got != test.want {
				t.Fatalf("ParamAccepts(%+v, %d) = %v, want %v", test.spec, test.value, got, test.want)
			}
		})
	}
}

var expectedConditionRules = map[uint64]conditionRule{
	EquipMakeGrade:                {sub: compareAtLeast},
	TotalWarResultScore:           {sub: compareAtLeast},
	MiniGameSurvivalCharLevel:     {sub: compareAtLeast},
	MiniGameSurvivalSkillMaxCount: {sub: compareAtLeast},
	MiniGameSurvivalKillCount:     {sub: compareAtLeast},
	MiniGameDefenseWaveClear:      {sub: compareAtLeast},
	PackManChallengeTimeoutScore:  {sub: compareAtLeast},
	MiniGameSurvivalLifeTime:      {param: compareAtLeast},
	FieldSpawnEventReward:         {sub: compareExact},
}

// TestConditionRules pins the forced comparisons and checks each one, so a
// removed or mistyped table entry cannot hide behind the hand-picked cases.
func TestConditionRules(t *testing.T) {
	if !maps.Equal(conditionRules, expectedConditionRules) {
		t.Fatalf("condition rules = %v, want %v", conditionRules, expectedConditionRules)
	}
	for conditionType, rule := range conditionRules {
		switch rule.sub {
		case compareAtLeast:
			spec := Spec{Type: conditionType, SubType: 5}
			if !spec.SubTypeReached(5) || !spec.SubTypeReached(6) || spec.SubTypeReached(4) {
				t.Fatalf("condition %d at-least rule does not compare >= 5", conditionType)
			}
		case compareExact:
			spec := Spec{Type: conditionType, SubType: 0}
			if !spec.SubTypeReached(0) || spec.SubTypeReached(1) {
				t.Fatalf("condition %d exact rule does not keep zero a value", conditionType)
			}
		}
		if rule.param == compareAtLeast {
			spec := Spec{Type: conditionType, Params: []uint64{5}}
			if !spec.ParamAccepts(5) || !spec.ParamAccepts(6) || spec.ParamAccepts(4) {
				t.Fatalf("condition %d param rule does not compare >= 5", conditionType)
			}
		}
	}
}

func TestTarget(t *testing.T) {
	if got := Target(0); got != 1 {
		t.Fatalf("Target(0) = %d, want 1", got)
	}
	if got := Target(7); got != 7 {
		t.Fatalf("Target(7) = %d, want 7", got)
	}
}
