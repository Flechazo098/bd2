// Package battle implements a deterministic local battle session. The client
// remains authoritative for turn simulation; the server validates sequencing
// and echoes the submitted combat state without capture replay.
package battle

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
)

type Service struct {
	mu              sync.Mutex
	states          map[string]*battleState
	activeSession   string
	gameDataRoot    string
	gameDataVersion string
	inventory       *player.Inventory
	currentPack     func() (int, error)
	loadRewards     func(string, string, int, uint64) ([]gamedata.BattleReward, error)
	buffs           func() ([]gamedata.PictorialBuffStat, error)
	onTutorialWin   func() error
}

type battleState struct {
	entered     bool
	index       uint64
	round       uint64
	monster     uint64
	deck        uint64
	pack        int
	initialBlue [][]byte
}

// BeginSession discards an unfinished battle when LoginUser creates a new
// game session. Persistent rewards are written only by a successful BattleEnd
// request transaction, so reconnect returns to the last pre-battle commit.
func (s *Service) BeginSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		return
	}
	if s.states == nil {
		s.states = make(map[string]*battleState)
	}
	if s.states[id] == nil {
		if len(s.states) >= 1024 {
			for key := range s.states {
				if key != id {
					delete(s.states, key)
					break
				}
			}
		}
		s.states[id] = &battleState{}
	}
	s.activeSession = id
}

func (s *Service) AttachTutorialWin(callback func() error) { s.onTutorialWin = callback }

func NewService(gameDataRoot, gameDataVersion string, inventory *player.Inventory, currentPack func() (int, error)) *Service {
	return &Service{
		gameDataRoot: gameDataRoot, gameDataVersion: gameDataVersion,
		inventory: inventory, currentPack: currentPack, loadRewards: gamedata.BattleDeckRewards,
		states: make(map[string]*battleState),
	}
}

func (s *Service) AttachPictorialBuffs(buffs func() ([]gamedata.PictorialBuffStat, error)) {
	s.buffs = buffs
}

func checkSeq(request []byte) error {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return errors.New("battle: invalid request sequence")
	}
	return nil
}

