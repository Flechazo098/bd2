// Package battle implements a deterministic local battle session. The client
// remains authoritative for turn simulation; the server validates sequencing
// and echoes the submitted combat state without capture replay.
package battle

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
)

type Service struct {
	mu                 sync.Mutex
	states             map[string]*battleState
	activeSession      string
	gameDataRoot       string
	gameDataVersion    string
	inventory          *player.Inventory
	currentPack        func() (int, error)
	currentDifficulty  func() (uint64, error)
	loadDifficultyDeck func(string, string, int, uint64, uint64) (uint64, error)
	loadRewards        func(string, string, int, uint64) ([]gamedata.BattleReward, error)
	loadPhases         func(string, string, int, uint64, uint64) ([]gamedata.BattlePhase, error)
	buffs              func() ([]gamedata.PictorialBuffStat, error)
	onTutorialWin      func() error
	onMonsterWin       func() error
	commitHealth       func(map[uint64]uint64) error
	hunting            HuntingRuntime
	monsterHunt        MonsterHuntRuntime
	eventBattles       []EventBattleRuntime
	fieldMonsters      FieldMonsterRuntime
	consumeFieldBuff   func(string) error
}
type FieldMonsterRuntime interface {
	BeginFieldMonsterBattle(int, uint64, uint64) (string, bool, error)
	CompleteFieldMonsterBattle(int, uint64, string) ([]byte, error)
}

func (s *Service) AttachFieldMonsters(runtime FieldMonsterRuntime) { s.fieldMonsters = runtime }

func (s *Service) AttachFieldBuffConsume(consume func(string) error) { s.consumeFieldBuff = consume }

// EventBattleRuntime owns event stage eligibility, costs and settlement while
// the normal battle service transports the client's turn simulation.
type EventBattleRuntime interface {
	HandlesBattle(mode uint64) bool
	EnterBattle(request []byte, receipt string) ([]byte, error)
	CompleteBattle(request []byte, receipt string) ([]byte, error)
}

func (s *Service) AttachEventBattle(runtime EventBattleRuntime) {
	if runtime != nil {
		s.eventBattles = append(s.eventBattles, runtime)
	}
}

func (s *Service) eventBattle(mode uint64) EventBattleRuntime {
	for _, runtime := range s.eventBattles {
		if runtime.HandlesBattle(mode) {
			return runtime
		}
	}
	return nil
}

type MonsterHuntRuntime interface {
	EnterBattle(request []byte, receipt string) ([]byte, error)
	CompleteBattle(request []byte, receipt string) ([]byte, error)
}

func (s *Service) AttachMonsterHunt(runtime MonsterHuntRuntime) {
	s.monsterHunt = runtime
}

func isMonsterHunt(mode uint64) bool { return mode == 8 || mode == 24 }

// HuntingRuntime validates the active hunting ground and settles each won
// encounter with the account's persistent AP, monsters and reward ledger.
type HuntingRuntime interface {
	ValidateBattle(pack int, mode, monster, deck uint64) error
	CompleteBattle(pack int, mode, monster, deck uint64, receipt string) ([]byte, [][]byte, error)
}

const huntingGroundMode = 5

func (s *Service) AttachHunting(runtime HuntingRuntime) {
	s.hunting = runtime
}

type battleState struct {
	entered       bool
	index         uint64
	round         uint64
	monster       uint64
	deck          uint64
	pack          int
	mode          uint64
	enterReceipt  string
	fieldInstance string
	initialBlue   [][]byte
	phases        []gamedata.BattlePhase
	phase         int
	phaseStarted  bool
	phaseSeq      uint64
	phaseReply    []byte
	endSeq        uint64
	endRequest    []byte
	endReply      []byte
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

func (s *Service) AttachMonsterWinMission(callback func() error) { s.onMonsterWin = callback }

func (s *Service) AttachTutorialWin(callback func() error) { s.onTutorialWin = callback }

// AttachCommittedHealth persists only completed battle results. Round state
// remains transient, so reconnect rolls back an unfinished battle.
// The callback may normalize values to field HP; the response uses those
// committed values rather than echoing battle-only HP buffs.
func (s *Service) AttachCommittedHealth(callback func(map[uint64]uint64) error) {
	s.commitHealth = callback
}

func (s *Service) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked().entered
}

