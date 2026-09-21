package player

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"bd2server/internal/gamedata"
)

func gachaFixedKey(fixedID, fixedType uint64) string {
	return fmt.Sprintf("%d:%d", fixedID, fixedType)
}

func applyGachaPurchase(next *collectionSnapshot, identity string, costumeIDs []uint64, purchase GachaPurchase, grant *CollectionGrant) {
	if next.GachaApplied[identity] {
		return
	}
	groupKey := strconv.FormatUint(purchase.Group.ID, 10)
	user := next.GachaUsers[groupKey]
	user.GroupID = purchase.Group.ID
	point := purchase.Group.PointCount * uint64(len(costumeIDs))
	user.Point += point
	switch purchase.BuyType {
	case 1: // GB_NORMAL
		user.TotalBuyCount += uint64(len(costumeIDs))
	case 2: // GB_CASH
		if len(costumeIDs) == 1 {
			user.OneCashPickCount++
		} else {
			user.TenCashPickCount++
		}
	}
	next.GachaUsers[groupKey] = user
	grant.GachaGroupID = purchase.Group.ID
	grant.GachaPoint = point
	grant.GachaFixed = append([]GachaFixedState(nil), purchase.Fixed...)
	grant.SelectionApplySortIDs = append([]uint64(nil), purchase.SelectionApplySortIDs...)
	for _, fixed := range purchase.Fixed {
		stored := fixed
		stored.ApplySort = -1
		next.GachaFixed[gachaFixedKey(fixed.FixedID, fixed.Type)] = stored
	}
	next.GachaApplied[identity] = true
}

func (s *CollectionStore) GachaUsers() []GachaUserState {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]GachaUserState, 0, len(s.data.GachaUsers))
	for _, value := range s.data.GachaUsers {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].GroupID < result[j].GroupID })
	return result
}

func (s *CollectionStore) GachaUser(groupID uint64) GachaUserState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.GachaUsers[strconv.FormatUint(groupID, 10)]
}

func (s *CollectionStore) GachaFixedStates() []GachaFixedState {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]GachaFixedState, 0, len(s.data.GachaFixed))
	for _, value := range s.data.GachaFixed {
		value.ApplySort = -1
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].FixedID != result[j].FixedID {
			return result[i].FixedID < result[j].FixedID
		}
		return result[i].Type < result[j].Type
	})
	return result
}

func (s *CollectionStore) GachaFixedCount(fixedID, fixedType uint64) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.GachaFixed[gachaFixedKey(fixedID, fixedType)].Count
}

