// Package battle implements a deterministic local battle session. The client
// remains authoritative for turn simulation; the server validates sequencing
// and echoes the submitted combat state without capture replay.
package battle

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"fmt"
)

type Service struct {
	preparation      func(command.Context, Completion) error
	restored         *battleState
	completion       func(command.Context, Completion) ([]byte, error)
	states           map[string]*battleState
	transientVersion uint64

	gameDataRoot       string
	gameDataVersion    string
	currentPack        func(command.Context) (int, error)
	currentDifficulty  func(command.Context) (uint64, error)
	loadDifficultyDeck func(string, string, int, uint64, uint64, uint64) (gamedata.QuestBattleDeck, error)
	validateQuest      func(command.Context, int, []uint64) error
	grantRewards       func(command.Context, string, []gamedata.Reward) ([]byte, error)
	loadRewards        func(string, string, int, uint64) ([]gamedata.BattleReward, error)
	loadPhases         func(string, string, int, uint64, uint64) ([]gamedata.BattlePhase, error)
	buffs              func(command.Context) ([]gamedata.PictorialBuffStat, error)
	onTutorialWin      func(ctx command.Context) error
	onMonsterWin       func(ctx command.Context) error
	commitHealth       func(ctx command.Context, _ map[uint64]uint64) error
	hunting            HuntingRuntime
	skyway             SkyWayRuntime
	monsterHunt        MonsterHuntRuntime
	eventBattles       []EventBattleRuntime
	fieldMonsters      FieldMonsterRuntime
	consumeFieldBuff   func(ctx command.Context, _ string) error
	externalResume     func(command.Context, uint64, string) ([]byte, error)
	abandonment        func(command.Context, string) error
}
type FieldMonsterRuntime interface {
	BeginFieldMonsterBattle(ctx command.Context, _ int, _ uint64, _ uint64) (string, bool, error)
	CompleteFieldMonsterBattle(ctx command.Context, _ int, _ uint64, _ string) ([]byte, error)
}

func (s *Service) AttachFieldMonsters(runtime FieldMonsterRuntime) { s.fieldMonsters = runtime }

func (s *Service) AttachFieldBuffConsume(consume func(ctx command.Context, _ string) error) {
	s.consumeFieldBuff = consume
}

// EventBattleRuntime owns event stage eligibility, costs and settlement while
// the normal battle service transports the client's turn simulation.
type EventBattleRuntime interface {
	HandlesBattle(Mode uint64) bool
	EnterBattle(ctx command.Context, request []byte, receipt string) ([]byte, error)
	CompleteBattle(ctx command.Context, request []byte, receipt string) ([]byte, error)
}

func (s *Service) AttachEventBattle(runtime EventBattleRuntime) {
	if runtime != nil {
		s.eventBattles = append(s.eventBattles, runtime)
	}
}

func (s *Service) BeginExternal(ctx command.Context, Mode, Deck uint64, receipt string) error {
	if err := s.restore(ctx); err != nil {
		return err
	}
	if (Mode != 23 && Mode != 26) || Deck == 0 || receipt == "" {
		return fmt.Errorf("battle: invalid external encounter")
	}
	state := s.stateLocked(ctx)
	if state.Entered && state.EnterReceipt != receipt {
		return fmt.Errorf("battle: another encounter is active")
	}
	*state = battleState{Entered: true, Index: Deck, Deck: Deck, Mode: Mode, EnterReceipt: receipt}
	s.transientVersion++
	return s.persist(ctx)
}

func (s *Service) RoundCount(ctx command.Context) uint64 { return s.stateLocked(ctx).Round }

func (s *Service) eventBattle(Mode uint64) EventBattleRuntime {
	for _, runtime := range s.eventBattles {
		if runtime.HandlesBattle(Mode) {
			return runtime
		}
	}
	return nil
}

type MonsterHuntRuntime interface {
	EnterBattle(ctx command.Context, request []byte, receipt string) ([]byte, error)
	CompleteBattle(ctx command.Context, request []byte, receipt string) ([]byte, error)
}

func (s *Service) AttachMonsterHunt(runtime MonsterHuntRuntime) {
	s.monsterHunt = runtime
}

func isMonsterHunt(Mode uint64) bool { return Mode == 8 || Mode == 24 }

