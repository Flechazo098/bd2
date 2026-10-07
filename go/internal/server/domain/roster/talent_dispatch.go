package roster

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"

	"time"
)

type talentDispatchRow struct {
	ID       uint64            `json:"id"`
	Start    int64             `json:"start"`
	End      int64             `json:"end"`
	Identity string            `json:"identity"`
	Rewards  []gamedata.Reward `json:"rewards"`
	Claimed  bool              `json:"claimed"`
	Bundle   []byte            `json:"bundle"`
}
type talentDispatchState struct {
	Claims map[string]talentDispatchClaim `json:"claims"`
	Rows   map[uint64]talentDispatchRow   `json:"rows"`
	Starts map[string][]byte              `json:"starts"`
}
type talentDispatchClaim struct {
	Digest string `json:"digest"`
	Body   []byte `json:"body"`
}

type TalentDispatchService struct {
	store   stateio.Store
	design  map[uint64]gamedata.TalentDispatchDesign
	now     func() time.Time
	draw    func(uint64) (uint64, error)
	economy interface {
		Apply(ctx command.Context, _ string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error)
	}
}

func OpenTalentDispatch(store stateio.Store, d map[uint64]gamedata.TalentDispatchDesign, economy interface {
	Apply(ctx command.Context, _ string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error)
}) (*TalentDispatchService, error) {
	if store == nil || len(d) == 0 || economy == nil {
		return nil, fmt.Errorf("dispatch: invalid configuration")
	}
	return &TalentDispatchService{store: store, design: d, economy: economy, now: time.Now, draw: func(n uint64) (uint64, error) {
		if n == 0 {
			return 0, fmt.Errorf("dispatch: empty pool")
		}
		v, e := rand.Int(rand.Reader, new(big.Int).SetUint64(n))
		if e != nil {
			return 0, e
		}
		return v.Uint64(), nil
	}}, nil
}
func (s *TalentDispatchService) load(ctx command.Context) (talentDispatchState, error) {
	st := talentDispatchState{Rows: map[uint64]talentDispatchRow{}, Starts: map[string][]byte{}, Claims: map[string]talentDispatchClaim{}}
	b, e := s.store.Load(ctx.State, "talent_dispatch")
	if e != nil || b == nil {
		return st, e
	}
	if e = stateio.RequireExactJSONObject(b, "rows", "starts", "claims"); e != nil {
		return st, e
	}
	if e = json.Unmarshal(b, &st); e != nil {
		return st, e
	}
	if st.Rows == nil || st.Starts == nil || st.Claims == nil {
		return st, fmt.Errorf("dispatch: invalid state")
	}
	for id, r := range st.Rows {
		if _, ok := s.design[id]; !ok || r.ID != id || r.Start <= 0 || r.End <= r.Start || r.Identity == "" {
			return st, fmt.Errorf("dispatch: invalid row")
		}
	}
	return st, nil
}

func (s *TalentDispatchService) save(ctx command.Context, st talentDispatchState) error {
	b, e := json.Marshal(st)
	if e != nil {
		return e
	}
	return s.store.Save(ctx.State, "talent_dispatch", b)
}
