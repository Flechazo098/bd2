package player

import (
	"bd2server/internal/server/stateio"
	"testing"
)

func TestCommerceTimedGrantRenewalAndRetry(t *testing.T) {
	inv, err := OpenInventory(stateio.NewMemory(), &Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := inv.GrantCommerceOnce("a", []Item{{Type: 19, ID: 38, Count: 1, ExpiryTime: 1000}})
	if err != nil || len(first) != 1 {
		t.Fatalf("%+v %v", first, err)
	}
	second, err := inv.GrantCommerceOnce("b", []Item{{Type: 19, ID: 38, Count: 2, ExpiryTime: 2000}})
	if err != nil || len(second) != 1 || second[0].InvenIndex != first[0].InvenIndex || inv.ContentTicketExpiry(38) != 2000 {
		t.Fatalf("renewal %+v %v", second, err)
	}
	retry, err := inv.GrantCommerceOnce("b", nil)
	if err != nil || len(retry) != 1 || len(inv.owned.Items) != 1 {
		t.Fatalf("retry %+v %v", retry, err)
	}
	if _, err = inv.GrantCommerceOnce("bad", []Item{{Type: 19, ID: 38, Count: 1}}); err == nil {
		t.Fatal("untimed ticket accepted")
	}
}