// HuntingRuntime validates the active hunting ground and settles each won
// encounter with the account's persistent AP, monsters and reward ledger.
type HuntingRuntime interface {
	ValidateBattle(ctx command.Context, Pack int, Mode, Monster, Deck uint64) error
	CompleteBattle(ctx command.Context, Pack int, Mode, Monster, Deck uint64, receipt string) ([]byte, [][]byte, error)
}

type SkyWayRuntime interface {
	SkyWayBeginBattle(command.Context, int, uint64, uint64, uint64) (string, error)
	SkyWayCompleteBattle(command.Context, int, uint64, uint64, uint64, string, string) ([]byte, []byte, [][]byte, error)
	SkyWayBattleStarted(command.Context, uint64, string) error
}

func (s *Service) AttachSkyWay(runtime SkyWayRuntime) { s.skyway = runtime }

const huntingGroundMode = 5

func (s *Service) AttachHunting(runtime HuntingRuntime) {
	s.hunting = runtime
}

type battleState struct {
	EntryRequest  []byte
	EntryResponse []byte
	EnteredDeck   [][]byte
	Participating []uint64
	Checkpoint    []byte
	PreviousBlue  []byte
	LastBlue      []byte
	Entered       bool
	Retryable     bool
	Index         uint64
	Round         uint64
	Monster       uint64
	Quest         uint64
	Deck          uint64
	Pack          int
	Mode          uint64
	EnterReceipt  string
	Attempt       uint64
	FieldInstance string
	InitialBlue   [][]byte
	Phases        []gamedata.BattlePhase
	Phase         int
	PhaseStarted  bool
	PhaseSeq      uint64
	PhaseReply    []byte
	EndSeq        uint64
	EndRequest    []byte
	EndReply      []byte
}

// BeginLogin binds the login to the account encounter loaded during initialization.
func (s *Service) BeginLogin(ctx command.Context) {
	id := ctx.SessionID

	if id == "" {
		return
	}
	if s.states == nil {
		s.states = make(map[string]*battleState)
	}
	if s.states[id] == nil {
		s.transientVersion++
		if len(s.states) >= 1024 {
			for key := range s.states {
				if key != id {
					delete(s.states, key)
					break
				}
			}
		}
		if s.restored != nil {
			s.states[id] = s.restored
		} else {
			s.states[id] = &battleState{}
		}
	}
}

func (s *Service) TransientVersion() uint64 {

	return s.transientVersion
}

func (s *Service) AttachMonsterWinMission(callback func(ctx command.Context) error) {
	s.onMonsterWin = callback
}

func (s *Service) AttachTutorialWin(callback func(ctx command.Context) error) {
	s.onTutorialWin = callback
}

// AttachCommittedHealth persists completed field HP after battle-only bonuses are removed.
func (s *Service) AttachCommittedHealth(callback func(ctx command.Context, _ map[uint64]uint64) error) {
	s.commitHealth = callback
}

func (s *Service) Active(ctx command.Context) bool {

	return s.stateLocked(ctx).Entered
}

func NewService(gameDataRoot, gameDataVersion string, currentPack func(command.Context) (int, error)) *Service {
	return &Service{
		gameDataRoot: gameDataRoot, gameDataVersion: gameDataVersion,
		currentPack: currentPack, loadRewards: gamedata.BattleDeckRewards,
		states: make(map[string]*battleState),
	}
}

func (s *Service) AttachRewards(grant func(command.Context, string, []gamedata.Reward) ([]byte, error)) {
	s.grantRewards = grant
}

func (s *Service) AttachQuestBattleValidation(validate func(command.Context, int, []uint64) error) {
	s.validateQuest = validate
}

// AttachCurrentDifficulty selects the GameData quest Deck for the active Pack.
func (s *Service) AttachCurrentDifficulty(resolve func(command.Context) (uint64, error)) {
	s.currentDifficulty = resolve
}

func (s *Service) AttachPictorialBuffs(buffs func(command.Context) ([]gamedata.PictorialBuffStat, error)) {
	s.buffs = buffs
}

func (s *Service) PictorialBuffs(ctx command.Context) ([]gamedata.PictorialBuffStat, error) {
	if s.buffs == nil {
		return nil, nil
	}
	return s.buffs(ctx)
}

func (s *Service) stateLocked(ctx command.Context) *battleState {
	if s.restored != nil {
		return s.restored
	}
	if s.states == nil {
		s.states = make(map[string]*battleState)
	}
	key := ctx.SessionID
	state := s.states[key]
	if state == nil {
		state = &battleState{}
		s.states[key] = state
	}
	return state
}
