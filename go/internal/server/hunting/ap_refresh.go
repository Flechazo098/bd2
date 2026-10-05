package hunting

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
	"encoding/json"
	"fmt"
	"time"
)

type apClock struct {
	Version string `json:"version"`
	Next    int64  `json:"next"`
}

func (s *Service) AttachAPRefresh(design gamedata.HuntingAPDesign) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if design.Max == 0 || design.Max > 2147483647 || design.ResetSeconds < 0 || design.ResetSeconds >= 86400 {
		return fmt.Errorf("hunting: invalid AP refresh design")
	}
	s.apDesign = &design
	s.now = time.Now
	return nil
}

// The client clock is UTC+9. DailyResetTime is expressed in that clock.
func (s *Service) refreshAP() error {
	if s.apDesign == nil {
		return nil
	}
	now := s.now().UnixMilli()
	offset := (s.apDesign.ResetSeconds - 9*3600) * 1000
	day := (now - offset) / 86400000
	nextTime := (day+1)*86400000 + offset
	b, e := s.storage.Load("huntingapclock")
	if e != nil {
		return e
	}
	clock := apClock{versionconfig.State(), nextTime}
	if b != nil {
		if e = stateio.RequireExactJSONObject(b, "version", "next"); e != nil {
			return e
		}
		if e = json.Unmarshal(b, &clock); e != nil {
			return e
		}
		if clock.Version != versionconfig.State() || clock.Next <= 0 {
			return fmt.Errorf("hunting: invalid AP clock")
		}
		if now < clock.Next {
			return nil
		}
		next := s.clone()
		next.Free = s.apDesign.Max
		if e = s.persist(next); e != nil {
			return e
		}
		clock.Next = nextTime
	}
	raw, e := json.Marshal(clock)
	if e != nil {
		return e
	}
	return s.storage.Save("huntingapclock", raw)
}

func (s *Service) APChargeInfo() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.refreshAP(); e != nil {
		return nil, e
	}
	raw, e := s.storage.Load("huntingapclock")
	if e != nil || raw == nil {
		return nil, e
	}
	var clock apClock
	if e = json.Unmarshal(raw, &clock); e != nil {
		return nil, e
	}
	b := wire.AppendVarint(nil, 1, uint64(clock.Next-86400000))
	item := wire.AppendVarint(nil, 3, 21)
	item = wire.AppendVarint(item, 4, s.state.Free)
	b = wire.AppendBytes(b, 2, item)
	return wire.AppendBytes(nil, 1, b), nil
}
