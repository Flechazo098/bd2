// Package battle implements a deterministic local battle session. The client
// remains authoritative for turn simulation; the server validates sequencing
// and echoes the submitted combat state without capture replay.
package battle

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
)

type Service struct {
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
	monsterHunt        MonsterHuntRuntime
	eventBattles       []EventBattleRuntime
	fieldMonsters      FieldMonsterRuntime
	consumeFieldBuff   func(ctx command.Context, _ string) error
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
	HandlesBattle(mode uint64) bool
	EnterBattle(ctx command.Context, request []byte, receipt string) ([]byte, error)
	CompleteBattle(ctx command.Context, request []byte, receipt string) ([]byte, error)
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
	EnterBattle(ctx command.Context, request []byte, receipt string) ([]byte, error)
	CompleteBattle(ctx command.Context, request []byte, receipt string) ([]byte, error)
}

func (s *Service) AttachMonsterHunt(runtime MonsterHuntRuntime) {
	s.monsterHunt = runtime
}

func isMonsterHunt(mode uint64) bool { return mode == 8 || mode == 24 }

// HuntingRuntime validates the active hunting ground and settles each won
// encounter with the account's persistent AP, monsters and reward ledger.
type HuntingRuntime interface {
	ValidateBattle(ctx command.Context, pack int, mode, monster, deck uint64) error
	CompleteBattle(ctx command.Context, pack int, mode, monster, deck uint64, receipt string) ([]byte, [][]byte, error)
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

// BeginLogin creates the transient battle state owned by this login identity.
// Persistent rewards require a successful BattleEnd command transaction.
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
		s.states[id] = &battleState{}
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

// AttachCommittedHealth persists only completed battle results. Round state
// remains transient, so reconnect rolls back an unfinished battle.
// The callback may normalize values to field HP; the response uses those
// committed values rather than echoing battle-only HP buffs.
func (s *Service) AttachCommittedHealth(callback func(ctx command.Context, _ map[uint64]uint64) error) {
	s.commitHealth = callback
}

func (s *Service) Active(ctx command.Context) bool {

	return s.stateLocked(ctx).entered
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

// AttachCurrentDifficulty selects the GameData quest deck for the active pack.
func (s *Service) AttachCurrentDifficulty(resolve func(command.Context) (uint64, error)) {
	s.currentDifficulty = resolve
}

func (s *Service) AttachPictorialBuffs(buffs func(command.Context) ([]gamedata.PictorialBuffStat, error)) {
	s.buffs = buffs
}

// The local engine does not simulate combat. With verification disabled
// and no battle_result the client explicitly preserves its blue team;
// fabricating a verified result would overwrite HP, SP and action state.
// Dynamic official turn/SP values are not available in this request.

// Packet code 142 follows BattleVerify(141). State 3 is the protocol's
// explicit SUCCESS value; PVE does not require authoritative team lists.

// The local engine is the normal deterministic engine.

// Retry requests carry the current deck; the response must restore
// the first phase's deck, as PhaseBattleManager.ApplyRetryResponse does.

//nolint:staticcheck // QF1003

// Stable per-battle/round seed; reproducible across retries.

// Monster Hunt owns its remaining HP, progression and daily/season
// rewards. Field HP and ordinary pack rewards must not settle here.

func (s *Service) stateLocked(ctx command.Context) *battleState {
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
