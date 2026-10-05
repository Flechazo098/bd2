package player

import (
	"bd2server/internal/server/stateio"
	"testing"
)

func TestAutoRecoveryRequiresLivingCasterAndChargesActualTargets(t *testing.T) {
	s, chars, wallet, _ := talentTest(t, stateio.NewMemory(), 10)
	chars.characters = append(chars.characters, Character{InvenIndex: 88, ID: 350, HP: 100, Level: 1, TalentLevel: 1})
	if e := chars.SetCurrentHealth(77, 0); e != nil {
		t.Fatal(e)
	}
	r, e := s.AutoRecover(1, 77, []uint64{77})
	if e != nil || r.Caster != 88 || r.Disabled != 0 || len(r.Characters) != 1 || r.Characters[0].HP != 100 || r.Experience != 3 || r.Catalyst != 45 {
		t.Fatalf("result%+v err%v", r, e)
	}
	if wallet.CatalystBalance() != 45 {
		t.Fatal("wrong catalyst")
	}
	if e := chars.SetCurrentHealth(77, 0); e != nil {
		t.Fatal(e)
	}
	if e := chars.SetCurrentHealth(88, 0); e != nil {
		t.Fatal(e)
	}
	r, e = s.AutoRecover(2, 88, []uint64{77})
	if e != nil || r.Disabled != 1 || len(r.Characters) != 0 || wallet.CatalystBalance() != 45 {
		t.Fatalf("exhaustion%+v %v", r, e)
	}
	if e := chars.SetCurrentHealth(88, 100); e != nil {
		t.Fatal(e)
	}
	rule := s.design.Rules[[2]uint64{42, 1}]
	rule.Catalyst = 46
	s.design.Rules[[2]uint64{42, 1}] = rule
	r, e = s.AutoRecover(3, 88, []uint64{77})
	if e != nil || r.Disabled != 2 || len(r.Characters) != 0 || wallet.CatalystBalance() != 45 {
		t.Fatalf("insufficient catalyst%+v %v", r, e)
	}
}
