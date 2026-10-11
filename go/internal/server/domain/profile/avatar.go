package profile

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

type AvatarUse struct {
	UseCharID uint64
	Parts     [9]uint64
	Date      uint64
}
type avatarState struct {
	Use         AvatarUse `json:"use"`
	Wishlist    []uint64  `json:"wishlist"`
	FirstChosen bool      `json:"first_chosen"`
}
type AvatarSource interface {
	AvatarItems(command.Context) []assets.Item
	AvatarCurrency(command.Context) assets.Currency
	AvatarUnlocked(command.Context, gamedata.AvatarProduct) (bool, error)
}
type AvatarEconomy interface {
	Apply(command.Context, string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}
type AvatarService struct {
	store   stateio.Store
	design  *gamedata.AvatarProfileDesign
	source  AvatarSource
	economy AvatarEconomy
}

func OpenAvatar(ctx command.Context, store stateio.Store, d *gamedata.AvatarProfileDesign, source AvatarSource, economy AvatarEconomy) (*AvatarService, error) {
	if store == nil || d == nil || source == nil || economy == nil {
		return nil, fmt.Errorf("avatar: missing design or player dependencies")
	}
	s := &AvatarService{store, d, source, economy}
	_, err := s.load(ctx)
	return s, err
}
func (s *AvatarService) load(ctx command.Context) (avatarState, error) {
	var state avatarState
	b, err := s.store.Load(ctx.State, "profile_avatar")
	if err != nil || b == nil {
		return state, err
	}
	if err = stateio.RequireExactJSONObject(b, "use", "wishlist", "first_chosen"); err != nil {
		return state, err
	}
	if err = json.Unmarshal(b, &state); err != nil {
		return state, err
	}
	for _, id := range state.Wishlist {
		if _, ok := s.design.Shop[id]; !ok {
			return state, fmt.Errorf("avatar: invalid saved wishlist product")
		}
	}
	return state, nil
}
func (s *AvatarService) save(ctx command.Context, state avatarState) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.store.Save(ctx.State, "profile_avatar", b)
}
func (s *AvatarService) CurrentUse(ctx command.Context) (AvatarUse, error) {
	st, err := s.load(ctx)
	return st.Use, err
}
func (s *AvatarService) FirstChosen(ctx command.Context) (bool, error) {
	st, err := s.load(ctx)
	return st.FirstChosen, err
}
func AvatarUseWire(use AvatarUse) []byte {
	out := wire.AppendVarint(nil, 1, use.UseCharID)
	for i, id := range use.Parts {
		if id != 0 {
			out = wire.AppendVarint(out, i+2, id)
		}
	}
	if use.Date != 0 {
		out = wire.AppendVarint(out, 11, use.Date)
	}
	return out
}
func (s *AvatarService) validateUse(use AvatarUse, owned map[[2]uint64]uint64) error {
	c, ok := s.design.Characters[use.UseCharID]
	if !ok || owned[[2]uint64{61, use.UseCharID}] == 0 {
		return fmt.Errorf("avatar: character is not owned")
	}
	for i, id := range use.Parts {
		if id == 0 {
			continue
		}
		p, ok := s.design.Parts[id]
		if !ok || p.Category != uint64(i+1) || owned[[2]uint64{49, id}] == 0 || p.Character != 0 && p.Character != use.UseCharID || slices.Contains(c.Banned, id) {
			return fmt.Errorf("avatar: invalid or unowned part in slot %d", i+1)
		}
		if c.Collaboration && i >= 6 && i <= 8 {
			return fmt.Errorf("avatar: collaboration character cannot use pet, mount or effects")
		}
		if c.Collaboration && p.Character != use.UseCharID {
			return fmt.Errorf("avatar: collaboration character requires its own parts")
		}
	}
	return nil
}
func (s *AvatarService) owned(ctx command.Context) (map[[2]uint64]uint64, error) {
	out := map[[2]uint64]uint64{}
	for _, item := range s.source.AvatarItems(ctx) {
		key := [2]uint64{item.Type, item.ID}
		if item.Type == 49 || item.Type == 50 || item.Type == 61 {
			if item.Count > math.MaxUint64-out[key] {
				return nil, fmt.Errorf("avatar: ownership count overflow")
			}
			out[key] += item.Count
		}
	}
	return out, nil
}