func (s *Service) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/BattleEnter" && path != "/BattleStart" && path != "/BattleRetry" && path != "/BattleVerifyState" && path != "/BattleEnd" && path != "/BattleExit" {
		return 0, nil, false, nil
	}
	if err := checkSeq(request); err != nil {
		return 0, nil, true, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.stateLocked()
	switch path {
	case "/BattleVerifyState":
		if !state.entered {
			return 0, nil, true, errors.New("battle: verify before enter")
		}
		// Packet code 142 follows BattleVerify(141). State 3 is the protocol's
		// explicit SUCCESS value; PVE does not require authoritative team lists.
		return 142, wire.AppendVarint(nil, 1, 3), true, nil
	case "/BattleEnter":
		deck, deckFound, err := wire.Varint(request, 4)
		if err != nil || !deckFound || deck == 0 {
			return 0, nil, true, errors.New("battle: missing battle deck")
		}
		mode, modeFound, err := wire.Varint(request, 5)
		if err != nil || !modeFound || mode == 0 {
			return 0, nil, true, errors.New("battle: missing battle mode")
		}
		packID := 0
		if s.currentPack != nil {
			packID, err = s.currentPack()
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: resolve current pack: %w", err)
			}
			if packID <= 0 {
				return 0, nil, true, errors.New("battle: current pack is invalid")
			}
		} else if s.inventory != nil && s.gameDataRoot != "" {
			return 0, nil, true, errors.New("battle: current pack resolver is unavailable")
		}
		monster, _, _ := wire.Varint(request, 3)
		response := wire.AppendVarint(nil, 2, deck)
		if s.buffs != nil {
			buffs, err := s.buffs()
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: account pictorial buffs: %w", err)
			}
			for _, buff := range buffs {
				entry := wire.AppendVarint(nil, 1, buff.StatType)
				entry = wire.AppendDouble(entry, 2, buff.Value)
				if buff.Category != 0 {
					entry = wire.AppendVarint(entry, 3, buff.Category)
				}
				response = wire.AppendBytes(response, 4, entry)
			}
		}
		// The local engine is the normal deterministic engine.
		response = wire.AppendVarint(response, 6, 1)
		state.entered, state.index, state.round, state.initialBlue = true, 0, 0, nil
		state.monster, state.deck, state.pack = monster, deck, packID
		slog.Info("team trace: battle entered", "pack", packID, "monster", monster, "enemyDeck", deck, "mode", mode)
		return 52, response, true, nil
	case "/BattleRetry":
		if !state.entered {
			return 0, nil, true, errors.New("battle: retry before enter")
		}
		index, found, err := wire.Varint(request, 2)
		if err != nil || !found || index == 0 {
			return 0, nil, true, errors.New("battle: retry missing battle index")
		}
		if len(state.initialBlue) == 0 {
			return 0, nil, true, errors.New("battle: retry before initial battle state")
		}
		var response []byte
		for _, character := range state.initialBlue {
			response = wire.AppendBytes(response, 2, character)
		}
		response = wire.AppendVarint(response, 3, index)
		state.index, state.round = index, 0
		return 58, response, true, nil
	case "/BattleStart":
		if !state.entered {
			return 0, nil, true, errors.New("battle: start before enter")
		}
		index, found, err := wire.Varint(request, 2)
		if err != nil || !found || index == 0 {
			return 0, nil, true, errors.New("battle: invalid battle index")
		}
		if state.index != 0 && index != state.index {
			return 0, nil, true, fmt.Errorf("battle: index changed from %d to %d", state.index, index)
		}
		state.index, state.round = index, state.round+1
		if state.round == 1 {
			state.initialBlue = nil
		}
		var response []byte
		err = wire.Walk(request, func(field wire.Field) error {
			if field.Type != 2 {
				return nil
			}
			if field.Number == 4 {
				response = wire.AppendBytes(response, 1, field.Value)
			} else if field.Number == 5 {
				response = wire.AppendBytes(response, 2, field.Value)
				if state.round == 1 {
					state.initialBlue = append(state.initialBlue, append([]byte(nil), field.Value...))
				}
			}
			return nil
		})
		if err != nil {
			return 0, nil, true, err
		}
		// Stable per-battle/round seed; reproducible across retries.
		seed := index*7919 + state.round*104729
		response = wire.AppendVarint(response, 3, seed)
		return 14, response, true, nil
	case "/BattleEnd":
		if !state.entered {
			return 0, nil, true, errors.New("battle: end before enter")
		}
		result, found, err := wire.Varint(request, 2)
		if err != nil || !found || result == 0 {
			return 0, nil, true, errors.New("battle: invalid result")
		}
		response := wire.AppendVarint(nil, 1, result)
		err = wire.Walk(request, func(field wire.Field) error {
			if field.Number == 3 && field.Type == 2 {
				response = wire.AppendBytes(response, 3, field.Value)
			}
			return nil
		})
		if err != nil {
			return 0, nil, true, err
		}
		rewardBundle := false
		if result == 1 && s.inventory != nil && state.monster != 0 && s.gameDataRoot != "" {
			if state.pack <= 0 {
				return 0, nil, true, errors.New("battle: victory has no locked pack")
			}
			loader := s.loadRewards
			if loader == nil {
				loader = gamedata.BattleDeckRewards
			}
			rewards, rewardErr := loader(s.gameDataRoot, s.gameDataVersion, state.pack, state.deck)
			if rewardErr != nil {
				return 0, nil, true, fmt.Errorf("battle: pack %d monster %d deck %d rewards: %w", state.pack, state.monster, state.deck, rewardErr)
			}
			items, grantErr := s.inventory.GrantOnce(fmt.Sprintf("pack%d:monster%d:deck%d", state.pack, state.monster, state.deck), rewards)
			if grantErr != nil {
				return 0, nil, true, grantErr
			}
			var bundle []byte
			for _, item := range items {
				bundle = wire.AppendBytes(bundle, 1, player.ItemWire(item))
			}
			if len(bundle) != 0 {
				response = wire.AppendBytes(response, 5, bundle)
				rewardBundle = true
			}
		}
		if result == 1 && s.onTutorialWin != nil {
			if err := s.onTutorialWin(); err != nil {
				return 0, nil, true, fmt.Errorf("battle: update tutorial kill mission: %w", err)
			}
		}
		for _, field := range []int{5, 6, 7, 8, 10, 11, 15, 16, 25} {
			if field == 5 && rewardBundle {
				continue
			}
			response = wire.AppendBytes(response, field, nil)
		}
		state.entered, state.deck, state.pack, state.initialBlue = false, 0, 0, nil
		return 15, response, true, nil
	case "/BattleExit":
		state.entered, state.index, state.round, state.deck, state.pack, state.initialBlue = false, 0, 0, 0, 0, nil
		return 388, nil, true, nil
	}
	panic("unreachable")
}

func (s *Service) stateLocked() *battleState {
	if s.states == nil {
		s.states = make(map[string]*battleState)
	}
	key := s.activeSession
	if key == "" {
		key = "__direct_test__"
	}
	state := s.states[key]
	if state == nil {
		state = &battleState{}
		s.states[key] = state
	}
	return state
}
