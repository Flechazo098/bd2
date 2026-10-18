package castle

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"fmt"
	"math"
	"slices"
)

func (s *Service) HandlesBattle(mode uint64) bool {
	return mode == 7 || mode == 16 || mode == 19 || mode == 20
}
func (s *Service) EnterBattle(ctx command.Context, req []byte, identity string) ([]byte, error) {
	v, season, active, e := s.load(ctx)
	if e != nil {
		return nil, e
	}
	mode, deck := n(req, 5), n(req, 4)
	if !s.HandlesBattle(mode) || identity == "" {
		return nil, fmt.Errorf("castle: invalid castle encounter")
	}
	if s.currentPack == nil {
		return nil, fmt.Errorf("castle: current pack unavailable")
	}
	pack, e := s.currentPack(ctx)
	if e != nil {
		return nil, e
	}
	if uint64(pack) != s.design.PackID {
		return nil, fmt.Errorf("castle: not in castle pack")
	}
	a := &encounter{Mode: mode, Deck: deck, Monster: n(req, 3), Receipt: identity, Season: season}
	if mode == 7 {
		if !active || v.Run == nil {
			return nil, fmt.Errorf("castle: no active pride floor")
		}
		f, ok := s.design.Floors[v.Run.Floor]
		if !ok || f.Monsters[a.Monster] != deck || v.Run.Cleared[a.Monster] {
			return nil, fmt.Errorf("castle: invalid pride monster/deck")
		}
		if a.Monster == f.Boss {
			for monster := range f.Monsters {
				if monster != f.Boss && !v.Run.Cleared[monster] {
					return nil, fmt.Errorf("castle: pride boss locked")
				}
			}
		}
		a.ID = f.ID
	} else {
		id := n(req, 9)
		t, ok := s.design.Towers[[2]uint64{mode, id}]
		if !ok || t.Deck != deck {
			return nil, fmt.Errorf("castle: invalid tower/deck")
		}
		if t.Previous != 0 {
			if _, ok = v.Towers[towerKey(mode, t.Previous)]; !ok {
				return nil, fmt.Errorf("castle: tower floor locked")
			}
		}
		a.ID = id
	}
	v.Encounter = a
	return nil, save(ctx, v)
}

func (s *Service) AbandonBattle(ctx command.Context) error {
	v, _, _, e := s.load(ctx)
	if e != nil {
		return e
	}
	v.Encounter = nil
	return save(ctx, v)
}

