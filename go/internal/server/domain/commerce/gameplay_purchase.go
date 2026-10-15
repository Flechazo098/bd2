package commerce

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
)

// ChargeGameplayPurchase applies a native product's price and purchase limit;
// the calling gameplay command delivers its effect in the same transaction.
func (s *Service) ChargeGameplayPurchase(ctx command.Context, identity string, key gamedata.CashProductKey) error {
	design, exists := s.catalog.Design(key)
	if !exists || identity == "" || design.PriceType <= 1 || design.PriceCount == 0 || !s.available(design) {
		return fmt.Errorf("commerce: gameplay purchase unavailable")
	}
	state, err := s.load(ctx)
	if err != nil {
		return err
	}
	raw := fmt.Sprintf("%d:%d:%d", key.GroupID, key.ProductID, key.SaleGroup)
	hash := sha256.Sum256([]byte(raw))
	digest := hex.EncodeToString(hash[:])
	if receipt, exists := state.Receipts[identity]; exists {
		if receipt.Digest != digest {
			return fmt.Errorf("commerce: gameplay purchase identity conflict")
		}
		return nil
	}
	count := s.count(state, design)
	if s.legacy != nil {
		for _, raw := range s.legacy.PurchaseCountDBInfos(ctx) {
			group, _, _ := wire.Varint(raw, 1)
			product, _, _ := wire.Varint(raw, 2)
			sale, _, _ := wire.Varint(raw, 3)
			n, _, _ := wire.Varint(raw, 4)
			if group == key.GroupID && product == key.ProductID && sale == key.SaleGroup {
				count = max(count, n)
			}
		}
	}
	if count >= math.MaxInt32 || design.PurchaseLimitType > 4 || (design.PurchaseLimitType != 0 && (design.PurchaseLimitCount == 0 || count >= design.PurchaseLimitCount)) || !s.predecessorBought(ctx, state, key) {
		return fmt.Errorf("commerce: gameplay purchase limit exceeded")
	}
	old := state.Counts[skuKey(key)]
	if old.Lifetime == math.MaxUint64 {
		return fmt.Errorf("commerce: gameplay purchase lifetime overflow")
	}
	costs := []gamedata.Reward{{Type: design.PriceType, ID: design.PriceID, Count: design.PriceCount}}
	if resolver, ok := s.economy.(nativeCostResolver); ok {
		costs, err = resolver.NativePurchaseCosts(ctx, costs[0])
		if err != nil {
			return err
		}
	}
	if _, err := s.economy.Apply(ctx, identity, costs, nil); err != nil {
		return err
	}
	state.Counts[skuKey(key)] = purchaseCount{Count: count + 1, Period: s.period(design.PurchaseLimitType), Lifetime: old.Lifetime + 1}
	state.Receipts[identity] = purchaseReceipt{Digest: digest, Response: wire.AppendBytes(nil, 1, nil)}
	if s.hook != nil {
		if err := s.hook(ctx, identity, design, 1); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.store.Save(ctx.State, "commerce", encoded)
}
