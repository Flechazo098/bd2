package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"bytes"
	"fmt"
)

func (s *Service) AttachOverwhelmAuthorization(f func(string, uint64) error) {
	s.overwhelmAuthorize = f
}
func (s *Service) AttachOverwhelmHunting(h interface {
	ValidateBattle(int, uint64, uint64, uint64) error
	CompleteBattle(int, uint64, uint64, uint64, string) ([]byte, [][]byte, error)
}) {
	s.overwhelmHunting = h
}
func nestedMessages(b []byte, number int) ([][]byte, error) {
	var out [][]byte
	e := wire.Walk(b, func(f wire.Field) error {
		if f.Number == number {
			if f.Type != 2 {
				return ErrInvalidRequest
			}
			out = append(out, append([]byte(nil), f.Value...))
		}
		return nil
	})
	return out, e
}

type overwhelmedMonster struct {
	ID, Group, Deck, Mode uint64
	Definition            gamedata.FieldMonsterDesign
	Instance              string
	Costs                 []gamedata.Reward
}

func (s *Service) AttachOverwhelmDesign(root, version string) error {
	rows, e := gamedata.LoadSkyWayOverwhelm(root, version)
	if e != nil {
		return e
	}
	s.overwhelmSky = rows
	s.overwhelmQuest = func(pack, quest int) (gamedata.OverwhelmQuestRule, error) {
		return gamedata.LoadOverwhelmQuest(root, version, pack, quest)
	}
	return nil
}

