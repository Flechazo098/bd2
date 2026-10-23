package eventplay

import (
	"bd2server/internal/common/util"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/domain/progression/conditions"
	"bd2server/internal/server/protocol/wire"
	"fmt"
	"math"
	"sort"
	"strings"
)

func playRecord(st *snapshot, uid, game, stage, mode uint64, family string) record {
	r := st.Records[recKey(uid, stage, mode, family)]
	if r.Clears == nil {
		r = record{UID: uid, Game: game, Stage: stage, Mode: mode, Family: family, Clears: map[string]bool{}}
	}
	return r
}

func (s *Service) singlePlayerGame(ctx command.Context, path string, req []byte, st *snapshot, key string) ([]byte, error) {
	f := family(path)
	if strings.Contains(path, "Ranking") || strings.Contains(path, "UserRecord") {
		return s.arcadeRanking(ctx, path, req)
	}
	if path == "/MiniGameSichuanInfo" {
		uids, err := list(req, 2)
		if err != nil {
			return nil, err
		}
		if len(uids) == 0 {
			uids = []uint64{0}
		}
		var out []byte
		for _, uid := range uids {
			c, err := s.calendar(uid, f)
			if err != nil {
				return nil, err
			}
			body := s.sichuanInfo(st, c.UID)
			world, e := s.arcadeWorldBest(ctx, c.UID, st, 3, 4, 5)
			if e != nil {
				return nil, e
			}
			out = wire.AppendBytes(out, 1, append(body, world...))
		}
		return out, nil
	}
	if strings.HasSuffix(path, "Info") {
		c, err := s.calendar(num(req, 2), f)
		if err != nil {
			return nil, err
		}
		switch f {
		case "Hopscotch":
			return s.hopscotchInfo(st, c.UID), nil
		case "PackMan":
			return wire.AppendVarint(nil, 1, playRecord(st, c.UID, c.ID, 0, 2, f).Best), nil
		case "Rhythm":
			var out, bundle []byte
			for _, row := range s.design.Tables["RhythmGameMusicTable"] {
				for mode := range uint64(3) {
					r := playRecord(st, c.UID, c.ID, num(row, 5), mode, f)
					out = wire.AppendBytes(out, 1, rhythmRecordWire(r))
					bundle = append(bundle, r.PendingBundle...)
					r.PendingBundle = nil
					st.Records[recKey(r.UID, r.Stage, r.Mode, f)] = r
				}
			}
			if len(bundle) > 0 {
				out = wire.AppendBytes(out, 2, bundle)
			}
			return out, nil
		}
	}
	if strings.HasSuffix(path, "Start") {
		c, err := s.calendar(num(req, 2), f)
		if err != nil {
			return nil, err
		}
		if now := s.now().UnixMilli(); now < c.Start || now >= c.End {
			return nil, fmt.Errorf("eventplay: season closed")
		}
		a := Run{UID: c.UID, Game: c.ID, Family: f, Started: uint64(s.now().UnixMilli()), Session: ctx.SessionID, Stage: num(req, 3), Mode: num(req, 4)}
		switch f {
		case "Sichuan":
			a.Stage, a.Mode = num(req, 4), num(req, 3)
			row, err := s.sichuanStage(a.Mode, a.Stage)
			if err != nil {
				return nil, err
			}
			if !s.sichuanActive(st, a.UID, a.Mode, a.Stage) {
				return nil, fmt.Errorf("eventplay: Sichuan level locked")
			}
			var blocks []byte
			var total uint64
			seen := map[uint64]bool{}
			err = wire.Walk(req, func(field wire.Field) error {
				if field.Number != 5 {
					return nil
				}
				if field.Type != 2 {
					return wire.ErrMalformed
				}
				id, count := num(field.Value, 1), num(field.Value, 2)
				if count == 0 || count > num(row, 8) || count%2 != 0 || id/1000 == 12 || id/1000 == 10 || id == 0 || seen[id] {
					return fmt.Errorf("eventplay: invalid Sichuan block inventory")
				}
				seen[id] = true
				total += count
				blocks = wire.AppendBytes(blocks, 1, field.Value)
				return nil
			})
			if err != nil {
				return nil, err
			}
			if total == 0 || total > num(row, 8) {
				return nil, fmt.Errorf("eventplay: Sichuan block count differs from layout")
			}
			a.Blocks = blocks
		case "Rhythm":
			a.Mode, a.Char = num(req, 5), num(req, 4)
			if err := s.validateStage(f, a.Stage, a.Mode); err != nil {
				return nil, err
			}
			if _, err := s.design.Row("RhythmGameCharTable", 2, a.Char); err != nil {
				return nil, err
			}
		case "Hopscotch":
			a.Mode = 0
			if err := s.validateStage(f, a.Stage, 0); err != nil {
				return nil, err
			}
		case "PackMan":
			a.Mode, a.Stage = num(req, 3), 1
			if _, err := s.packManRule(a.Mode); err != nil {
				return nil, err
			}
		}
		st.Runs[f] = a
		if s.onProgress != nil {
			if err := s.onProgress(ctx, conditions.Event{Type: conditions.MiniGamePlay, Sub: c.ID, Count: 1}); err != nil {
				return nil, err
			}
			if err := s.onProgress(ctx, conditions.Event{Type: conditions.EventFieldMiniGamePlay, Sub: c.ID, Count: 1}); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	a, exists := st.Runs[f]
	if !exists {
		return nil, fmt.Errorf("eventplay: no active run")
	}
	calendar, err := s.calendar(a.UID, f)
	if err != nil {
		return nil, err
	}
	if now := s.now().UnixMilli(); now < calendar.Start || now >= calendar.End {
		return nil, fmt.Errorf("eventplay: season closed")
	}
	if path == "/MiniGamePackManStageClear" {
		if num(req, 2) != a.Stage {
			return nil, fmt.Errorf("eventplay: PackMan stage out of order")
		}
		rule, err := s.packManRule(a.Mode)
		if err != nil {
			return nil, err
		}
		if max := num(rule, 4); max > 0 && a.Stage >= max {
			return nil, fmt.Errorf("eventplay: final PackMan stage requires End")
		}
		progress, _, err := wire.Bytes(req, 3)
		if err != nil {
			return nil, err
		}
		if _, err = s.validatePackManRecord(a, progress, true); err != nil {
			return nil, err
		}
		a.PackRecord = append([]byte(nil), progress...)
		a.Stage++
		st.Runs[f] = a
		return nil, nil
	}
	if !strings.HasSuffix(path, "End") {
		return nil, fmt.Errorf("eventplay: unsupported single player transition")
	}
	var out []byte
	switch f {
	case "Sichuan":
		out, err = s.endSichuan(ctx, req, a, st, key)
	case "Hopscotch":
		out, err = s.endHopscotch(ctx, req, a, st, key)
	case "PackMan":
		out, err = s.endPackMan(ctx, req, a, st, key)
	case "Rhythm":
		out, err = s.endRhythm(ctx, req, a, st, key)
	}
	if err != nil {
		return nil, err
	}
	delete(st.Runs, f)
	return out, nil
}

func (s *Service) sichuanStage(group, id uint64) ([]byte, error) {
	for _, row := range s.design.Rows("SichuanEventTable", 1, group) {
		if num(row, 2) == id {
			return s.design.Row("SichuanStageTable", 2, num(row, 3))
		}
	}
	return nil, fmt.Errorf("eventplay: Sichuan event level missing")
}

func (s *Service) sichuanActive(st *snapshot, uid, group, id uint64) bool {
	if group == 2 {
		return true
	}
	if group != 1 {
		return false
	}
	for _, row := range s.design.Rows("SichuanEventTable", 1, group) {
		stage := num(row, 2)
		if stage < id && !st.Records[recKey(uid, stage, group, "Sichuan")].Clears["clear"] {
			return false
		}
	}
	return true
}

func (s *Service) sichuanInfo(st *snapshot, uid uint64) []byte {
	out := wire.AppendVarint(nil, 1, uid)
	best := float64(0)
	for _, row := range s.design.Tables["SichuanEventTable"] {
		group, id := num(row, 1), num(row, 2)
		r := st.Records[recKey(uid, id, group, "Sichuan")]
		v := wire.AppendVarint(nil, 1, group)
		v = wire.AppendVarint(v, 2, id)
		for i := range 3 {
			if r.Clears[fmt.Sprintf("star:%d", i)] {
				v = wire.AppendVarint(v, 3, uint64(i))
			}
		}
		out = wire.AppendBytes(out, 2, v)
		v = wire.AppendVarint(nil, 1, group)
		v = wire.AppendVarint(v, 2, id)
		v = wire.AppendVarint(v, 3, 1)
		out = wire.AppendBytes(out, 8, v)
		if group == 2 && r.BestScore > 0 && (best == 0 || r.BestScore < best) {
			best = r.BestScore
		}
	}
	out = wire.AppendDouble(out, 6, best)
	ledger := st.Records[recKey(uid, 0, 0, "Sichuan")]
	for _, row := range s.design.Tables["SichuanRewardTable"] {
		group, id := num(row, 1), num(row, 2)
		if ledger.Clears[fmt.Sprintf("reward:%d:%d", group, id)] {
			v := wire.AppendVarint(nil, 1, group)
			v = wire.AppendVarint(v, 2, id)
			out = wire.AppendBytes(out, 7, v)
		}
	}
	return out
}

func (s *Service) endSichuan(ctx command.Context, req []byte, a Run, st *snapshot, key string) ([]byte, error) {
	encoded := num(req, 3)
	if encoded < 7 || (encoded-7)%13 != 0 {
		return nil, fmt.Errorf("eventplay: invalid Sichuan game checksum")
	}
	score := (encoded - 7) / 13
	if score%11 != num(req, 4) || score > math.MaxInt32 {
		return nil, fmt.Errorf("eventplay: Sichuan checksum mismatch")
	}
	row, err := s.sichuanStage(a.Mode, a.Stage)
	if err != nil {
		return nil, err
	}
	r := playRecord(st, a.UID, a.Game, a.Stage, a.Mode, a.Family)
	clear := num(req, 2) == 0 && num(req, 6) == 0
	if clear {
		limits, err := list(row, 3)
		if err != nil {
			return nil, err
		}
		if len(limits) == 0 || score == 0 || score > limits[0] {
			return nil, fmt.Errorf("eventplay: Sichuan clear time outside limit")
		}
		remaining := map[uint64]uint64{}
		_ = wire.Walk(a.Blocks, func(f wire.Field) error { remaining[num(f.Value, 1)] = num(f.Value, 2); return nil })
		turn := uint64(0)
		err = wire.Walk(req, func(f wire.Field) error {
			if f.Number != 5 {
				return nil
			}
			if f.Type != 2 {
				return wire.ErrMalformed
			}
			if n := num(f.Value, 1); n <= turn {
				return fmt.Errorf("eventplay: Sichuan turn order invalid")
			} else {
				turn = n
			}
			ids, e := list(f.Value, 2)
			if e != nil {
				return e
			}
			if len(ids) != 2 || ids[0] != ids[1] || remaining[ids[0]] < 2 {
				return fmt.Errorf("eventplay: Sichuan turn differs from starting inventory")
			}
			remaining[ids[0]] -= 2
			return nil
		})
		if err != nil {
			return nil, err
		}
		for _, count := range remaining {
			if count != 0 {
				return nil, fmt.Errorf("eventplay: Sichuan board not cleared")
			}
		}
		r.Clears["clear"] = true
		for i, limit := range limits {
			if score <= limit {
				r.Clears[fmt.Sprintf("star:%d", i)] = true
			}
		}
		if r.BestScore == 0 || float64(score) < r.BestScore {
			r.Best = score
			r.BestScore = float64(score)
			r.AchievedAt = uint64(s.now().UnixMilli())
		}
	}
	st.Records[recKey(a.UID, a.Stage, a.Mode, a.Family)] = r
	ledger := playRecord(st, a.UID, a.Game, 0, 0, a.Family)
	var rewards []gamedata.BattleReward
	for _, reward := range s.design.Tables["SichuanRewardTable"] {
		group, id := num(reward, 1), num(reward, 2)
		count := uint64(0)
		if group == 2 {
			count = boolWire(clear && a.Mode == group)
		} else {
			for _, progress := range st.Records {
				if progress.UID != a.UID || progress.Family != a.Family || progress.Mode != group {
					continue
				}
				for i := range 3 {
					if progress.Clears[fmt.Sprintf("star:%d", i)] {
						count++
					}
				}
			}
		}
		identity := fmt.Sprintf("reward:%d:%d", group, id)
		if count < num(reward, 3) || group != 2 && ledger.Clears[identity] {
			continue
		}
		rr, e := gamedata.EventPlayRewards(reward, 6, 5, 4)
		if e != nil {
			return nil, e
		}
		rewards = append(rewards, rr...)
		ledger.Clears[identity] = true
	}
	st.Records[recKey(a.UID, 0, 0, a.Family)] = ledger
	var out []byte
	if len(rewards) > 0 {
		bundle, e := s.grant(ctx, "eventplay:"+key, rewards)
		if e != nil {
			return nil, e
		}
		out = wire.AppendBytes(out, 1, bundle)
	}
	if num(req, 6) > 0 {
		out = wire.AppendVarint(out, 5, 1)
	}
	world, e := s.arcadeWorldBest(ctx, a.UID, st, 2, 3, 4)
	if e != nil {
		return nil, e
	}
	out = append(out, world...)
	return out, nil
}

func boolWire(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}

func (s *Service) hopscotchInfo(st *snapshot, uid uint64) []byte {
	ledger := st.Records[recKey(uid, 0, 0, "Hopscotch")]
	var out []byte
	for _, row := range s.design.Tables["HopscotchGalleryTable"] {
		id := num(row, 4)
		if ledger.Clears[fmt.Sprintf("gallery:%d", id)] {
			out = wire.AppendVarint(out, 1, id)
		}
	}
	for _, row := range s.design.Tables["HopscotchStageTable"] {
		id := num(row, 6)
		r := st.Records[recKey(uid, id, 0, "Hopscotch")]
		v := wire.AppendVarint(nil, 1, id)
		v = wire.AppendVarint(v, 2, boolWire(r.Clears["clear"]))
		out = wire.AppendBytes(out, 2, v)
	}
	for _, row := range s.design.Tables["HopscotchRewardTable"] {
		id := num(row, 3)
		if ledger.Clears[fmt.Sprintf("reward:%d", id)] {
			out = wire.AppendVarint(out, 3, id)
		}
	}
	return out
}

func (s *Service) endHopscotch(ctx command.Context, req []byte, a Run, st *snapshot, key string) ([]byte, error) {
	if len(s.design.Tables["HopscotchDefaultTable"]) == 0 {
		return nil, fmt.Errorf("eventplay: Hopscotch default missing")
	}
	d := s.design.Tables["HopscotchDefaultTable"][0]
	area, elapsed := num(req, 2), num(req, 3)
	if area > 100 || elapsed > math.MaxInt32 || elapsed > num(d, 9)*1000 {
		return nil, fmt.Errorf("eventplay: invalid Hopscotch result")
	}
	row, err := s.design.Row("HopscotchStageTable", 6, a.Stage)
	if err != nil {
		return nil, err
	}
	r := playRecord(st, a.UID, a.Game, a.Stage, 0, a.Family)
	clear := area >= num(d, 4)
	r.Clears["play"] = true
	if clear {
		r.Clears["clear"] = true
	}
	if clear && elapsed > 0 && (r.ClearTime == 0 || elapsed < r.ClearTime) {
		r.CapturedArea, r.ClearTime, r.Best = area, elapsed, area
		r.AchievedAt = uint64(s.now().UnixMilli())
	}
	if num(req, 4) == 0 && clear {
		r.Clears["no-continue"] = true
	}
	if area == 100 {
		r.Clears["full"] = true
	}
	st.Records[recKey(a.UID, a.Stage, 0, a.Family)] = r
	ledger := playRecord(st, a.UID, a.Game, 0, 0, a.Family)
	var out []byte
	gallery := func(id uint64) {
		if id > 0 && !ledger.Clears[fmt.Sprintf("gallery:%d", id)] {
			ledger.Clears[fmt.Sprintf("gallery:%d", id)] = true
			out = wire.AppendVarint(out, 1, id)
		}
	}
	gallery(num(row, 2))
	if clear {
		gallery(num(row, 1))
	}
	allPlayed, allCleared := true, true
	for _, stage := range s.design.Tables["HopscotchStageTable"] {
		p := st.Records[recKey(a.UID, num(stage, 6), 0, a.Family)]
		allPlayed = allPlayed && p.Clears["play"]
		allCleared = allCleared && p.Clears["clear"]
	}
	if allPlayed {
		gallery(num(d, 2))
	}
	if allCleared {
		gallery(num(d, 1))
	}
	var rewards []gamedata.BattleReward
	for _, reward := range s.design.Tables["HopscotchRewardTable"] {
		id, sub := num(reward, 3), num(reward, 1)
		p := st.Records[recKey(a.UID, sub, 0, a.Family)]
		earned := false
		switch num(reward, 2) {
		case 1:
			earned = p.Clears["play"]
		case 2:
			earned = p.Clears["clear"]
		case 3:
			earned = allPlayed
		case 4:
			earned = allCleared
		case 5:
			for _, p := range st.Records {
				if p.UID == a.UID && p.Family == a.Family && p.Clears["no-continue"] {
					earned = true
				}
			}
		case 6:
			earned = p.Clears["full"]
		}
		identity := fmt.Sprintf("reward:%d", id)
		if !earned || ledger.Clears[identity] {
			continue
		}
		rr, e := gamedata.EventPlayRewards(reward, 7, 6, 4)
		if e != nil {
			return nil, e
		}
		for _, v := range rr {
			if v.Type > 0 && v.Count > 0 {
				rewards = append(rewards, v)
			}
		}
		ledger.Clears[identity] = true
		out = wire.AppendVarint(out, 3, id)
	}
	v := wire.AppendVarint(nil, 1, a.Stage)
	v = wire.AppendVarint(v, 2, boolWire(r.Clears["clear"]))
	out = wire.AppendBytes(out, 2, v)
	if len(rewards) > 0 {
		bundle, e := s.grant(ctx, "eventplay:"+key, rewards)
		if e != nil {
			return nil, e
		}
		out = wire.AppendBytes(out, 4, bundle)
	}
	st.Records[recKey(a.UID, 0, 0, a.Family)] = ledger
	return out, nil
}

func rhythmRecordWire(r record) []byte {
	v := wire.AppendVarint(nil, 1, r.Stage)
	v = wire.AppendVarint(v, 2, r.Mode)
	v = wire.AppendVarint(v, 3, r.Best)
	v = wire.AppendVarint(v, 4, r.BestGrade)
	return wire.AppendVarint(v, 5, r.BestCombo)
}

func (s *Service) endRhythm(ctx command.Context, req []byte, a Run, st *snapshot, key string) ([]byte, error) {
	score, err := s.endScore("Rhythm", req, a)
	if err != nil {
		return nil, err
	}
	grade, combo := num(req, 2), num(req, 6)
	if grade > 6 || combo > 2 {
		return nil, fmt.Errorf("eventplay: invalid rhythm grade/combo")
	}
	counts := map[uint64]uint64{}
	err = wire.Walk(req, func(f wire.Field) error {
		if f.Number != 3 {
			return nil
		}
		if f.Type != 2 {
			return wire.ErrMalformed
		}
		typ, count := num(f.Value, 1), num(f.Value, 2)
		if typ < 1 || typ > 4 || counts[typ] != 0 {
			return fmt.Errorf("eventplay: invalid rhythm judgment")
		}
		counts[typ] = count
		return nil
	})
	if err != nil {
		return nil, err
	}
	if combo > 0 && (num(req, 7) > 0 || counts[4] > 0) || combo == 2 && counts[2]+counts[3] > 0 {
		return nil, fmt.Errorf("eventplay: rhythm combo contradicts judgments")
	}
	if s.arcadeProfile == nil {
		return nil, fmt.Errorf("eventplay: rhythm account profile unavailable")
	}
	profile, err := s.arcadeProfile(ctx)
	if err != nil {
		return nil, err
	}
	seed := profile.Owner + score + num(req, 1)
	for typ, count := range counts {
		seed += typ * count
	}
	random := util.NewWell512(seed)
	checksum := fmt.Sprintf("%d-%d-%d-%d", 11111+random.Next()%88888, 11111+random.Next()%88888, 11111+random.Next()%88888, 11111+random.Next()%88888)
	checksumWire, _, err := wire.Bytes(req, 5)
	if err != nil {
		return nil, err
	}
	if string(checksumWire) != checksum {
		return nil, fmt.Errorf("eventplay: rhythm game checksum mismatch")
	}
	r := playRecord(st, a.UID, a.Game, a.Stage, a.Mode, a.Family)
	if score > r.Best {
		r.Best = score
		r.AchievedAt = uint64(s.now().UnixMilli())
	}
	if grade > 0 && (r.BestGrade == 0 || grade < r.BestGrade) {
		r.BestGrade = grade
	}
	if combo > r.BestCombo {
		r.BestCombo = combo
	}
	st.Records[recKey(a.UID, a.Stage, a.Mode, a.Family)] = r
	if s.onRhythmResult != nil && grade > 0 && num(req, 7) == 0 {
		result := events.RhythmResult{Music: a.Stage, Difficulty: a.Mode, Score: score, Grade: grade, Combo: combo, Judgments: counts}
		if err := s.onRhythmResult(ctx, result); err != nil {
			return nil, err
		}
	}
	return wire.AppendBytes(nil, 1, rhythmRecordWire(r)), nil
}

func sortedRecordKeys(st *snapshot) []string {
	keys := make([]string, 0, len(st.Records))
	for key := range st.Records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