func (s *Service) CompleteBattle(ctx command.Context, req []byte, identity string) ([]byte, error) {
	v, season, active, e := s.load(ctx)
	if e != nil {
		return nil, e
	}
	a := v.Encounter
	if a == nil || a.Receipt != identity || s.evidence == nil {
		return nil, fmt.Errorf("castle: settlement has no castle encounter")
	}
	proof := s.evidence(ctx)
	if proof.Receipt != identity || proof.Mode != a.Mode || proof.Deck != a.Deck || proof.Monster != a.Monster || proof.Round == 0 {
		return nil, fmt.Errorf("castle: settlement has no started battle")
	}
	key := fmt.Sprintf("battle:%s:%d", identity, proof.Attempt)
	if out, ok, e := loadReceipt(ctx, key, req); e != nil || ok {
		return out, e
	}
	win := n(req, 2) == 1
	var out []byte
	if win {
		deck, ok := s.design.Decks[a.Deck]
		if !ok {
			return nil, fmt.Errorf("castle: missing deck design")
		}
		final, e := msgs(req, 3)
		if e != nil {
			return nil, e
		}
		alive := map[uint64]bool{}
		participants := map[uint64]uint64{}
		finalSeen := map[uint64]bool{}
		var party []Participant
		for _, raw := range proof.InitialBlue {
			index, id := n(raw, 2), n(raw, 3)
			if index == 0 || id == 0 || participants[index] != 0 {
				return nil, fmt.Errorf("castle: invalid initial party")
			}
			participants[index] = id
			party = append(party, Participant{index, n(raw, 6), id, n(raw, 7)})
		}
		if len(participants) == 0 || len(participants) > 5 {
			return nil, fmt.Errorf("castle: invalid castle party size")
		}
		if s.validate == nil {
			return nil, fmt.Errorf("castle: ownership validation unavailable")
		}
		if e = s.validate(ctx, party); e != nil {
			return nil, e
		}
		for _, raw := range final {
			index := n(raw, 1)
			if _, ok := participants[index]; !ok || finalSeen[index] || n(raw, 2) != participants[index] || n(raw, 3) > math.MaxInt64 {
				return nil, fmt.Errorf("castle: invalid settlement party")
			}
			alive[index] = n(raw, 3) > 0
			finalSeen[index] = true
		}
		if len(final) != len(participants) {
			return nil, fmt.Errorf("castle: missing settlement party")
		}
		definitions := make([]gamedata.EventBattleChallenge, len(deck.Challenges))
		for i, c := range deck.Challenges {
			definitions[i] = gamedata.EventBattleChallenge{Type: c.Type, Value1: c.Value1, Value2: c.Value2}
		}
		indexes, e := gamedata.VerifySubmittedChallenges(req, definitions)
		if e != nil {
			return nil, e
		}
		var bonus uint64
		for _, index := range indexes {
			c := deck.Challenges[index]
			satisfied := true
			switch c.Type {
			case 1, 3:
				satisfied = false
				for id, char := range participants {
					if s.design.Characters[char].Unique == c.Value1 && (c.Type == 1 || alive[id]) {
						satisfied = true
					}
				}
			case 2:
				satisfied = proof.Round <= c.Value1
			case 4, 5:
				count := uint64(0)
				for _, char := range participants {
					if s.design.Characters[char].Element == c.Value1 {
						count++
					}
				}
				satisfied = (c.Type == 4 && count >= c.Value2) || (c.Type == 5 && count <= c.Value2)
			case 7:
				dead := uint64(0)
				for id := range participants {
					if !alive[id] {
						dead++
					}
				}
				satisfied = dead < c.Value1
			}
			if !satisfied {
				return nil, fmt.Errorf("castle: contradicted challenge claim")
			}
			if bonus > math.MaxInt32-c.Score {
				return nil, fmt.Errorf("castle: challenge score overflow")
			}
			bonus += c.Score
		}
		if a.Mode == 7 {
			if !active || a.Season != season || v.Run == nil || v.Run.Floor != a.ID {
				return nil, fmt.Errorf("castle: pride season/run changed")
			}
			f := s.design.Floors[a.ID]
			survived := uint64(0)
			for _, live := range alive {
				if live {
					survived++
				}
			}
			sc := f.Scoring
			// GetEvaluationPoint uses single-precision intermediate truncation.
			evaluation := uint64(float32(1)/float32(proof.Round+9)*10*float32(sc.Turn)) + uint64(float32(survived)/5*float32(sc.Survival)) + uint64(float32(1)/float32(v.Run.Retry+proof.Attempt+1)*float32(sc.Retry))
			if a.Monster == f.Boss {
				evaluation = uint64(math.RoundToEven(float64(evaluation) * math.Trunc(sc.BossMultiplier*100) / 100))
			}
			points := bonus + sc.Base + evaluation
			if points > math.MaxInt32 || v.Run.Point > math.MaxInt32-points {
				return nil, fmt.Errorf("castle: floor score overflow")
			}
			v.Run.Point += points
			v.Run.Cleared[a.Monster] = true
			progress := v.Floors[a.ID]
			progress.ID = a.ID
			progress.Point = v.Run.Point
			progress.Retry = v.Run.Retry
			if a.Monster == f.Boss {
				if progress.Point > progress.Highest {
					progress.Highest = progress.Point
					progress.Achieved = uint64(s.now().UnixMilli())
				}
				v.Run = nil
			}
			v.Floors[a.ID] = progress
			out = wire.AppendBytes(out, 10, floorWire(progress))
			out = wire.AppendBytes(out, 11, totalWire(v))
			out = wire.AppendVarint(out, 12, evaluation)
			monster := wire.AppendVarint(nil, 1, a.Monster)
			monster = wire.AppendVarint(monster, 2, a.Deck)
			out = wire.AppendBytes(out, 4, monster)
		} else {
			key := towerKey(a.Mode, a.ID)
			previous := v.Towers[key]
			current := previous
			current.ID, current.Mode = a.ID, a.Mode
			var rewards []gamedata.Reward
			if previous.ID == 0 {
				rewards = append(rewards, deck.Rewards...)
			}
			for _, index := range indexes {
				if !slices.Contains(current.Challenges, index) {
					current.Challenges = append(current.Challenges, index)
					r := deck.Challenges[index].Reward
					if r.Type != 0 && r.Count != 0 {
						rewards = append(rewards, r)
					}
				}
			}
			slices.Sort(current.Challenges)
			if len(rewards) > 0 {
				if s.grant == nil {
					return nil, fmt.Errorf("castle: reward grant unavailable")
				}
				bundle, e := s.grant(ctx, "castle:tower:"+key+":"+identity, rewards)
				if e != nil {
					return nil, e
				}
				out = wire.AppendBytes(out, 5, bundle)
			}
			v.Towers[key] = current
			out = wire.AppendBytes(out, 15, towerWire(current))
		}
	}
	v.Encounter = nil
	if e = save(ctx, v); e != nil {
		return nil, e
	}
	if e = putReceipt(ctx, key, req, out); e != nil {
		return nil, e
	}
	return out, nil
}
