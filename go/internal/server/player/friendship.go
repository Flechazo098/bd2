package player

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

// FriendshipService applies inventory gifts and counseling inside the account
// request transaction. Static IDs, thresholds and rewards come from GameData.
type FriendshipService struct {
	mu         sync.Mutex
	design     *gamedata.FriendshipDesign
	awake      *gamedata.CharAwakeDesign
	potential  *gamedata.CostumePotentialDesign
	collection *CollectionStore
	inventory  *Inventory
	wallet     *Wallet
	sessionID  string
	now        func() time.Time
}

func NewFriendshipService(design *gamedata.FriendshipDesign, awake *gamedata.CharAwakeDesign, potential *gamedata.CostumePotentialDesign, collection *CollectionStore, inventory *Inventory, wallet *Wallet) (*FriendshipService, error) {
	if design == nil || awake == nil || potential == nil || collection == nil || inventory == nil || wallet == nil {
		return nil, errors.New("player: incomplete friendship service")
	}
	s := &FriendshipService{design: design, awake: awake, potential: potential, collection: collection, inventory: inventory, wallet: wallet, now: time.Now}
	for _, entry := range collection.FriendshipEntries() {
		if entry.Daily != nil && entry.Daily.Used > design.Default.MaxCounselingAP {
			return nil, errors.New("player: saved daily friendship AP exceeds maximum")
		}
		if entry.State == nil {
			continue
		}
		state := entry.State
		if _, owned := s.owned(state.CostumeID); !owned {
			return nil, errors.New("player: saved friendship costume is not owned")
		}
		level, ok := design.Levels[gamedata.FriendshipKey{GroupID: state.CostumeID, ID: state.Level}]
		if !ok || (level.NextEXP != 0 && state.EXP >= level.NextEXP) || (state.Level >= design.Default.MaxLevels[2] && state.EXP != 0) {
			return nil, errors.New("player: saved friendship level is not in GameData")
		}
		if state.CounselingCount > design.Default.MaxCounselingAPByCostume && state.Level < design.Default.MaxLevels[2] {
			return nil, errors.New("player: invalid saved costume counseling count")
		}
		for _, id := range state.Sessions {
			if _, ok := design.Sessions[gamedata.FriendshipKey{GroupID: state.CostumeID, ID: id}]; !ok {
				return nil, errors.New("player: saved counseling session is not in GameData")
			}
		}
	}
	return s, nil
}

func (s *FriendshipService) BeginSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionID = id
}

func (s *FriendshipService) FriendshipAP() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	daily := s.daily(s.collection.FriendshipEntries(), s.now().UTC().Format("2006-01-02"))
	if daily.Used >= s.design.Default.MaxCounselingAP {
		return 0, nil
	}
	return s.design.Default.MaxCounselingAP - daily.Used, nil
}

func (s *FriendshipService) daily(entries map[string]FriendshipEntry, day string) FriendshipDaily {
	if entry := entries["daily"]; entry.Daily != nil && entry.Daily.Day == day {
		return *entry.Daily
	}
	return FriendshipDaily{Day: day}
}

func (s *FriendshipService) owned(id uint64) (Costume, bool) {
	costumeID, ok := s.design.Costumes[id]
	if !ok {
		return Costume{}, false
	}
	return s.collection.CostumeByID(costumeID)
}

func (s *FriendshipService) maximum(costume Costume) uint64 {
	max := s.design.Default.MaxLevels[0]
	unique := s.potential.CostumeUnique[costume.ID]
	awake, exists := s.collection.CharAwakeState(unique)
	rule, hasRule := s.awake.Characters[unique]
	if !exists || !hasRule || !awake.IsAwake {
		return max
	}
	for i, level := range awake.ImprintLevels {
		if len(rule.ImprintGrowth[i]) == 0 || level != uint64(len(rule.ImprintGrowth[i])) {
			return max
		}
	}
	max = s.design.Default.MaxLevels[1]
	nodes := s.potential.Nodes[costume.ID]
	if len(nodes) == 0 || len(costume.PotentialIDs) != len(nodes) {
		return max
	}
	for _, id := range costume.PotentialIDs {
		if _, ok := nodes[id]; !ok {
			return max
		}
	}
	return s.design.Default.MaxLevels[2]
}