func (s *AvatarService) rewardMembers(r gamedata.Reward) ([]gamedata.Reward, error) {
	members, err := s.design.Bundles.Expand([]gamedata.BattleReward{{Type: r.Type, ID: r.ID, Count: r.Count}})
	if err != nil {
		return nil, err
	}
	var out []gamedata.Reward
	positions := map[[2]uint64]int{}
	for _, member := range members {
		positions[[2]uint64{member.Type, member.ID}] = len(out)
		out = append(out, gamedata.Reward(member))
	}
	for _, member := range members {
		if member.Type == 61 {
			c, ok := s.design.Characters[member.ID]
			if !ok {
				return nil, fmt.Errorf("avatar: unknown rewarded character")
			}
			for _, bonus := range c.Rewards {
				if bonus.Count > math.MaxInt32/member.Count {
					return nil, fmt.Errorf("avatar: reward quantity overflow")
				}
				bonus.Count *= member.Count
				key := [2]uint64{bonus.Type, bonus.ID}
				if index, exists := positions[key]; exists {
					out[index].Count = max(out[index].Count, bonus.Count)
				} else {
					positions[key] = len(out)
					out = append(out, bonus)
				}
			}
		}
	}
	return out, nil
}
func (s *AvatarService) maximum(typ, id uint64) uint64 {
	switch typ {
	case 49:
		if rule, ok := s.design.Parts[id]; ok {
			return max(1, rule.Maximum)
		}
	case 50:
		if limit, ok := s.design.MotionMaximum[id]; ok {
			return max(1, limit)
		}
	case 61:
		if rule, ok := s.design.Characters[id]; ok {
			return max(1, rule.Maximum)
		}
	}
	return 0
}
func (s *AvatarService) buy(ctx command.Context, st *avatarState, products []avatarPurchase) ([]byte, error) {
	owned, err := s.owned(ctx)
	if err != nil {
		return nil, err
	}
	var costs, rewards []gamedata.Reward
	var diamondCost uint64
	seen := map[uint64]bool{}
	for _, purchase := range products {
		if seen[purchase.ID] {
			return nil, fmt.Errorf("avatar: duplicate purchase product")
		}
		seen[purchase.ID] = true
		product, ok := s.design.Shop[purchase.ID]
		if purchase.Motion {
			product, ok = s.design.MotionShop[purchase.ID]
		}
		if !ok {
			return nil, fmt.Errorf("avatar: unknown purchase product")
		}
		if product.Reward.Count == 0 {
			return nil, fmt.Errorf("avatar: product does not define a purchase reward")
		}
		unlocked, err := s.source.AvatarUnlocked(ctx, product)
		if err != nil {
			return nil, err
		}
		if !unlocked {
			return nil, fmt.Errorf("avatar: purchase is locked")
		}
		members, err := s.rewardMembers(product.Reward)
		if err != nil {
			return nil, err
		}
		allOwned := true
		for _, r := range members {
			allOwned = allOwned && owned[[2]uint64{r.Type, r.ID}] != 0
		}
		if !product.Purchasable && !allOwned {
			return nil, fmt.Errorf("avatar: product is not purchasable")
		}
		if product.Visibility != 0 && !allOwned {
			return nil, fmt.Errorf("avatar: product is not visible")
		}
		price := product.Price
		if !purchase.Motion && purchase.Cost.Type == product.Premium.Type && purchase.Cost.ID == product.Premium.ID && product.Premium.Count > 0 {
			price = product.Premium
		}
		count := uint64(1)
		if !purchase.Motion {
			if price.Count == 0 {
				if purchase.Cost.Type != price.Type || purchase.Cost.ID != price.ID || purchase.Cost.Count != 0 {
					return nil, fmt.Errorf("avatar: invalid free product cost")
				}
			}
			if price.Count > 0 {
				if purchase.Cost.Type != price.Type || purchase.Cost.ID != price.ID || purchase.Cost.Count == 0 || purchase.Cost.Count%price.Count != 0 {
					return nil, fmt.Errorf("avatar: cost does not match product")
				}
				count = purchase.Cost.Count / price.Count
			}
		}
		if count > math.MaxInt32 || price.Count > math.MaxInt32/count {
			return nil, fmt.Errorf("avatar: purchase count overflow")
		}
		price.Count *= count
		if price.Count > 0 {
			if price.Type == 2 || price.Type == 3 {
				if price.ID != 0 || price.Count > math.MaxInt32-diamondCost {
					return nil, fmt.Errorf("avatar: combined diamond price overflow")
				}
				diamondCost += price.Count
			} else {
				costs = append(costs, price)
			}
		}
		for _, r := range members {
			if r.Count > math.MaxInt32/count {
				return nil, fmt.Errorf("avatar: reward count overflow")
			}
			r.Count *= count
			key := [2]uint64{r.Type, r.ID}
			limit := s.maximum(r.Type, r.ID)
			if limit == 0 || r.Count > limit || owned[key] > limit-r.Count {
				return nil, fmt.Errorf("avatar: product ownership limit reached")
			}
			owned[key] += r.Count
			rewards = append(rewards, r)
		}
		if product.Initial && product.Reward.Type == 61 {
			st.FirstChosen = true
		}
	}
	if diamondCost != 0 {
		currency := s.source.AvatarCurrency(ctx)
		free := min(currency.FreeJewelry, diamondCost)
		paid := diamondCost - free
		if currency.Jewelry < paid {
			return nil, fmt.Errorf("avatar: insufficient combined diamonds")
		}
		if free != 0 {
			costs = append(costs, gamedata.Reward{Type: 3, Count: free})
		}
		if paid != 0 {
			costs = append(costs, gamedata.Reward{Type: 2, Count: paid})
		}
	}
	identity := "avatar-buy:" + ctx.RequestID
	if ctx.RequestID == "" {
		return nil, fmt.Errorf("avatar: purchase request identity is missing")
	}
	bundle, err := s.economy.Apply(ctx, identity, costs, rewards)
	if err != nil {
		return nil, err
	}
	if err = s.save(ctx, *st); err != nil {
		return nil, err
	}
	return wire.AppendBytes(nil, 1, bundle), nil
}
func (s *AvatarService) applyUse(ctx command.Context, st *avatarState, use AvatarUse) error {
	owned, err := s.owned(ctx)
	if err != nil {
		return err
	}
	if err = s.validateUse(use, owned); err != nil {
		return err
	}
	use.Date = uint64(time.Now().UnixMilli())
	st.Use = use
	return s.save(ctx, *st)
}
