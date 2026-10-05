package eventactions

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"errors"
)

func (s *Service) cafeteriaReward(req []byte, identity string) ([]byte, error) {
	group, id := val(req, 2), val(req, 3)
	row, ok := s.row("CafeteriaEventTable", 5, 6, group, id)
	if !ok || row.V(13) == 0 || row.V(11) == 0 {
		return nil, errors.New("eventactions: cafeteria interaction absent")
	}
	var uid uint64
	for _, v := range s.registry.List() {
		if v.Type == 25 && s.active(v) && (v.SubID == group || v.SubID == 0 && v.ID == group) {
			uid = v.UID
			break
		}
	}
	if uid == 0 {
		return nil, errors.New("eventactions: cafeteria event inactive")
	}
	if len(s.design.Tables["CafeteriaDefaultTable"]) != 1 {
		return nil, errors.New("eventactions: cafeteria default missing")
	}
	defaults := s.design.Tables["CafeteriaDefaultTable"][0]
	cap := defaults.V(8)
	if cap == 0 || s.state.CafeteriaCurrency >= cap {
		return nil, errors.New("eventactions: cafeteria daily currency limit")
	}
	term := defaults.V(3)
	if row.V(4) != 1 {
		term = defaults.V(28)
	}
	receiptKey := key(uid, group, id)
	now := s.now().UnixMilli()
	if last := s.state.CafeteriaLast[receiptKey]; last > 0 && now-last < int64(term)*1000 {
		return nil, errors.New("eventactions: cafeteria interaction cooldown")
	}
	count := row.V(11)
	if count > cap-s.state.CafeteriaCurrency {
		count = cap - s.state.CafeteriaCurrency
	}
	rewards := []gamedata.Reward{{Type: row.V(13), ID: row.V(12), Count: count}}
	bundle, err := s.economy.Apply(identity, nil, rewards)
	if err != nil {
		return nil, err
	}
	s.state.CafeteriaCurrency += count
	s.state.CafeteriaLast[receiptKey] = now
	out := wire.AppendBytes(nil, 1, bundle)
	out = wire.AppendVarint(out, 2, s.state.CafeteriaCurrency)
	return out, nil
}