// ExchangeGachaPoint performs the authoritative point debit. The exchange is
// saved before the wallet credit; a retry can therefore safely recover a
// wallet write using the same identity without spending the points twice.
func (s *CollectionStore) ExchangeGachaPoint(identity string, groupID, count uint64) (GachaPointExchange, error) {
	if identity == "" || groupID == 0 || count == 0 {
		return GachaPointExchange{}, errors.New("player: invalid gacha point exchange")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if exchange, ok := s.data.GachaPointExchange[identity]; ok {
		if exchange.GroupID != groupID || exchange.Count != count {
			return GachaPointExchange{}, errors.New("player: gacha point exchange identity conflict")
		}
		return exchange, nil
	}
	key := strconv.FormatUint(groupID, 10)
	user, ok := s.data.GachaUsers[key]
	if !ok || user.Point < count {
		return GachaPointExchange{}, errors.New("player: insufficient gacha point")
	}
	next := cloneCollection(s.data)
	user.Point -= count
	if math.MaxUint64-user.ExchangeMileageCount < count {
		return GachaPointExchange{}, errors.New("player: gacha point exchange count overflow")
	}
	user.ExchangeMileageCount += count
	next.GachaUsers[key] = user
	exchange := GachaPointExchange{GroupID: groupID, Count: count}
	next.GachaPointExchange[identity] = exchange
	if err := s.commit(next); err != nil {
		return GachaPointExchange{}, err
	}
	return exchange, nil
}

type legacyGachaDraw struct {
	identity string
	session  string
	seq      uint64
	gachaID  uint64
	grant    CollectionGrant
}

// RepairGachaProgress is a one-time migration for grants written before the
// server persisted GachaUserDBInfo and GachaFixedDBInfo. Existing results are
// replayed in request order to recover points and resettable rarity counters.
func (s *CollectionStore) RepairGachaProgress(catalog *gamedata.RegularGachaCatalog) error {
	if catalog == nil {
		return errors.New("player: nil gacha catalog")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data.GachaApplied) != 0 || len(s.data.GachaUsers) != 0 || len(s.data.GachaFixed) != 0 {
		if s.data.GachaCountCorrected {
			return nil
		}
		next := cloneCollection(s.data)
		counts := map[uint64]uint64{}
		for identity, grant := range next.Grants {
			parts := strings.Split(identity, ":")
			if len(parts) < 4 || parts[0] != "regular-gacha" {
				continue
			}
			gachaID, err := strconv.ParseUint(parts[1], 10, 64)
			if err != nil {
				continue
			}
			group, ok := catalog.GroupForGacha(gachaID)
			if ok && next.GachaApplied[identity] {
				counts[group.ID] += uint64(len(grant.ViewCostumeIDs))
			}
		}
		for groupID, count := range counts {
			key := strconv.FormatUint(groupID, 10)
			user := next.GachaUsers[key]
			user.TotalBuyCount = count
			next.GachaUsers[key] = user
		}
		next.GachaCountCorrected = true
		return s.commit(next)
	}
	var draws []legacyGachaDraw
	for identity, grant := range s.data.Grants {
		parts := strings.Split(identity, ":")
		if len(parts) < 4 || parts[0] != "regular-gacha" {
			continue
		}
		gachaID, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			continue
		}
		if _, ok := catalog.GroupForGacha(gachaID); !ok {
			continue
		}
		seq, err := strconv.ParseUint(parts[len(parts)-1], 10, 64)
		if err != nil {
			continue
		}
		session := ""
		if len(parts) >= 6 && parts[2] == "session" && parts[4] == "seq" {
			session = parts[3]
		}
		draws = append(draws, legacyGachaDraw{identity: identity, session: session, seq: seq, gachaID: gachaID, grant: grant})
	}
	if len(draws) == 0 {
		return nil
	}
	sort.SliceStable(draws, func(i, j int) bool {
		if draws[i].session != draws[j].session {
			if draws[i].session == "" {
				return true
			}
			if draws[j].session == "" {
				return false
			}
			return draws[i].session < draws[j].session
		}
		return draws[i].seq < draws[j].seq
	})
	next := cloneCollection(s.data)
	for _, draw := range draws {
		group, _ := catalog.GroupForGacha(draw.gachaID)
		key := strconv.FormatUint(group.ID, 10)
		user := next.GachaUsers[key]
		user.GroupID = group.ID
		user.TotalBuyCount += uint64(len(draw.grant.ViewCostumeIDs))
		user.Point += group.PointCount * uint64(len(draw.grant.ViewCostumeIDs))
		next.GachaUsers[key] = user
		if group.FixedID != 0 {
			fourKey := gachaFixedKey(group.FixedID, 0)
			fiveKey := gachaFixedKey(group.FixedID, 1)
			four := next.GachaFixed[fourKey]
			five := next.GachaFixed[fiveKey]
			four.FixedID, four.Type, four.ApplySort = group.FixedID, 0, -1
			five.FixedID, five.Type, five.ApplySort = group.FixedID, 1, -1
			for _, costumeID := range draw.grant.ViewCostumeIDs {
				grade, ok := catalog.CostumeGrade(costumeID)
				if !ok {
					return fmt.Errorf("player: gacha costume %d has no grade", costumeID)
				}
				switch grade {
				case 5:
					four.Count, five.Count = 0, 0
				case 4:
					four.Count = 0
					five.Count++
				default:
					four.Count++
					five.Count++
				}
			}
			next.GachaFixed[fourKey] = four
			next.GachaFixed[fiveKey] = five
		}
		next.GachaApplied[draw.identity] = true
	}
	next.GachaCountCorrected = true
	return s.commit(next)
}
