package hunting

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *Service) EnsureForPack(ctx command.Context, pack int) ([]byte, error) {

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
		if e = s.persist(ctx, next); e != nil {
			return nil, e
		}
	}
	return s.info(pack, d)
}

func (s *Service) SnapshotForPack(ctx command.Context, pack int) ([]byte, error) {

	d, e := s.load(pack)
	if e != nil {
		return nil, e
	}
	if len(d.Grounds) == 0 {
		return nil, nil
	}
	return s.info(pack, d)
}

func (s *Service) handleGroundSession(ctx command.Context, path string, req []byte, session string) (int, []byte, bool, error) {
	if path != "/HuntingGroundEnter" {
		return s.handleGroundBase(ctx, path, req)
	}
	seq, found, e := wire.Varint(req, 1)
	if e != nil || !found || seq == 0 {
		return 110, nil, true, fmt.Errorf("hunting: missing sequence")
	}
	key := "enter:" + session + ":" + strconv.FormatUint(seq, 10)
	rs, e := s.loadBattleReceipts(ctx)
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
	code, out, ok, e := s.handleGroundBase(ctx, path, req)
	if e != nil || !ok {
		return code, out, ok, e
	}
	rs[key] = battleReceipt{Fingerprint: fp, Bundle: out}
	if e = s.saveBattleReceipts(ctx, rs); e != nil {
		return code, nil, true, e
	}
	return code, out, ok, nil
}

// A catalog entry is not an account's active hunting run. The detail
// endpoint preserves an explicitly present empty message until entry.

func (s *Service) info(pack int, d *gamedata.HuntingPack) ([]byte, error) {
	st := s.state.Packs[strconv.Itoa(pack)]
	initial := st.Current == 0
	if st.Current == 0 {
		st.Current = d.Grounds[0].ID
	}
	g, ok := ground(d, st.Current)
	if !ok {
		return nil, fmt.Errorf("hunting: saved ground missing from GameData")
	}
	b := wire.AppendVarint(nil, 2, st.Current)
	if st.Highest != 0 {
		b = wire.AppendVarint(b, 3, st.Highest)
	}
	b = wire.AppendVarint(b, 5, uint64(pack))
	if st.Auto {
		b = wire.AppendVarint(b, 1, 1)
	}
	var monsterInfos [][]byte
	if initial {
		// The initial catalog lists ordinary monsters only. A boss is not an
		// active account encounter simply because its design row exists.
		for _, id := range g.Monsters {
			monsterInfos = append(monsterInfos, monsterWire(d.Monsters[id], true))
		}
	} else {
		monsterInfos = monstersWithState(d, g, st.Defeated)
	}
	for _, m := range monsterInfos {
		b = wire.AppendBytes(b, 4, m)
	}
	return b, nil
}

func monsterWire(m gamedata.HuntingMonster, active bool) []byte {
	b := wire.AppendVarint(nil, 1, m.ID)
	b = wire.AppendVarint(b, 2, m.Decks[0])
	if active {
		b = wire.AppendVarint(b, 6, 1)
	}
	return b
}

func monstersWithState(d *gamedata.HuntingPack, g gamedata.HuntingGround, defeated []uint64) [][]byte {
	dead := map[uint64]bool{}
	for _, id := range defeated {
		dead[id] = true
	}
	boss := len(g.Monsters) == 0
	all := true
	for _, id := range g.Monsters {
		all = all && dead[id]
	}
	boss = boss || all
	out := make([][]byte, 0, len(g.Monsters)+1)
	for _, id := range g.Monsters {
		out = append(out, monsterWire(d.Monsters[id], !dead[id]))
	}
	if boss {
		out = append(out, monsterWire(d.Monsters[g.BossID], true))
	}
	return out
}

