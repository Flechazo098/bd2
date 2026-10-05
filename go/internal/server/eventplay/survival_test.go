package eventplay

import (
	"bd2server/internal/server/wire"
	"testing"
)

func row(fields map[int]uint64) []byte {
	var b []byte
	for n, v := range fields {
		b = wire.AppendVarint(b, n, v)
	}
	return b
}
func TestSurvivalProgressDerivesLootAndRejectsUnspawnedKills(t *testing.T) {
	s, _ := makeService(t)
	s.design.Tables["FieldMiniGameCharTable"] = [][]byte{row(map[int]uint64{13: 1, 2: 1})}
	s.design.Tables["FieldMiniGameMapTable"] = [][]byte{row(map[int]uint64{2: 1, 3: 1, 4: 1})}
	s.design.Tables["FieldMiniGameMonsterTable"] = [][]byte{row(map[int]uint64{2: 1, 5: 1, 12: 1, 6: 1})}
	box := row(map[int]uint64{2: 1})
	box = wire.AppendVarint(box, 3, 3)
	s.design.Tables["FieldMiniGameSurvivalBoxTable"] = [][]byte{box}
	s.design.Tables["FieldMiniGameSurvivalItemTable"] = [][]byte{row(map[int]uint64{1: 3, 3: 3, 4: 10})}
	s.design.Tables["FieldMiniGameCharLevelTable"] = [][]byte{row(map[int]uint64{1: 1, 2: 1, 3: 10}), row(map[int]uint64{1: 1, 2: 2, 3: 20})}
	a := Run{Char: 1, MapGroup: 1, Stage: 1, HP: 10, MaxHP: 10, Started: 1, Killed: map[uint64]uint64{}, Available: map[uint64]uint64{}, Picked: map[uint64]uint64{}, SkillLevels: map[uint64]uint64{}, Level: 1}
	req := wire.AppendVarint(nil, 2, 10)
	kill := row(map[int]uint64{1: 1, 2: 1})
	req = wire.AppendBytes(req, 6, kill)
	item := row(map[int]uint64{1: 3, 2: 1})
	req = wire.AppendBytes(req, 5, item)
	req = wire.AppendVarint(req, 3, 999)
	exp, coin, e := s.survivalProgress(req, &a)
	if e != nil || exp != 10 || coin != 0 || a.SkillCredits != 1 {
		t.Fatalf("exp%d coin%d credit%d err%v", exp, coin, a.SkillCredits, e)
	}
	if _, _, e = s.survivalProgress(req, &a); e == nil {
		t.Fatal("kill exceeded spawn accepted")
	}
}
func TestSurvivalSkillsRequireEarnedOfferAndRerolls(t *testing.T) {
	s, _ := makeService(t)
	s.design.Tables["FieldMiniGameSkillGroupTable"] = [][]byte{row(map[int]uint64{1: 1, 2: 3}), row(map[int]uint64{1: 2, 2: 3})}
	a := Run{SkillCredits: 1, Rerolls: 1, SkillLevels: map[uint64]uint64{}}
	if _, e := s.survivalOffers(&a); e != nil {
		t.Fatal(e)
	}
	if len(a.Offers) != 2 {
		t.Fatal("offer missing")
	}
	if _, e := s.survivalOffers(&a); e != nil || a.Rerolls != 0 {
		t.Fatal("reroll not consumed")
	}
	if _, e := s.survivalOffers(&a); e == nil {
		t.Fatal("infinite reroll")
	}
	a.SkillCredits = 0
	if _, e := s.survivalOffers(&a); e == nil {
		t.Fatal("unearned skill selection")
	}
}