func (s *Service) handleOverwhelm(request []byte) (int, []byte, bool, error) {
	seq, present, e := wire.Varint(request, 1)
	if e != nil || !present || seq == 0 || seq > 0x7fffffff || s.monsterSession == "" {
		return 275, nil, true, ErrInvalidRequest
	}
	pack, e := s.CurrentPackID()
	if e != nil {
		return 275, nil, true, e
	}
	v, e := s.loadMonsterState()
	if e != nil {
		return 275, nil, true, e
	}
	identity := fmt.Sprintf("overwhelm:%s:%d", s.monsterSession, seq)
	if prior, ok := v.Requests[identity]; ok {
		if !bytes.Equal(prior.Request, request) {
			return 275, nil, true, ErrInvalidRequest
		}
		return 275, prior.Response, true, nil
	}
	raw, e := nestedMessages(request, 2)
	if e != nil || len(raw) == 0 || len(raw) > 4096 {
		return 275, nil, true, ErrInvalidRequest
	}
	var targets []overwhelmedMonster
	seen := map[uint64]bool{}
	for _, b := range raw {
		var m overwhelmedMonster
		for f, dst := range map[int]*uint64{1: &m.Group, 2: &m.ID, 3: &m.Deck, 4: &m.Mode} {
			*dst, _, e = wire.Varint(b, f)
			if e != nil || *dst > 0x7fffffff {
				return 275, nil, true, ErrInvalidRequest
			}
		}
		if m.ID == 0 || seen[m.ID] {
			return 275, nil, true, ErrInvalidRequest
		}
		seen[m.ID] = true
		if m.Mode == 5 {
			definition, found, x := s.findFieldMonster(pack, int(m.ID))
			if x != nil || !found || definition.UseBattleSkip != 1 || definition.Type >= 2 {
				return 275, nil, true, fmt.Errorf("world: hunting monster cannot be overwhelmed")
			}
			if s.overwhelmHunting == nil {
				return 275, nil, true, fmt.Errorf("world: hunting overwhelm runtime missing")
			}
			if e = s.overwhelmHunting.ValidateBattle(pack, 5, m.ID, m.Deck); e != nil {
				return 275, nil, true, e
			}
		} else {
			if m.Mode != 1 && m.Mode != 2 && m.Mode != 4 && (m.Mode < 9 || m.Mode > 15) {
				return 275, nil, true, fmt.Errorf("world: unavailable overwhelm battle mode %d", m.Mode)
			}
			definition, found, e := s.findFieldMonster(pack, int(m.ID))
			if e != nil || !found || (definition.UseBattleSkip != 1 && definition.Type != 3) {
				return 275, nil, true, ErrInvalidRequest
			}
			m.Definition = definition
			if definition.Type == 2 || definition.Type == 4 {
				return 275, nil, true, fmt.Errorf("world: private field monster cannot be overwhelmed")
			}
			if e = s.authorizeMonsterMap(pack, int(m.ID)); e != nil {
				return 275, nil, true, e
			}
			validDeck := definition.Type == 3 && m.Deck == 0
			for _, d := range definition.BattleDecks {
				if d == m.Deck {
					validDeck = true
				}
			}
			if definition.BattleDeck == m.Deck {
				validDeck = true
			}
			if !validDeck {
				return 275, nil, true, fmt.Errorf("world: overwhelm deck mismatch")
			}
			if m.Mode >= 9 && m.Mode <= 15 {
				mapID, e := s.currentFieldMap(pack)
				if e != nil {
					return 275, nil, true, e
				}
				found := false
				for _, rule := range s.overwhelmSky {
					if rule.Map != uint64(mapID) || rule.Group+8 != m.Mode {
						continue
					}
					cost := uint64(0)
					if rule.Boss == m.ID {
						found = true
						cost = rule.BossAP
					} else {
						for i, id := range rule.Monsters {
							if id == m.ID {
								found = true
								cost = rule.AP[i]
							}
						}
					}
					if found {
						typ := uint64(21)
						if rule.APType == 2 {
							typ = 23
						}
						if cost > 0 {
							m.Costs = []gamedata.Reward{{Type: typ, Count: cost}}
						}
						break
					}
				}
				if !found {
					return 275, nil, true, fmt.Errorf("world: skyway monster does not match current dungeon")
				}
				m.Instance = fmt.Sprintf("skyway:%s:%d", identity, m.ID)
			} else if m.Mode == 2 {
				if int(m.Group) != definition.GroupID || definition.GroupID == 0 || !s.monsterEligible(pack, definition) {
					return 275, nil, true, ErrInvalidRequest
				}
				state, e := s.monsterState(&v, pack, definition)
				if e != nil {
					return 275, nil, true, e
				}
				if state.Defeated || state.Respawn > s.monsterTime().UnixMilli() {
					return 275, nil, true, fmt.Errorf("world: overwhelm monster not spawned")
				}
				m.Instance = fmt.Sprintf("fieldmonster:%s:%d", monsterKey(pack, s.questDifficulty(pack), definition.ID), state.Generation)
			} else {
				if m.Group != 0 {
					return 275, nil, true, ErrInvalidRequest
				}
				m.Instance = fmt.Sprintf("questmonster:%d:%d:%d", pack, s.questDifficulty(pack), m.ID)
			}
		}
		targets = append(targets, m)
	}
	quests, e := nestedMessages(request, 3)
	if e != nil {
		return 275, nil, true, e
	}
	var updates [][]byte
	var updated []uint64
	for _, b := range quests {
		quest, _, e := wire.Varint(b, 1)
		if e != nil {
			return 275, nil, true, e
		}
		qp, _, e := wire.Varint(b, 2)
		if e != nil || int(qp) != pack || quest == 0 || !s.canClear(pack, int(quest)) {
			return 275, nil, true, ErrInvalidRequest
		}
		values, e := intsRequest(b, 3)
		if e != nil || len(values) == 0 {
			return 275, nil, true, ErrInvalidRequest
		}
		if e = s.validateOverwhelmQuest(pack, int(quest), values, targets); e != nil {
			return 275, nil, true, e
		}
		req := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, seq), 2, quest), 3, qp)
		for _, value := range values {
			if value > 0x7fffffff {
				return 275, nil, true, ErrInvalidRequest
			}
			req = wire.AppendVarint(req, 4, value)
		}
		updates = append(updates, req)
		updated = append(updated, quest)
	}
	if s.overwhelmAuthorize == nil {
		return 275, nil, true, fmt.Errorf("world: overwhelm requires successful talent use")
	}
	if e = s.overwhelmAuthorize(identity, uint64(len(targets))); e != nil {
		return 275, nil, true, e
	}
	var response, bundle []byte
	for _, m := range targets {
		if m.Mode == 5 {
			reward, monsters, e := s.overwhelmHunting.CompleteBattle(pack, 5, m.ID, m.Deck, fmt.Sprintf("%s:%d", identity, m.ID))
			if e != nil {
				return 275, nil, true, e
			}
			bundle = append(bundle, reward...)
			for _, row := range monsters {
				response = wire.AppendBytes(response, 1, row)
			}
		} else {
			if !v.Claims[m.Instance] && m.Definition.Type != 3 {
				definition := m.Definition
				definition.BattleDeck = m.Deck
				var reward []byte
				var e error
				if len(m.Costs) > 0 {
					if s.monsterRewards == nil || s.researchEconomy == nil {
						return 275, nil, true, ErrInvalidRequest
					}
					rs, x := s.monsterRewards(pack, m.Deck)
					if x != nil {
						return 275, nil, true, x
					}
					rewards := make([]gamedata.Reward, 0, len(rs))
					for _, r := range rs {
						rewards = append(rewards, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count})
					}
					reward, e = s.researchEconomy.Apply(m.Instance, m.Costs, rewards)
				} else {
					reward, e = s.grantFieldMonster(pack, definition, m.Instance)
				}
				if e != nil {
					return 275, nil, true, e
				}
				bundle = append(bundle, reward...)
			}
			v.Claims[m.Instance] = true
			if m.Definition.GroupID > 0 {
				state, e := s.monsterState(&v, pack, m.Definition)
				if e != nil {
					return 275, nil, true, e
				}
				state.Defeated = true
				state.Respawn = s.nextMonsterSpawn(m.Definition)
				v.Monsters[monsterKey(pack, s.questDifficulty(pack), int(m.ID))] = state
				response = wire.AppendBytes(response, 1, monsterWire(m.Definition, state, true))
			}
		}
	}
	for i, req := range updates {
		if _, _, _, e = s.handleQuestUpdate(req); e != nil {
			return 275, nil, true, e
		}
		response = wire.AppendVarint(response, 2, updated[i])
	}
	response = wire.AppendBytes(response, 3, bundle)
	v.Requests[identity] = fieldMonsterReply{Request: append([]byte(nil), request...), Response: response}
	if e = s.saveMonsterState(v); e != nil {
		return 275, nil, true, e
	}
	return 275, response, true, nil
}
func (s *Service) validateOverwhelmQuest(pack, quest int, values []uint64, targets []overwhelmedMonster) error {
	if s.overwhelmQuest == nil {
		return fmt.Errorf("world: overwhelm quest rules missing")
	}
	r, e := s.overwhelmQuest(pack, quest)
	if e != nil {
		return e
	}
	mapID, e := s.currentFieldMap(pack)
	if e != nil {
		return e
	}
	gain := uint64(0)
	for _, m := range targets {
		switch r.Type {
		case 3:
			for _, id := range r.Targets {
				if id == m.ID {
					gain++
				}
			}
		case 1:
			for _, enemy := range r.Enemies[m.Deck] {
				for _, id := range r.Targets {
					if enemy == id {
						gain++
					}
				}
			}
		case 8:
			if len(r.Targets) > 0 && r.Targets[0] == uint64(mapID) {
				gain += uint64(len(r.Enemies[m.Deck]))
			}
		}
	}
	old := uint64(0)
	if p, ok := s.state.QuestInPack(quest, pack, s.questDifficultyFor(pack, quest)); ok && len(p.Values) > 0 {
		old = uint64(p.Values[0])
	}
	if gain == 0 || len(values) != 1 || values[0] != old+gain {
		return fmt.Errorf("world: quest does not match overwhelmed monsters")
	}
	return nil
}
