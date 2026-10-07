package commerce

import (
	"fmt"
	"math"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
)

// Native prices belong to GameData; cash quotes remain server policy.
func nativePrice(d gamedata.CashProductDesign, count uint64) (uint64, error) {
	if d.PriceType == 0 {
		if d.PriceID != 0 || d.PriceCount != 0 {
			return 0, fmt.Errorf("commerce: invalid free product price")
		}
		return 0, nil
	}
	if count == 0 || count > math.MaxInt32 || d.PriceCount > math.MaxInt32/count {
		return 0, fmt.Errorf("commerce: native price overflow")
	}
	return d.PriceCount * count, nil
}

type nativeCostResolver interface {
	NativePurchaseCosts(ctx command.Context, _ gamedata.Reward) ([]gamedata.Reward, error)
}

// AttachSpecialProducts reserves exact keys whose rewards depend on a prior
// gameplay preview. Ordinary tickets and native goods stay in the economy.
func (s *Service) AttachSpecialProducts(keys []gamedata.CashProductKey) error {
	products := map[gamedata.CashProductKey]bool{}
	for _, key := range keys {
		if _, ok := s.catalog.Design(key); !ok {
			return fmt.Errorf("commerce: unknown special product %+v", key)
		}
		products[key] = true
	}
	s.specialProducts = products
	return nil
}

func (e *EntitlementEconomy) NativePurchaseCosts(ctx command.Context, cost gamedata.Reward) ([]gamedata.Reward, error) {
	if resolver, ok := e.base.(nativeCostResolver); ok {
		return resolver.NativePurchaseCosts(ctx, cost)
	}
	return []gamedata.Reward{cost}, nil
}
