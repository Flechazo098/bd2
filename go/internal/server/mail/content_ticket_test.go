package mail

import (
	"bd2server/internal/server/gamedata"
	"testing"
)

func TestContentTicketsUseCatalogAndRequireSingleUse(t *testing.T) {
	s := &Service{}
	if err := s.AttachContentTickets(&gamedata.GachaContentTicketDesign{IDs: map[uint64]bool{660003: true}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, count uint64
		want      bool
	}{{660003, 1, true}, {450030, 1, false}, {660003, 2, false}, {660003, 0, false}} {
		if got := s.supportedItemDBInfoReward(gamedata.Reward{Type: 19, ID: tc.id, Count: tc.count}); got != tc.want {
			t.Fatalf("ticket=%+v supported=%v", tc, got)
		}
	}
}
