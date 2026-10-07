package hunting

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"encoding/json"
	"fmt"
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

func (s *Service) loadBattleReceipts(ctx command.Context) (map[string]battleReceipt, error) {
	b, e := s.storage.Load(ctx.State, "huntingbattle")
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
func (s *Service) saveBattleReceipts(ctx command.Context, rs map[string]battleReceipt) error {
	b, e := json.Marshal(battleLedger{versionconfig.State(), rs})
	if e != nil {
		return e
	}
	return s.storage.Save(ctx.State, "huntingbattle", b)
}
func (s *Service) AttachEligibility(check func(ctx command.Context, _ int, _ uint64) error) {

	s.eligibility = check
}
func (s *Service) AttachRewards(grant func(command.Context, string, []gamedata.Reward) ([]byte, error)) {

	s.grant = grant
}

// EnsureForPack initializes the ordinary stage when the world loads a pack.

// Ground mutations have their own persisted session+sequence replay ledger.

func (s *Service) reload(ctx command.Context) error {
	b, e := s.storage.Load(ctx.State, "hunting")
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
func (s *Service) CanExchangeAP(ctx command.Context, costs, rewards []gamedata.Reward) error {

	if e := s.reload(ctx); e != nil {
		return e
	}
	if e := s.refreshAP(ctx); e != nil {
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
func (s *Service) ExchangeAPOnce(ctx command.Context, identity string, costs, rewards []gamedata.Reward) error {

	if identity == "" {
		return fmt.Errorf("hunting: missing AP identity")
	}
	if e := s.reload(ctx); e != nil {
		return e
	}
	key := "apexchange:" + identity
	fpb, e := json.Marshal([][]gamedata.Reward{costs, rewards})
	if e != nil {
		return e
	}
	rs, e := s.loadBattleReceipts(ctx)
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
	if e := s.refreshAP(ctx); e != nil {
		return e
	}
	st, e := s.exchangeAP(costs, rewards)
	if e != nil {
		return e
	}
	if e = s.persist(ctx, st); e != nil {
		return e
	}
	rs[key] = battleReceipt{Fingerprint: fp}
	return s.saveBattleReceipts(ctx, rs)
}