func (s *Service) CompleteBattle(ctx command.Context, pack int, mode, monster, deck uint64, receipt string) ([]byte, [][]byte, error) {
	if mode != BattleMode {
		return nil, nil, nil
	}

	if receipt == "" {
		return nil, nil, fmt.Errorf("hunting: missing battle receipt")
	}
	fingerprint := fmt.Sprintf("%d:%d:%d:%d", pack, mode, monster, deck)
	ledger, err := s.loadBattleReceipts(ctx)
	if err != nil {
		return nil, nil, err
	}
	if saved, ok := ledger[receipt]; ok {
		if saved.Fingerprint != fingerprint {
			return nil, nil, fmt.Errorf("hunting: battle receipt conflict")
		}
		return saved.Bundle, saved.Monsters, nil
	}
	d, g, m, err := s.validate(pack, monster, deck)
	if err != nil {
		return nil, nil, err
	}
	rewards := m.Rewards[deck]
	identity := "hunting:" + receipt
	currency := make([]gamedata.Reward, 0)
	stack := make([]gamedata.BattleReward, 0)
	for _, r := range rewards {
		if r.Type == 2 || r.Type == 3 || r.Type == 4 || r.Type == 12 || r.Type == 20 {
			currency = append(currency, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count}) //nolint:staticcheck // S1016
		} else {
			stack = append(stack, r)
		}
	}
	var grantedBundle []byte
	var items []assets.Item
	if s.grant != nil {
		rs := make([]gamedata.Reward, len(rewards))
		for i, r := range rewards {
			rs[i] = gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count} //nolint:staticcheck // S1016
		}

		grantedBundle, err = s.grant(ctx, identity, rs)

		if err != nil {
			return nil, nil, err
		}
	} else {
		if _, err := s.wallet.GrantQuestOnce(ctx, identity+":currency", currency); err != nil {
			return nil, nil, err
		}
		items, err = s.inventory.GrantOnce(ctx, identity+":items", stack)
		if err != nil {
			return nil, nil, err
		}
		if len(items) == 0 {
			items = s.inventory.GrantedItems(identity + ":items")
		}
	}
	next := s.clone()
	cost := d.NormalAP
	if m.Type == 1 {
		cost = d.BossAP
	}
	if next.Free >= cost {
		next.Free -= cost
	} else {
		next.Bonus -= cost - next.Free
		next.Free = 0
	}
	st := next.Packs[strconv.Itoa(pack)]
	if monster == g.BossID {
		if st.Highest < g.ID {
			st.Highest = g.ID
		}
		st.Defeated = nil
	} else {
		st.Defeated = append(append([]uint64(nil), st.Defeated...), monster)
	}
	next.Packs[strconv.Itoa(pack)] = st
	next.Receipts[receipt] = true
	var bundle []byte
	if s.grant != nil {
		bundle = grantedBundle
	}
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(item))
		view := wire.AppendVarint(nil, 2, item.ID)
		view = wire.AppendVarint(view, 3, item.Type)
		view = wire.AppendVarint(view, 4, item.Count)
		bundle = wire.AppendBytes(bundle, 6, view)
	}
	for _, r := range currency {
		if s.grant != nil {
			break
		}
		b := wire.AppendVarint(nil, 3, r.Type)
		b = wire.AppendVarint(b, 4, r.Count)
		bundle = wire.AppendBytes(bundle, 1, b)
	}
	updates := [][]byte{monsterWire(m, false)}
	if monster == g.BossID {
		for _, id := range g.Monsters {
			updates = append(updates, monsterWire(d.Monsters[id], true))
		}
	} else if len(st.Defeated) == len(g.Monsters) {
		updates = append(updates, monsterWire(d.Monsters[g.BossID], true))
	}
	if err := s.persist(ctx, next); err != nil {
		return nil, nil, err
	}
	latest, err := s.loadBattleReceipts(ctx)
	if err != nil {
		return nil, nil, err
	}
	maps.Copy(ledger, latest)
	ledger[receipt] = battleReceipt{Fingerprint: fingerprint, Bundle: bundle, Monsters: updates}
	if err := s.saveBattleReceipts(ctx, ledger); err != nil {
		return nil, nil, err
	}
	return bundle, updates, nil
}

func packed(req []byte, n int) ([]uint64, error) {
	var out []uint64
	err := wire.Walk(req, func(f wire.Field) error {
		if f.Number != n {
			return nil
		}
		if f.Type == 0 {
			v, _ := binary.Uvarint(f.Value)
			out = append(out, v)
			return nil
		}
		if f.Type != 2 {
			return fmt.Errorf("hunting: invalid repeated integer")
		}
		for raw := f.Value; len(raw) > 0; {
			v, n := binary.Uvarint(raw)
			if n <= 0 {
				return fmt.Errorf("hunting: invalid packed integer")
			}
			out = append(out, v)
			raw = raw[n:]
		}
		return nil
	})
	return out, err
}