func friendshipWire(state FriendshipState) []byte {
	b := wire.AppendVarint(nil, 1, state.CostumeID)
	b = wire.AppendVarint(b, 2, state.Level)
	if state.EXP != 0 {
		b = wire.AppendVarint(b, 3, state.EXP)
	}
	if state.LastCounselingDate != 0 {
		b = wire.AppendVarint(b, 4, state.LastCounselingDate)
	}
	return b
}

func (s *FriendshipService) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/FriendshipInfo" && path != "/FriendshipGift" && path != "/FriendshipCounseling" {
		return 0, nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return 0, nil, true, errors.New("player: friendship invalid sequence")
	}
	entries := s.collection.FriendshipEntries()
	if path == "/FriendshipInfo" {
		return 612, s.info(entries), true, nil
	}
	session := s.sessionID
	if session == "" {
		return 0, nil, true, errors.New("player: friendship mutation requires an authenticated session")
	}
	keyHash := sha256.Sum256([]byte(session + ":" + strconv.FormatUint(seq, 10)))
	key := "reply:" + hex.EncodeToString(keyHash[:])
	digestHash := sha256.Sum256(request)
	digest := hex.EncodeToString(digestHash[:])
	code := 614
	if path == "/FriendshipCounseling" {
		code = 613
	}
	if entry := entries[key]; entry.Reply != nil {
		if entry.Reply.Digest != digest || entry.Reply.Code != code {
			return 0, nil, true, errors.New("player: friendship sequence reused with different request")
		}
		return code, append([]byte(nil), entry.Reply.Body...), true, nil
	}
	id, found, err := wire.Varint(request, 2)
	if err != nil || !found || id == 0 || id > math.MaxInt32 {
		return 0, nil, true, errors.New("player: friendship invalid costume")
	}
	costume, owned := s.owned(id)
	if !owned {
		return 0, nil, true, fmt.Errorf("player: friendship costume %d is not owned", id)
	}
	state := FriendshipState{CostumeID: id, Level: 1}
	if entry := entries[friendshipStateKey(id)]; entry.State != nil {
		state = *entry.State
		state.Sessions = append([]uint64(nil), state.Sessions...)
	}
	max := s.maximum(costume)
	var body []byte
	if code == 614 {
		body, err = s.gift(request, state, max, key, digest)
	} else {
		body, err = s.counsel(request, state, max, entries, key, digest)
	}
	return code, body, true, err
}

