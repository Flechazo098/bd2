package hunting

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

type dispatchReceipt struct {
	Request, Response []byte
	Code              int
}
type dispatchJob struct {
	Group, ID, Count, Start, End, Free, Bonus uint64
	Rewards                                   []gamedata.BattleReward
	Runs                                      [][]gamedata.BattleReward
}
type dispatchState struct {
	Version  string                     `json:"version"`
	Jobs     map[string]dispatchJob     `json:"jobs"`
	Receipts map[string]dispatchReceipt `json:"receipts"`
}

func (s *Service) AttachDispatchEligibility(check func(*gamedata.DispatchDesign) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dispatchEligibility = check
}
func (s *Service) HandleSession(path string, req []byte, session string) (int, []byte, bool, error) {
	if strings.HasPrefix(path, "/HuntDispatch") {
		return s.handleDispatch(path, req, session)
	}
	return s.Handle(path, req)
}
func (s *Service) handleDispatch(path string, req []byte, session string) (int, []byte, bool, error) {
	code := map[string]int{"/HuntDispatchInfo": 189, "/HuntDispatchStart": 190, "/HuntDispatchEnd": 191, "/HuntDispatchRewardPreview": 194, "/HuntDispatch": 0}[path]
	if path != "/HuntDispatchInfo" && path != "/HuntDispatchStart" && path != "/HuntDispatchEnd" && path != "/HuntDispatchRewardPreview" && path != "/HuntDispatch" {
		return 0, nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, found, err := wire.Varint(req, 1)
	if err != nil || !found || seq == 0 {
		return code, nil, true, fmt.Errorf("hunting: missing sequence")
	}
	ds := dispatchState{Version: versionconfig.State(), Jobs: map[string]dispatchJob{}, Receipts: map[string]dispatchReceipt{}}
	raw, err := s.storage.Load("huntdispatch")
	if err != nil {
		return code, nil, true, err
	}
	if raw != nil {
		if err = stateio.RequireExactJSONObject(raw, "version", "jobs", "receipts"); err != nil {
			return code, nil, true, err
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&ds); err != nil {
			return code, nil, true, err
		}
		if ds.Version != versionconfig.State() || ds.Jobs == nil || ds.Receipts == nil {
			return code, nil, true, fmt.Errorf("hunting: invalid dispatch state")
		}
	}
	if path == "/HuntDispatchInfo" {
		var out []byte
		keys := make([]string, 0, len(ds.Jobs))
		for k := range ds.Jobs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = wire.AppendBytes(out, 1, dispatchJobWire(ds.Jobs[k]))
		}
		// A client without a remembered dispatch pack needs a default. This
		// server selects the smallest cleared ordinary hunting pack.
		var minimum uint64
		for key, state := range s.state.Packs {
			if state.Highest == 0 {
				continue
			}
			pack, err := strconv.ParseUint(key, 10, 32)
			if err != nil || pack == 0 {
				return code, nil, true, fmt.Errorf("hunting: invalid saved dispatch pack")
			}
			if minimum == 0 || pack < minimum {
				minimum = pack
			}
		}
		if minimum != 0 {
			out = wire.AppendVarint(out, 2, minimum)
		}
		return code, out, true, nil
	}
	for k, j := range ds.Jobs {
		if k != fmt.Sprintf("%d:%d", j.Group, j.ID) || j.Group == 0 || j.ID == 0 || j.Count == 0 || j.Count > 20 || j.Start == 0 || j.End < j.Start || len(j.Runs) != int(j.Count) || j.Free > 2147483647 || j.Bonus > 2147483647 {
			return code, nil, true, fmt.Errorf("hunting: invalid saved dispatch job")
		}
	}
	for k, r := range ds.Receipts {
		if k == "" || len(r.Request) == 0 || r.Response == nil {
			return code, nil, true, fmt.Errorf("hunting: invalid saved dispatch receipt")
		}
	}
	key := session + ":" + strconv.FormatUint(seq, 10)
	if r, ok := ds.Receipts[key]; ok {
		if !bytes.Equal(r.Request, append([]byte(path), req...)) {
			return code, nil, true, fmt.Errorf("hunting: sequence reused with different dispatch request")
		}
		return r.Code, r.Response, true, nil
	}
	group, _, e := wire.Varint(req, 2)
	id, _, e2 := wire.Varint(req, 3)
	if e != nil || e2 != nil || group == 0 || id == 0 {
		return code, nil, true, fmt.Errorf("hunting: invalid dispatch ID")
	}
	jobKey := fmt.Sprintf("%d:%d", group, id)
	job, exists := ds.Jobs[jobKey]
	d, err := s.dispatchLoad(group, id)
	if err != nil {
		return code, nil, true, err
	}
	var out []byte
	if path == "/HuntDispatchRewardPreview" {
		if !exists {
			return code, nil, true, fmt.Errorf("hunting: dispatch not started")
		}
		played := dispatchPlayed(job, d.ClearTime)
		out = wire.AppendVarint(out, 1, played)
		preview := dispatchCompletedRewards(job, played)
		remaining := d.AP * (job.Count - played)
		bonus := remaining
		if bonus > job.Bonus {
			bonus = job.Bonus
		}
		free := remaining - bonus
		if free > 0 {
			preview = append(preview, gamedata.BattleReward{Type: 21, Count: free})
		}
		if bonus > 0 {
			preview = append(preview, gamedata.BattleReward{Type: 23, Count: bonus})
		}
		bundle := dispatchPreview(preview)
		for _, r := range preview {
			v := wire.AppendVarint(nil, 2, r.ID)
			v = wire.AppendVarint(v, 3, r.Type)
			v = wire.AppendVarint(v, 4, r.Count)
			bundle = wire.AppendBytes(bundle, 1, v)
		}
		out = wire.AppendBytes(out, 2, bundle)
		return code, out, true, nil
	}
	if path == "/HuntDispatchEnd" {
		if !exists {
			return code, nil, true, fmt.Errorf("hunting: dispatch not started")
		}
		played := dispatchPlayed(job, d.ClearTime)
		completed := dispatchCompletedRewards(job, played)
		remaining := d.AP * (job.Count - played)
		refundBonus := remaining
		if refundBonus > job.Bonus {
			refundBonus = job.Bonus
		}
		refundFree := remaining - refundBonus
		next := s.clone()
		next.Free += refundFree
		next.Bonus += refundBonus
		if e := s.persist(next); e != nil {
			return code, nil, true, e
		}
		if refundFree > 0 {
			completed = append(completed, gamedata.BattleReward{Type: 21, Count: refundFree})
		}
		if refundBonus > 0 {
			completed = append(completed, gamedata.BattleReward{Type: 23, Count: refundBonus})
		}
		bundle, e := s.dispatchGrant("dispatch:"+key, completed)
		if e != nil {
			return code, nil, true, e
		}
		out = wire.AppendBytes(out, 1, bundle)
		delete(ds.Jobs, jobKey)
	} else {
		count, _, e := wire.Varint(req, 4)
		if e != nil || count == 0 || count > 20 {
			return code, nil, true, fmt.Errorf("hunting: dispatch count must be 1..20")
		}
		if exists {
			return code, nil, true, fmt.Errorf("hunting: dispatch already running")
		}
		for _, active := range ds.Jobs {
			design, e := s.dispatchLoad(active.Group, active.ID)
			if e != nil {
				return code, nil, true, e
			}
			if design.TypeGroup == d.TypeGroup {
				return code, nil, true, fmt.Errorf("hunting: dispatch category already running")
			}
		}
		if s.dispatchEligibility != nil {
			if e = s.dispatchEligibility(d); e != nil {
				return code, nil, true, e
			}
		} else {
			if d.TypeGroup != 0 {
				return code, nil, true, fmt.Errorf("hunting: SkyWay dispatch eligibility unavailable")
			}
			st := s.state.Packs[strconv.Itoa(int(d.Pack))]
			if st.Highest < d.GroundID {
				return code, nil, true, fmt.Errorf("hunting: dispatch requires cleared difficulty")
			}
		}
		if d.AP > uint64(2147483647)/count {
			return code, nil, true, fmt.Errorf("hunting: dispatch AP overflow")
		}
		cost := d.AP * count
		if s.state.Free < cost && s.state.Bonus < cost-s.state.Free {
			return code, nil, true, fmt.Errorf("hunting: insufficient hunting AP")
		}
		rewards, e := d.Roll(count, func(n uint64) (uint64, error) {
			v, e := rand.Int(rand.Reader, new(big.Int).SetUint64(n))
			if e != nil {
				return 0, e
			}
			return v.Uint64(), nil
		})
		if e != nil {
			return code, nil, true, e
		}
		next := s.clone()
		free := cost
		if free > next.Free {
			free = next.Free
		}
		bonus := cost - free
		next.Free -= free
		next.Bonus -= bonus
		if path == "/HuntDispatchStart" {
			now := uint64(time.Now().UnixMilli())
			runs := make([][]gamedata.BattleReward, count)
			for i := range runs {
				runs[i], e = d.Roll(1, func(n uint64) (uint64, error) {
					v, e := rand.Int(rand.Reader, new(big.Int).SetUint64(n))
					if e != nil {
						return 0, e
					}
					return v.Uint64(), nil
				})
				if e != nil {
					return code, nil, true, e
				}
			}
			job = dispatchJob{Group: group, ID: id, Count: count, Start: now, End: now + d.ClearTime*count*1000, Free: free, Bonus: bonus, Rewards: rewards, Runs: runs}
			ds.Jobs[jobKey] = job
			out = wire.AppendBytes(out, 1, dispatchJobWire(job))
		} else {
			bundle, e := s.dispatchGrant("dispatch:"+key, rewards)
			if e != nil {
				return code, nil, true, e
			}
			out = wire.AppendBytes(out, 1, bundle)
		}
		if e = s.persist(next); e != nil {
			return code, nil, true, e
		}
	}
	ds.Receipts[key] = dispatchReceipt{append([]byte(path), req...), out, code}
	b, e := json.Marshal(ds)
	if e != nil {
		return code, nil, true, e
	}
	if e = s.storage.Save("huntdispatch", b); e != nil {
		return code, nil, true, e
	}
	return code, out, true, nil
}
func dispatchPlayed(j dispatchJob, seconds uint64) uint64 {
	if seconds == 0 {
		return j.Count
	}
	now := uint64(time.Now().UnixMilli())
	if now <= j.Start {
		return 0
	}
	n := (now - j.Start) / (seconds * 1000)
	if n > j.Count {
		n = j.Count
	}
	return n
}
func dispatchJobWire(j dispatchJob) []byte {
	var out []byte
	for n, v := range map[int]uint64{1: j.Group, 2: j.ID, 3: j.Count, 4: j.Start, 5: j.End, 6: j.Free, 7: j.Bonus} {
		out = wire.AppendVarint(out, n, v)
	}
	return out
}
func dispatchPreview(rs []gamedata.BattleReward) []byte {
	var out []byte
	for _, r := range rs {
		v := wire.AppendVarint(nil, 2, r.ID)
		v = wire.AppendVarint(v, 3, r.Type)
		v = wire.AppendVarint(v, 4, r.Count)
		out = wire.AppendBytes(out, 6, v)
	}
	return out
}
func (s *Service) dispatchGrant(identity string, rs []gamedata.BattleReward) ([]byte, error) {
	var currency []gamedata.Reward
	var items []gamedata.BattleReward
	for _, r := range rs {
		switch r.Type {
		case 21, 23:
			continue
		case 2, 3, 4, 12, 20:
			currency = append(currency, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count})
		default:
			items = append(items, r)
		}
	}
	if _, err := s.wallet.GrantQuestOnce(identity+":currency", currency); err != nil {
		return nil, err
	}
	granted, err := s.inventory.GrantOnce(identity+":items", items)
	if err != nil {
		return nil, err
	}
	if len(granted) == 0 {
		granted = s.inventory.GrantedItems(identity + ":items")
	}
	out := dispatchPreview(rs)
	for _, r := range rs {
		if r.Type == 21 || r.Type == 23 {
			v := wire.AppendVarint(nil, 3, r.Type)
			v = wire.AppendVarint(v, 4, r.Count)
			out = wire.AppendBytes(out, 1, v)
		}
	}
	for _, r := range currency {
		v := wire.AppendVarint(nil, 2, r.ID)
		v = wire.AppendVarint(v, 3, r.Type)
		v = wire.AppendVarint(v, 4, r.Count)
		out = wire.AppendBytes(out, 1, v)
	}
	for _, item := range granted {
		out = wire.AppendBytes(out, 1, player.ItemWire(item))
	}
	return out, nil
}

func dispatchCompletedRewards(j dispatchJob, n uint64) []gamedata.BattleReward {
	var out []gamedata.BattleReward
	for i := uint64(0); i < n && i < uint64(len(j.Runs)); i++ {
		out = append(out, j.Runs[i]...)
	}
	return out
}