func (s *Service) Handle(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	commandSession := ctx.SessionID
	if strings.HasPrefix(path, "/HuntDispatch") {
		return s.handleDispatch(ctx, path, req, commandSession)
	}
	return s.handleGroundSession(ctx, path, req, commandSession)
}

func (s *Service) handleDispatch(ctx command.Context, path string, req []byte, session string) (int, []byte, bool, error) {
	code := map[string]int{"/HuntDispatchInfo": 189, "/HuntDispatchStart": 190, "/HuntDispatchEnd": 191, "/HuntDispatchRewardPreview": 194, "/HuntDispatch": 0}[path]
	if path != "/HuntDispatchInfo" && path != "/HuntDispatchStart" && path != "/HuntDispatchEnd" && path != "/HuntDispatchRewardPreview" && path != "/HuntDispatch" {
		return 0, nil, false, nil
	}

	if e := s.refreshAP(ctx); e != nil {
		return code, nil, true, e
	}
	seq, found, err := wire.Varint(req, 1)
	if err != nil || !found || seq == 0 {
		return code, nil, true, fmt.Errorf("hunting: missing sequence")
	}
	ds := dispatchState{Version: versionconfig.State(), Jobs: map[string]dispatchJob{}, Receipts: map[string]dispatchReceipt{}}
	raw, err := s.storage.Load(ctx.State, "huntdispatch")
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
		bonus := min(remaining, job.Bonus)
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
		refundBonus := min(remaining, job.Bonus)
		refundFree := remaining - refundBonus
		next := s.clone()
		next.Free += refundFree
		next.Bonus += refundBonus
		if e := s.persist(ctx, next); e != nil {
			return code, nil, true, e
		}
		if refundFree > 0 {
			completed = append(completed, gamedata.BattleReward{Type: 21, Count: refundFree})
		}
		if refundBonus > 0 {
			completed = append(completed, gamedata.BattleReward{Type: 23, Count: refundBonus})
		}
		bundle, e := s.dispatchGrant(ctx, "dispatch:"+key, completed)
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
		free := min(cost, next.Free)
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
			beforeFree, beforeBonus := s.state.Free, s.state.Bonus
			bundle, e := s.dispatchGrant(ctx, "dispatch:"+key, rewards)
			if e != nil {
				return code, nil, true, e
			}
			out = wire.AppendBytes(out, 1, bundle)
			if s.state.Free >= beforeFree {
				next.Free += s.state.Free - beforeFree
			} else {
				delta := beforeFree - s.state.Free
				if next.Free < delta {
					return code, nil, true, fmt.Errorf("hunting: AP grant conflict")
				}
				next.Free -= delta
			}
			if s.state.Bonus >= beforeBonus {
				next.Bonus += s.state.Bonus - beforeBonus
			} else {
				delta := beforeBonus - s.state.Bonus
				if next.Bonus < delta {
					return code, nil, true, fmt.Errorf("hunting: AP grant conflict")
				}
				next.Bonus -= delta
			}
		}
		if e = s.persist(ctx, next); e != nil {
			return code, nil, true, e
		}
	}
	ds.Receipts[key] = dispatchReceipt{append([]byte(path), req...), out, code}
	b, e := json.Marshal(ds)
	if e != nil {
		return code, nil, true, e
	}
	if e = s.storage.Save(ctx.State, "huntdispatch", b); e != nil {
		return code, nil, true, e
	}
	return code, out, true, nil
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

func (s *Service) dispatchGrant(ctx command.Context, identity string, rs []gamedata.BattleReward) ([]byte, error) {
	if s.grant != nil {
		var rewards []gamedata.Reward
		var ap []gamedata.BattleReward
		for _, r := range rs {
			if r.Type == 21 || r.Type == 23 {
				ap = append(ap, r)
			} else {
				rewards = append(rewards, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count}) //nolint:staticcheck // S1016
			}
		}

		bundle, err := s.grant(ctx, identity, rewards)

		if err != nil {
			return nil, err
		}
		bundle = append(bundle, dispatchPreview(ap)...)
		for _, r := range ap {
			v := wire.AppendVarint(nil, 3, r.Type)
			v = wire.AppendVarint(v, 4, r.Count)
			bundle = wire.AppendBytes(bundle, 1, v)
		}
		return bundle, nil
	}

	var currency []gamedata.Reward
	var items []gamedata.BattleReward
	for _, r := range rs {
		switch r.Type {
		case 21, 23:
			continue
		case 2, 3, 4, 12, 20:
			currency = append(currency, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count}) //nolint:staticcheck // S1016
		default:
			items = append(items, r)
		}
	}
	if _, err := s.wallet.GrantQuestOnce(ctx, identity+":currency", currency); err != nil {
		return nil, err
	}
	granted, err := s.inventory.GrantOnce(ctx, identity+":items", items)
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
		out = wire.AppendBytes(out, 1, assets.ItemWire(item))
	}
	return out, nil
}