func NewService(gameDataRoot, gameDataVersion string, inventory *player.Inventory, currentPack func() (int, error)) *Service {
	return &Service{
		gameDataRoot: gameDataRoot, gameDataVersion: gameDataVersion,
		inventory: inventory, currentPack: currentPack, loadRewards: gamedata.BattleDeckRewards,
		states: make(map[string]*battleState),
	}
}

// AttachCurrentDifficulty selects the GameData quest deck for the active pack.
func (s *Service) AttachCurrentDifficulty(resolve func() (uint64, error)) {
	s.currentDifficulty = resolve
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
	if path != "/BattleEnter" && path != "/BattleStart" && path != "/BattleRetry" && path != "/BattleVerifyState" && path != "/BattleEnd" && path != "/BattleExit" && path != "/BattlePhaseChange" {
		return 0, nil, false, nil
	}
	if err := checkSeq(request); err != nil {
		return 0, nil, true, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.stateLocked()
	switch path {
	case "/BattlePhaseChange":
		if !state.entered {
			return 0, nil, true, errors.New("battle: phase change before enter")
		}
		seq, _, _ := wire.Varint(request, 1)
		if seq == state.phaseSeq && state.phaseReply != nil {
			return 632, append([]byte(nil), state.phaseReply...), true, nil
		}
		if seq <= state.phaseSeq {
			return 0, nil, true, errors.New("battle: stale phase change sequence")
		}
		if len(state.phases) == 0 || state.phase+1 >= len(state.phases) {
			return 0, nil, true, errors.New("battle: no next phase")
		}
		if !state.phaseStarted {
			return 0, nil, true, errors.New("battle: phase change before current phase start")
		}
		next := state.phases[state.phase+1]
		response := wire.AppendVarint(nil, 1, next.GroupID)
		response = wire.AppendVarint(response, 2, next.ID)
		response = wire.AppendVarint(response, 8, next.DeckID)
		// The local engine does not simulate combat. With verification disabled
		// and no battle_result the client explicitly preserves its blue team;
		// fabricating a verified result would overwrite HP, SP and action state.
		// Dynamic official turn/SP values are not available in this request.
		state.phase++
		state.index, state.deck, state.phaseStarted = next.DeckID, next.DeckID, false
		state.phaseSeq, state.phaseReply = seq, append([]byte(nil), response...)
		slog.Info("team trace: battle phase changed", "pack", state.pack, "monster", state.monster, "group", next.GroupID, "phase", next.ID, "enemyDeck", next.DeckID)
		return 632, response, true, nil
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
		var huntResponse []byte
		eventRuntime := s.eventBattle(mode)
		if eventRuntime != nil {
			seq, _, _ := wire.Varint(request, 1)
			huntResponse, err = eventRuntime.EnterBattle(request, fmt.Sprintf("%s:%d", s.activeSession, seq))
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: enter event: %w", err)
			}
		}
		if isMonsterHunt(mode) {
			if s.monsterHunt == nil {
				return 0, nil, true, errors.New("battle: monster hunt runtime unavailable")
			}
			if validator, ok := s.monsterHunt.(interface {
				ValidatePack(int, []byte) error
			}); ok {
				if err := validator.ValidatePack(packID, request); err != nil {
					return 0, nil, true, err
				}
			}
			seq, _, _ := wire.Varint(request, 1)
			huntResponse, err = s.monsterHunt.EnterBattle(request, fmt.Sprintf("%s:%d", s.activeSession, seq))
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: enter monster hunt: %w", err)
			}
		}
		if mode == huntingGroundMode {
			if s.hunting == nil {
				return 0, nil, true, errors.New("battle: hunting runtime unavailable")
			}
			if err := s.hunting.ValidateBattle(packID, mode, monster, deck); err != nil {
				return 0, nil, true, err
			}
		}
		if mode == 1 && s.currentDifficulty != nil {
			difficulty, resolveErr := s.currentDifficulty()
			if resolveErr != nil {
				return 0, nil, true, fmt.Errorf("battle: resolve difficulty: %w", resolveErr)
			}
			loader := s.loadDifficultyDeck
			if loader == nil {
				loader = gamedata.BattleDeckForDifficulty
			}
			deck, err = loader(s.gameDataRoot, s.gameDataVersion, packID, deck, difficulty)
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: select difficulty deck: %w", err)
			}
		}
		var phases []gamedata.BattlePhase
		fieldInstance := ""
		if mode == 2 && eventRuntime == nil && s.fieldMonsters != nil && monster != 0 {
			instance, _, e := s.fieldMonsters.BeginFieldMonsterBattle(packID, monster, deck)
			if e != nil {
				return 0, nil, true, e
			}
			fieldInstance = instance
		}
		if !isMonsterHunt(mode) && eventRuntime == nil && (s.loadPhases != nil || (s.gameDataRoot != "" && packID > 0 && monster != 0)) {
			loader := s.loadPhases
			if loader == nil {
				loader = gamedata.BattleDeckPhases
			}
			phases, err = loader(s.gameDataRoot, s.gameDataVersion, packID, monster, deck)
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: load phases: %w", err)
			}
			if len(phases) != 0 && phases[0].DeckID != deck {
				return 0, nil, true, errors.New("battle: enter must select first phase deck")
			}
		}
		response := wire.AppendVarint(nil, 2, deck)
		response = append(response, huntResponse...)
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
		seq, _, _ := wire.Varint(request, 1)
		identity := fmt.Sprintf("%s:%d", s.activeSession, seq)
		if s.consumeFieldBuff != nil {
			if err := s.consumeFieldBuff(identity); err != nil {
				return 0, nil, true, fmt.Errorf("battle: consume field buff: %w", err)
			}
		}
		state.entered, state.index, state.round, state.initialBlue = true, 0, 0, nil
		state.monster, state.deck, state.pack = monster, deck, packID
		state.mode = mode
		state.enterReceipt = identity
		state.fieldInstance = fieldInstance
		state.phases, state.phase, state.phaseStarted, state.phaseSeq, state.phaseReply = phases, 0, false, 0, nil
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
		if len(state.phases) != 0 {
			// Retry requests carry the current deck; the response must restore
			// the first phase's deck, as PhaseBattleManager.ApplyRetryResponse does.
			if index != state.index {
				return 0, nil, true, errors.New("battle: retry index does not match current phase")
			}
			index = state.phases[0].DeckID
		}
		var response []byte
		for _, character := range state.initialBlue {
			response = wire.AppendBytes(response, 2, character)
		}
		response = wire.AppendVarint(response, 3, index)
		state.index, state.round = index, 0
		if len(state.phases) != 0 {
			state.deck = state.phases[0].DeckID
		}
		state.phase, state.phaseStarted, state.phaseSeq, state.phaseReply = 0, false, 0, nil
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
		if len(state.phases) != 0 && index != state.phases[state.phase].DeckID {
			return 0, nil, true, errors.New("battle: start index does not match current phase")
		}
		nextRound := state.round + 1
		var initialBlue [][]byte
		var response []byte
		err = wire.Walk(request, func(field wire.Field) error {
			if field.Type != 2 {
				return nil
			}
			if field.Number == 4 {
				response = wire.AppendBytes(response, 1, field.Value)
			} else if field.Number == 5 {
				response = wire.AppendBytes(response, 2, field.Value)
				if nextRound == 1 {
					initialBlue = append(initialBlue, append([]byte(nil), field.Value...))
				}
			}
			return nil
		})
		if err != nil {
			return 0, nil, true, err
		}
		state.index, state.round, state.phaseStarted = index, nextRound, true
		if nextRound == 1 {
			state.initialBlue = initialBlue
		}
		// Stable per-battle/round seed; reproducible across retries.
		seed := index*7919 + state.round*104729
		response = wire.AppendVarint(response, 3, seed)
		return 14, response, true, nil
	case "/BattleEnd":
		endSeq, _, _ := wire.Varint(request, 1)
		if state.endSeq == endSeq && state.endRequest != nil {
			if !bytes.Equal(state.endRequest, request) {
				return 0, nil, true, errors.New("battle: changed settlement retry")
			}
			return 15, append([]byte(nil), state.endReply...), true, nil
		}
		if !state.entered {
			return 0, nil, true, errors.New("battle: end before enter")
		}
		result, found, err := wire.Varint(request, 2)
		if err != nil || !found || result == 0 || result > 4 {
			return 0, nil, true, errors.New("battle: invalid result")
		}
		if result == 1 && len(state.phases) != 0 && (state.phase != len(state.phases)-1 || !state.phaseStarted) {
			return 0, nil, true, errors.New("battle: victory before final phase start")
		}
		response := wire.AppendVarint(nil, 1, result)
		if eventRuntime := s.eventBattle(state.mode); eventRuntime != nil {
			extra, err := eventRuntime.CompleteBattle(request, state.enterReceipt)
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: settle event: %w", err)
			}
			response = append(response, extra...)
			state.rememberEnd(request, response)
			state.entered, state.deck, state.pack, state.initialBlue = false, 0, 0, nil
			return 15, response, true, nil
		}
		if isMonsterHunt(state.mode) {
			// Monster Hunt owns its remaining HP, progression and daily/season
			// rewards. Field HP and ordinary pack rewards must not settle here.
			extra, err := s.monsterHunt.CompleteBattle(request, state.enterReceipt)
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: settle monster hunt: %w", err)
			}
			response = append(response, extra...)
			state.rememberEnd(request, response)
			state.entered, state.deck, state.pack, state.initialBlue = false, 0, 0, nil
			return 15, response, true, nil
		}
		var resultCharacters [][]byte
		finishedHealth := make(map[uint64]uint64)
		participants := make(map[uint64]bool)
		if s.commitHealth != nil {
			for _, character := range state.initialBlue {
				index, _, err := wire.Varint(character, 2)
				if err != nil {
					return 0, nil, true, err
				}
				if index != 0 {
					participants[index] = true
				}
			}
		}
		err = wire.Walk(request, func(field wire.Field) error {
			if field.Number == 3 && field.Type == 2 {
				if s.commitHealth != nil {
					index, present, err := wire.Varint(field.Value, 1)
					if err != nil || !present || !participants[index] {
						return errors.New("battle: result character was not a battle participant")
					}
					if _, duplicate := finishedHealth[index]; duplicate {
						return errors.New("battle: duplicate result character")
					}
					hp, _, err := wire.Varint(field.Value, 3)
					if err != nil || hp > math.MaxInt64 {
						return errors.New("battle: invalid result health")
					}
					finishedHealth[index] = hp
				}
				resultCharacters = append(resultCharacters, field.Value)
			}
			return nil
		})
		if err != nil {
			return 0, nil, true, err
		}
		if s.commitHealth != nil && len(finishedHealth) != 0 {
			if err := s.commitHealth(finishedHealth); err != nil {
				return 0, nil, true, fmt.Errorf("battle: persist completed character health: %w", err)
			}
		}
		for _, character := range resultCharacters {
			if s.commitHealth != nil {
				index, _, _ := wire.Varint(character, 1)
				character, _, err = wire.ReplaceVarint(character, 3, finishedHealth[index])
				if err != nil {
					return 0, nil, true, err
				}
			}
			response = wire.AppendBytes(response, 3, character)
		}
		rewardBundle := false
		if result == 1 && state.mode == huntingGroundMode {
			seq, _, _ := wire.Varint(request, 1)
			bundle, monsters, err := s.hunting.CompleteBattle(state.pack, state.mode, state.monster, state.deck,
				fmt.Sprintf("%s:%d", s.activeSession, seq))
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: settle hunting encounter: %w", err)
			}
			for _, monster := range monsters {
				response = wire.AppendBytes(response, 4, monster)
			}
			response = wire.AppendBytes(response, 5, bundle)
			rewardBundle = true
		}
		if result == 1 && state.mode != huntingGroundMode && s.inventory != nil && state.monster != 0 && s.gameDataRoot != "" {
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
			rewardIdentity := fmt.Sprintf("pack%d:monster%d:deck%d", state.pack, state.monster, state.deck)
			if state.fieldInstance != "" {
				rewardIdentity = state.fieldInstance
			}
			items, grantErr := s.inventory.GrantOnce(rewardIdentity, rewards)
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
			if state.fieldInstance != "" {
				monsterRow, e := s.fieldMonsters.CompleteFieldMonsterBattle(state.pack, state.monster, state.fieldInstance)
				if e != nil {
					return 0, nil, true, e
				}
				response = wire.AppendBytes(response, 4, monsterRow)
			}
		}
		if result == 1 && state.monster != 0 && s.onMonsterWin != nil {
			if err := s.onMonsterWin(); err != nil {
				return 0, nil, true, fmt.Errorf("battle: monster win mission: %w", err)
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
		state.rememberEnd(request, response)
		state.entered, state.deck, state.pack, state.initialBlue = false, 0, 0, nil
		return 15, response, true, nil
	case "/BattleExit":
		state.entered, state.index, state.round, state.deck, state.pack, state.initialBlue = false, 0, 0, 0, 0, nil
		return 388, nil, true, nil
	}
	panic("unreachable")
}

func (state *battleState) rememberEnd(request, response []byte) {
	state.endSeq, _, _ = wire.Varint(request, 1)
	state.endRequest = append([]byte(nil), request...)
	state.endReply = append([]byte(nil), response...)
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