func (s *FriendshipService) info(entries map[string]FriendshipEntry) []byte {
	ids := make([]uint64, 0, len(s.design.Costumes))
	for id := range s.design.Costumes {
		if _, ok := s.owned(id); ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	var body []byte
	for _, id := range ids {
		state := FriendshipState{CostumeID: id, Level: 1}
		if entry := entries[friendshipStateKey(id)]; entry.State != nil {
			state = *entry.State
		}
		body = wire.AppendBytes(body, 1, friendshipWire(state))
		if len(state.Sessions) != 0 {
			counsel := wire.AppendVarint(nil, 1, id)
			for _, session := range state.Sessions {
				counsel = wire.AppendVarint(counsel, 2, session)
			}
			body = wire.AppendBytes(body, 2, counsel)
		}
	}
	return body
}

func (s *FriendshipService) gift(request []byte, state FriendshipState, max uint64, key, digest string) ([]byte, error) {
	if state.Level >= max {
		return nil, errors.New("player: friendship level is at its unlocked maximum")
	}
	items, err := equipmentRequestItems(request, 3, "FriendshipGift")
	if err != nil {
		return nil, err
	}
	var exp uint64
	seen := map[uint64]bool{}
	for _, item := range items {
		if seen[item.InvenIndex] || item.InvenIndex > math.MaxInt64 || item.ID > math.MaxInt32 || item.Type > math.MaxInt32 || item.Count > math.MaxInt32 {
			return nil, errors.New("player: invalid friendship gift item")
		}
		seen[item.InvenIndex] = true
		rule, ok := s.design.Gifts[[2]uint64{item.Type, item.ID}]
		if !ok {
			return nil, errors.New("player: item is not a friendship gift")
		}
		unit := rule.Experience(state.CostumeID)
		if unit == 0 || item.Count > math.MaxInt32/unit || exp > math.MaxInt32-unit*item.Count {
			return nil, errors.New("player: invalid friendship gift experience")
		}
		exp += unit * item.Count
	}
	if err := s.inventory.CanConsume(items); err != nil {
		return nil, err
	}
	next, rewards, err := s.advance(state, max, exp)
	if err != nil {
		return nil, err
	}
	if err := s.inventory.Consume(items); err != nil {
		return nil, err
	}
	bundle, err := s.grant(key, rewards)
	if err != nil {
		return nil, err
	}
	body := wire.AppendBytes(nil, 1, bundle)
	body = wire.AppendBytes(body, 2, friendshipWire(next))
	body = wire.AppendVarint(body, 3, exp)
	if err := s.collection.ApplyFriendship(next, nil, key, FriendshipReply{Digest: digest, Code: 614, Body: body}); err != nil {
		return nil, err
	}
	return body, nil
}

func (s *FriendshipService) counsel(request []byte, state FriendshipState, max uint64, entries map[string]FriendshipEntry, key, digest string) ([]byte, error) {
	session, found, err := wire.Varint(request, 3)
	if err != nil || !found || session == 0 || session > math.MaxInt32 {
		return nil, errors.New("player: invalid counseling session")
	}
	rule, exists := s.design.Sessions[gamedata.FriendshipKey{GroupID: state.CostumeID, ID: session}]
	if !exists {
		return nil, errors.New("player: counseling session does not belong to costume")
	}
	choice, _, err := wire.Varint(request, 4)
	if err != nil || choice >= rule.ChoiceCount {
		return nil, errors.New("player: invalid counseling choice")
	}
	quick, _, err := wire.Varint(request, 5)
	if err != nil || quick > 1 {
		return nil, errors.New("player: invalid counseling quick flag")
	}
	if quick == 1 && (uint64(len(state.Sessions)) < s.design.Default.QuickCounselingUnlockCount || state.Level >= max || choice != 1) {
		return nil, errors.New("player: quick counseling is not available")
	}
	now := s.now()
	day := now.UTC().Format("2006-01-02")
	daily := s.daily(entries, day)
	free := state.Level >= s.design.Default.MaxLevels[2]
	if !free {
		if daily.Used >= s.design.Default.MaxCounselingAP {
			return nil, errors.New("player: no daily friendship AP remaining")
		}
		if state.CounselingDay == day && state.CounselingCount >= s.design.Default.MaxCounselingAPByCostume {
			return nil, errors.New("player: costume daily counseling limit reached")
		}
	}
	correct := quick == 1 || choice == s.design.Default.CorrectSelectDialogIndex
	exp := s.design.Default.IncorrectEXP
	if correct {
		exp = s.design.Default.CorrectEXP
	}
	if state.Level >= max {
		exp = 0
	}
	next, rewards, err := s.advance(state, max, exp)
	if err != nil {
		return nil, err
	}
	completed := slices.Contains(state.Sessions, session)
	if !free {
		rewards = append(rewards, s.design.Default.CounselingRewards...)
		daily.Used++
	} else if !completed && quick == 0 {
		// At the final cap the client allows unrestricted story playback. A
		// previously unseen story earns its default reward once; repeated
		// playback does not create a source of unlimited account currency.
		rewards = append(rewards, s.design.Default.CounselingRewards...)
	}
	if next.CounselingDay != day {
		next.CounselingDay = day
		next.CounselingCount = 0
	}
	next.CounselingCount++
	if now.UnixMilli() <= 0 {
		return nil, errors.New("player: invalid counseling time")
	}
	next.LastCounselingDate = uint64(now.UnixMilli())
	if quick == 0 {
		if !completed {
			next.Sessions = append(next.Sessions, session)
			slices.Sort(next.Sessions)
		}
	}
	bundle, err := s.grant(key, rewards)
	if err != nil {
		return nil, err
	}
	body := wire.AppendBytes(nil, 1, bundle)
	body = wire.AppendBytes(body, 2, friendshipWire(next))
	if correct {
		body = wire.AppendVarint(body, 3, 1)
	}
	if exp != 0 {
		body = wire.AppendVarint(body, 4, exp)
	}
	if err := s.collection.ApplyFriendship(next, &daily, key, FriendshipReply{Digest: digest, Code: 613, Body: body}); err != nil {
		return nil, err
	}
	return body, nil
}

func (s *FriendshipService) advance(state FriendshipState, max, exp uint64) (FriendshipState, []gamedata.Reward, error) {
	if state.EXP > math.MaxInt32-exp {
		return state, nil, errors.New("player: friendship experience overflow")
	}
	state.EXP += exp
	var rewards []gamedata.Reward
	for state.Level < max {
		rule, ok := s.design.Levels[gamedata.FriendshipKey{GroupID: state.CostumeID, ID: state.Level}]
		if !ok || rule.NextEXP == 0 {
			return state, nil, errors.New("player: missing friendship level threshold")
		}
		if state.EXP < rule.NextEXP {
			break
		}
		state.EXP -= rule.NextEXP
		state.Level++
		next, ok := s.design.Levels[gamedata.FriendshipKey{GroupID: state.CostumeID, ID: state.Level}]
		if !ok {
			return state, nil, errors.New("player: missing friendship level reward")
		}
		rewards = append(rewards, next.Rewards...)
	}
	if state.Level >= max {
		state.EXP = 0
	}
	return state, rewards, nil
}

func (s *FriendshipService) grant(identity string, rewards []gamedata.Reward) ([]byte, error) {
	if len(rewards) == 0 {
		return nil, nil
	}
	var items []gamedata.BattleReward
	for _, reward := range rewards {
		if reward.Type == 0 || reward.Count == 0 || reward.Count > math.MaxInt32 {
			return nil, errors.New("player: invalid friendship reward")
		}
		switch reward.Type {
		case 2, 3, 4, 12, 20:
		default:
			if reward.ID == 0 || reward.ID > math.MaxInt32 {
				return nil, errors.New("player: invalid friendship item reward")
			}
			items = append(items, gamedata.BattleReward{Type: reward.Type, ID: reward.ID, Count: reward.Count}) //nolint:staticcheck // S1016
		}
	}
	if _, err := s.wallet.GrantQuestOnce(identity+":currency", rewards); err != nil {
		return nil, err
	}
	granted, err := s.inventory.GrantOnce(identity+":items", items)
	if err != nil {
		return nil, err
	}
	var bundle []byte
	for _, item := range granted {
		bundle = wire.AppendBytes(bundle, 1, ItemWire(item))
	}
	for _, reward := range rewards {
		view := Item{ID: reward.ID, Type: reward.Type, Count: reward.Count}
		bundle = wire.AppendBytes(bundle, 6, ItemWire(view))
		switch reward.Type {
		case 2, 3, 4, 12, 20:
			bundle = wire.AppendBytes(bundle, 1, ItemWire(view))
		}
	}
	return bundle, nil
}
