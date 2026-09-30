package player

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

func applyStepUpProgress(next *collectionSnapshot, groupID, step uint64) error {
	if groupID == 0 && step == 0 {
		return nil
	}
	if groupID == 0 || step == 0 {
		return errors.New("player: incomplete step-up purchase")
	}
	key := strconv.FormatUint(groupID, 10)
	completed := next.StepUpProgress[key]
	if completed >= step {
		return nil
	}
	if completed+1 != step {
		return fmt.Errorf("player: step-up group %d expects step %d, got %d", groupID, completed+1, step)
	}
	next.StepUpProgress[key] = step
	return nil
}

func gachaFixedKey(fixedID, fixedType uint64) string {
	return fmt.Sprintf("%d:%d", fixedID, fixedType)
}

func dailyGachaPrefix(day string, groupID, buyType uint64) string {
	return fmt.Sprintf("daily-gacha:%s:%d:%d:", day, groupID, buyType)
}

func applyGachaPurchase(next *collectionSnapshot, identity string, costumeIDs []uint64, purchase GachaPurchase, grant *CollectionGrant) error {
	if next.GachaApplied[identity] {
		return nil
	}
	groupKey := strconv.FormatUint(purchase.Group.ID, 10)
	user := next.GachaUsers[groupKey]
	user.GroupID = purchase.Group.ID
	if purchase.DailyLimit != 0 {
		if purchase.DailyKey == "" || (purchase.BuyType != 0 && purchase.BuyType != 2) {
			return errors.New("player: invalid daily gacha purchase")
		}
		prefix := dailyGachaPrefix(purchase.DailyKey, purchase.Group.ID, purchase.BuyType)
		var used uint64
		for key, applied := range next.GachaApplied {
			if applied && strings.HasPrefix(key, prefix) {
				used++
			}
		}
		if used >= purchase.DailyLimit {
			return fmt.Errorf("player: daily gacha group %d buy type %d exhausted", purchase.Group.ID, purchase.BuyType)
		}
		next.GachaApplied[prefix+identity] = true
	}
	point := purchase.Group.PointCount * uint64(len(costumeIDs))
	user.Point += point
	switch purchase.BuyType {
	case 1: // GB_NORMAL
		user.TotalBuyCount += uint64(len(costumeIDs))
	case 2: // GB_CASH outside the date-scoped daily discount
		if purchase.DailyLimit == 0 {
			if len(costumeIDs) == 1 {
				user.OneCashPickCount++
			} else {
				user.TenCashPickCount++
			}
		}
	case 3: // GB_CASH_CONTENT_TICKET
		user.TotalBuyCount += uint64(len(costumeIDs))
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
	return nil
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

// GachaUsersForDay returns cumulative gacha state with daily free/paid counts
// rebuilt from durable, date-scoped purchase markers. Historical counts never
// leak into a new reset day and no storage migration is required.
func (s *CollectionStore) GachaUsersForDay(day string) []GachaUserState {
	s.mu.Lock()
	defer s.mu.Unlock()
	byGroup := make(map[uint64]GachaUserState, len(s.data.GachaUsers))
	for _, value := range s.data.GachaUsers {
		value.OneFreePickCount = 0
		value.OneCashPickCount = 0
		value.TenFreePickCount = 0
		value.TenCashPickCount = 0
		byGroup[value.GroupID] = value
	}
	prefix := "daily-gacha:" + day + ":"
	for key, applied := range s.data.GachaApplied {
		if !applied || !strings.HasPrefix(key, prefix) {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(key, prefix), ":", 3)
		if len(parts) != 3 {
			continue
		}
		groupID, groupErr := strconv.ParseUint(parts[0], 10, 64)
		buyType, typeErr := strconv.ParseUint(parts[1], 10, 64)
		if groupErr != nil || typeErr != nil || groupID == 0 {
			continue
		}
		user := byGroup[groupID]
		user.GroupID = groupID
		switch buyType {
		case 0:
			user.OneFreePickCount++
		case 2:
			user.OneCashPickCount++
		}
		byGroup[groupID] = user
	}
	result := make([]GachaUserState, 0, len(byGroup))
	for _, value := range byGroup {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].GroupID < result[j].GroupID })
	return result
}

func (s *CollectionStore) GachaDailyCount(day string, groupID, buyType uint64) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := dailyGachaPrefix(day, groupID, buyType)
	var count uint64
	for key, applied := range s.data.GachaApplied {
		if applied && strings.HasPrefix(key, prefix) {
			count++
		}
	}
	return count
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
