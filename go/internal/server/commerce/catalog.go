// Package commerce exposes server-authoritative prices for SDK cash goods.
package commerce

import (
	"fmt"
	"math"
	"sort"

	"bd2server/internal/server/gameconfig"
	"bd2server/internal/server/gamedata"
)

type Product struct {
	SKU              string `json:"sku"`
	GroupID          uint64 `json:"group_id"`
	ProductID        uint64 `json:"product_id"`
	SaleGroup        uint64 `json:"sale_group"`
	ItemType         uint64 `json:"item_type"`
	Amount           uint64 `json:"amount"`
	Recharge         bool   `json:"recharge"`
	Enabled          bool   `json:"enabled"`
	Currency         string `json:"currency"`
	Cost             uint64 `json:"cost"`
	PaidDiamondPrice uint64 `json:"paid_diamond_price"`
}
type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	GameVersion   string    `json:"game_version"`
	Products      []Product `json:"products"`
}
type Catalog struct {
	version  string
	designs  map[gamedata.CashProductKey]gamedata.CashProductDesign
	quotes   map[gamedata.CashProductKey]Product
	manifest []Product
}

func NewCatalog(version string, design *gamedata.CashCatalog, cfg gameconfig.PurchasesConfig) (*Catalog, error) {
	validation := gameconfig.Default()
	validation.Purchases = cfg
	if err := validation.Validate(); err != nil {
		return nil, err
	}
	if version == "" || design == nil {
		return nil, fmt.Errorf("commerce: missing cash catalog/version")
	}
	c := &Catalog{version: version, designs: map[gamedata.CashProductKey]gamedata.CashProductDesign{}, quotes: map[gamedata.CashProductKey]Product{}}
	// Select the smallest regular recharge amount; its cash/diamond ratio is
	// the conservative conversion baseline. GameData provides both quantities.
	var baseAmount, basePrice uint64
	for _, p := range design.Products {
		if !p.Recharge || p.PriceType != 1 {
			continue
		}
		if p.NominalPaidDiamonds == 0 || p.PriceCount == 0 {
			return nil, fmt.Errorf("commerce: invalid recharge baseline")
		}
		if baseAmount == 0 || p.NominalPaidDiamonds < baseAmount || p.NominalPaidDiamonds == baseAmount && p.PriceCount > basePrice {
			baseAmount = p.NominalPaidDiamonds
			basePrice = p.PriceCount
		}
	}
	if baseAmount == 0 {
		return nil, fmt.Errorf("commerce: no regular diamond recharge conversion baseline")
	}
	for _, p := range design.Products {
		if _, exists := c.designs[p.Key]; exists {
			return nil, fmt.Errorf("commerce: duplicate product %+v", p.Key)
		}
		c.designs[p.Key] = p
		if p.PriceType != 1 {
			continue
		} // native wallet goods already use their own path
		price, err := ceilRatio(p.PriceCount, baseAmount, basePrice)
		if err != nil {
			return nil, err
		}
		q := Product{GroupID: p.Key.GroupID, ProductID: p.Key.ProductID, SaleGroup: p.Key.SaleGroup, Recharge: p.Recharge, Enabled: true, Currency: "paid_diamonds", ItemType: 2, Amount: price, Cost: price, PaidDiamondPrice: price}
		if p.Recharge {
			switch cfg.DiamondRecharge.Currency {
			case "free":
				q.Currency = "free"
				q.ItemType = 0
				q.Amount = 0
			case "gold":
				q.Currency = "gold"
				q.ItemType = 4
				q.Amount, err = boundedProduct(p.NominalPaidDiamonds, uint64(cfg.DiamondRecharge.GoldPerPaidDiamond))
			case "diamonds":
				q.Currency = "diamonds"
				q.ItemType = 3
				q.Amount, err = boundedProduct(p.NominalPaidDiamonds, uint64(cfg.DiamondRecharge.DiamondsPerPaidDiamond))
			case "ban", "":
				q.Currency = "ban"
				q.Enabled = false
				q.ItemType = 0
				q.Amount = 0
			}
			if err != nil {
				return nil, fmt.Errorf("commerce: recharge %+v: %w", p.Key, err)
			}
			q.Cost = q.Amount
		}
		aliases := []string{p.GoogleSKU}
		if p.AppleSKU != p.GoogleSKU {
			aliases = append(aliases, p.AppleSKU)
		}
		for _, sku := range aliases {
			if sku == "" {
				continue
			}
			q.SKU = sku
			c.manifest = append(c.manifest, q)
		}
		q.SKU = p.GoogleSKU
		if q.SKU == "" {
			q.SKU = p.AppleSKU
		}
		if q.SKU == "" {
			return nil, fmt.Errorf("commerce: cash product has no SKU")
		}
		c.quotes[p.Key] = q
	}
	sort.Slice(c.manifest, func(i, j int) bool {
		a, b := c.manifest[i], c.manifest[j]
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		if a.ProductID != b.ProductID {
			return a.ProductID < b.ProductID
		}
		if a.SaleGroup != b.SaleGroup {
			return a.SaleGroup < b.SaleGroup
		}
		return a.SKU < b.SKU
	})
	return c, nil
}
func ceilRatio(value, numerator, denominator uint64) (uint64, error) {
	if value == 0 || numerator == 0 || denominator == 0 || value > math.MaxUint64/numerator {
		return 0, fmt.Errorf("commerce: invalid or overflowing monetary conversion")
	}
	n := value * numerator
	result := n / denominator
	if n%denominator != 0 {
		result++
	}
	if result == 0 || result > math.MaxInt32 {
		return 0, fmt.Errorf("commerce: price outside supported wallet range")
	}
	return result, nil
}
func boundedProduct(a, b uint64) (uint64, error) {
	if a == 0 || b == 0 || a > math.MaxInt32/b {
		return 0, fmt.Errorf("commerce: purchase cost exceeds supported wallet range")
	}
	return a * b, nil
}
func (c *Catalog) Manifest() Manifest {
	return Manifest{SchemaVersion: 1, GameVersion: c.version, Products: append([]Product{}, c.manifest...)}
}
func (c *Catalog) Design(key gamedata.CashProductKey) (gamedata.CashProductDesign, bool) {
	p, ok := c.designs[key]
	return p, ok
}
func (c *Catalog) Designs() []gamedata.CashProductDesign {
	out := make([]gamedata.CashProductDesign, 0, len(c.designs))
	for _, p := range c.designs {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Key, out[j].Key
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		if a.ProductID != b.ProductID {
			return a.ProductID < b.ProductID
		}
		return a.SaleGroup < b.SaleGroup
	})
	return out
}
func (c *Catalog) Quote(key gamedata.CashProductKey, count uint64) (Product, error) {
	p, ok := c.quotes[key]
	if !ok {
		return Product{}, fmt.Errorf("commerce: unknown SDK cash product %+v", key)
	}
	if !p.Enabled {
		return Product{}, fmt.Errorf("commerce: paid-diamond recharge is disabled")
	}
	if count == 0 || count > math.MaxInt32 {
		return Product{}, fmt.Errorf("commerce: invalid purchase count")
	}
	if p.Amount != 0 {
		cost, err := boundedProduct(p.Amount, count)
		if err != nil {
			return Product{}, err
		}
		p.Cost = cost
	}
	return p, nil
}