func (s *Service) APChargeInfo(ctx command.Context) ([]byte, error) {

	if e := s.refreshAP(ctx); e != nil {
		return nil, e
	}
	raw, e := s.storage.Load(ctx.State, "huntingapclock")
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
func (s *Service) handleGroundBase(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	if path == "/HuntDispatch" || path == "/HuntDispatchInfo" || path == "/HuntDispatchStart" || path == "/HuntDispatchEnd" || path == "/HuntDispatchRewardPreview" {
		return s.handleDispatch(ctx, path, req, ctx.SessionID)
	}
	if path != "/HuntingGroundInfo" && path != "/HuntingGroundInfoList" && path != "/HuntingGroundEnter" {
		return 0, nil, false, nil
	}

	seq, found, err := wire.Varint(req, 1)
	if err != nil || !found || seq == 0 {
		return 0, nil, true, fmt.Errorf("hunting: missing sequence")
	}
	if path == "/HuntingGroundInfoList" {
		ids, err := packed(req, 2)
		if err != nil {
			return 387, nil, true, err
		}
		var response []byte
		seen := map[uint64]bool{}
		for _, id := range ids {
			if id == 0 || id > math.MaxInt32 || seen[id] {
				return 387, nil, true, fmt.Errorf("hunting: invalid pack list")
			}
			seen[id] = true
			d, err := s.load(int(id))
			if err != nil {
				return 387, nil, true, err
			}
			if len(d.Grounds) == 0 {
				response = wire.AppendBytes(response, 1, wire.AppendVarint(nil, 5, id))
				continue
			}
			info, err := s.info(int(id), d)
			if err != nil {
				return 387, nil, true, err
			}
			response = wire.AppendBytes(response, 1, info)
		}
		return 387, response, true, nil
	}
	pack, found, err := wire.Varint(req, 2)
	if err != nil || !found || pack == 0 || pack > math.MaxInt32 {
		return 134, nil, true, fmt.Errorf("hunting: invalid pack")
	}
	d, err := s.load(int(pack))
	if err != nil {
		return 134, nil, true, err
	}
	if path == "/HuntingGroundInfo" {

		if s.state.Packs[strconv.Itoa(int(pack))].Current == 0 {
			return 134, wire.AppendBytes(nil, 1, nil), true, nil
		}
		b, err := s.info(int(pack), d)
		return 134, wire.AppendBytes(nil, 1, b), true, err
	}
	if len(d.Grounds) == 0 {
		return 110, nil, true, fmt.Errorf("hunting: pack has no hunting ground")
	}
	current, err := s.currentPack(ctx)
	if err != nil || current != int(pack) {
		return 110, nil, true, fmt.Errorf("hunting: enter pack is not current")
	}
	id, _, err := wire.Varint(req, 3)
	if err != nil {
		return 110, nil, true, err
	}
	auto, _, err := wire.Varint(req, 4)
	if err != nil || auto > 1 {
		return 110, nil, true, fmt.Errorf("hunting: invalid auto flag")
	}
	st := s.state.Packs[strconv.Itoa(int(pack))]
	g, ok := ground(d, id)
	if !ok {
		return 110, nil, true, fmt.Errorf("hunting: unknown ground")
	}
	if s.eligibility != nil {
		if err := s.eligibility(ctx, int(pack), g.Difficulty); err != nil {
			return 110, nil, true, err
		}
	} else if id != d.Grounds[0].ID {
		return 110, nil, true, fmt.Errorf("hunting: main quest difficulty eligibility unavailable")
	}
	if st.Current != id {
		st.Defeated = nil
	}
	st.Current, st.Auto = id, auto != 0
	next := s.clone()
	next.Packs[strconv.Itoa(int(pack))] = st
	if err := s.persist(ctx, next); err != nil {
		return 110, nil, true, err
	}
	var out []byte
	for _, m := range monstersWithState(d, g, st.Defeated) {
		out = wire.AppendBytes(out, 1, m)
	}
	return 110, out, true, nil
}
