package hunting

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

type battleReceipt struct {
	Fingerprint string
	Bundle      []byte
	Monsters    [][]byte
}
type battleLedger struct {
	Version  string                   `json:"version"`
	Receipts map[string]battleReceipt `json:"receipts"`
}

func (s *Service) loadBattleReceipts() (map[string]battleReceipt, error) {
	b, e := s.storage.Load("huntingbattle")
	if e != nil {
		return nil, e
	}
	if b == nil {
		return map[string]battleReceipt{}, nil
	}
	if e = stateio.RequireExactJSONObject(b, "version", "receipts"); e != nil {
		return nil, e
	}
	var l battleLedger
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&l); e != nil {
		return nil, e
	}
	if l.Version != versionconfig.State() || l.Receipts == nil {
		return nil, fmt.Errorf("hunting: invalid battle ledger")
	}
	return l.Receipts, nil
}
func (s *Service) saveBattleReceipts(rs map[string]battleReceipt) error {
	b, e := json.Marshal(battleLedger{versionconfig.State(), rs})
	if e != nil {
		return e
	}
	return s.storage.Save("huntingbattle", b)
}
func (s *Service) AttachEligibility(check func(int, uint64) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eligibility = check
}
func (s *Service) AttachRewards(grant func(string, []gamedata.Reward) ([]byte, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grant = grant
}

// EnsureForPack initializes the ordinary stage when the world loads a pack.
func (s *Service) EnsureForPack(pack int) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, e := s.load(pack)
	if e != nil {
		return nil, e
	}
	if len(d.Grounds) == 0 {
		return nil, nil
	}
	st := s.state.Packs[strconv.Itoa(pack)]
	if st.Current == 0 {
		st.Current = d.Grounds[0].ID
		next := s.clone()
		next.Packs[strconv.Itoa(pack)] = st
		if e = s.persist(next); e != nil {
			return nil, e
		}
	}
	return s.info(pack, d)
}
func (s *Service) SnapshotForPack(pack int) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, e := s.load(pack)
	if e != nil {
		return nil, e
	}
	if len(d.Grounds) == 0 {
		return nil, nil
	}
	return s.info(pack, d)
}

// Ground mutations have their own persisted session+sequence replay ledger.
func (s *Service) handleGroundSession(path string, req []byte, session string) (int, []byte, bool, error) {
	if path != "/HuntingGroundEnter" {
		return s.Handle(path, req)
	}
	seq, found, e := wire.Varint(req, 1)
	if e != nil || !found || seq == 0 {
		return 110, nil, true, fmt.Errorf("hunting: missing sequence")
	}
	key := "enter:" + session + ":" + strconv.FormatUint(seq, 10)
	rs, e := s.loadBattleReceipts()
	if e != nil {
		return 110, nil, true, e
	}
	fp := fmt.Sprintf("%x", req)
	if r, ok := rs[key]; ok {
		if r.Fingerprint != fp {
			return 110, nil, true, fmt.Errorf("hunting: enter sequence conflict")
		}
		return 110, r.Bundle, true, nil
	}
	code, out, ok, e := s.Handle(path, req)
	if e != nil || !ok {
		return code, out, ok, e
	}
	rs[key] = battleReceipt{Fingerprint: fp, Bundle: out}
	if e = s.saveBattleReceipts(rs); e != nil {
		return code, nil, true, e
	}
	return code, out, ok, nil
}
func (s *Service) reload() error {
	b, e := s.storage.Load("hunting")
	if e != nil {
		return e
	}
	if b == nil {
		return nil
	}
	var st snapshot
	if e = json.Unmarshal(b, &st); e != nil {
		return e
	}
	s.state = st
	return nil
}
func (s *Service) CanExchangeAP(costs, rewards []gamedata.Reward) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.reload(); e != nil {
		return e
	}
	if e := s.refreshAP(); e != nil {
		return e
	}
	_, e := s.exchangeAP(costs, rewards)
	return e
}
func (s *Service) exchangeAP(costs, rewards []gamedata.Reward) (snapshot, error) {
	st := s.clone()
	apply := func(rs []gamedata.Reward, cost bool) error {
		for _, r := range rs {
			if (r.Type != 21 && r.Type != 23) || r.ID != 0 || r.Count == 0 {
				return fmt.Errorf("hunting: invalid AP exchange")
			}
			p := &st.Free
			if r.Type == 23 {
				p = &st.Bonus
			}
			if cost {
				if *p < r.Count {
					return fmt.Errorf("hunting: insufficient AP")
				}
				*p -= r.Count
			} else {
				if r.Count > 2147483647-*p {
					return fmt.Errorf("hunting: AP overflow")
				}
				*p += r.Count
			}
		}
		return nil
	}
	if e := apply(costs, true); e != nil {
		return st, e
	}
	return st, apply(rewards, false)
}
func (s *Service) ExchangeAPOnce(identity string, costs, rewards []gamedata.Reward) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if identity == "" {
		return fmt.Errorf("hunting: missing AP identity")
	}
	if e := s.reload(); e != nil {
		return e
	}
	key := "apexchange:" + identity
	fpb, e := json.Marshal([][]gamedata.Reward{costs, rewards})
	if e != nil {
		return e
	}
	rs, e := s.loadBattleReceipts()
	if e != nil {
		return e
	}
	fp := string(fpb)
	if r, ok := rs[key]; ok {
		if r.Fingerprint != fp {
			return fmt.Errorf("hunting: AP identity conflict")
		}
		return nil
	}
	if e := s.refreshAP(); e != nil {
		return e
	}
	st, e := s.exchangeAP(costs, rewards)
	if e != nil {
		return e
	}
	if e = s.persist(st); e != nil {
		return e
	}
	rs[key] = battleReceipt{Fingerprint: fp}
	return s.saveBattleReceipts(rs)
}
