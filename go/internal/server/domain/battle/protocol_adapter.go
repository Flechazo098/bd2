package battle

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bytes"
	"errors"
	"fmt"
	"math"
)

func checkSeq(request []byte) error {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return errors.New("battle: invalid request sequence")
	}
	return nil
}

func (s *Service) handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/BattleEnter" && path != "/BattleStart" && path != "/BattleRetry" && path != "/BattleVerifyState" && path != "/BattleEnd" && path != "/BattleEndTest" && path != "/BattleExit" && path != "/BattlePhaseChange" {
		return 0, nil, false, nil
	}
	if err := checkSeq(request); err != nil {
		return 0, nil, true, err
	}

	state := s.stateLocked(ctx)
	endCode := 15
	if path == "/BattleEndTest" {
		path = "/BattleEnd"
		endCode = 181
	}
	switch path {
	case "/BattlePhaseChange":
		if !state.Entered {
			return 0, nil, true, errors.New("battle: Phase change before enter")
		}
		seq, _, _ := wire.Varint(request, 1)
		if seq == state.PhaseSeq && state.PhaseReply != nil {
			return 632, append([]byte(nil), state.PhaseReply...), true, nil
		}
		if seq <= state.PhaseSeq {
			return 0, nil, true, errors.New("battle: stale Phase change sequence")
		}
		if len(state.Phases) == 0 || state.Phase+1 >= len(state.Phases) {
			return 0, nil, true, errors.New("battle: no next Phase")
		}
		if !state.PhaseStarted {
			return 0, nil, true, errors.New("battle: Phase change before current Phase start")
		}
		next := state.Phases[state.Phase+1]
		response := wire.AppendVarint(nil, 1, next.GroupID)
		response = wire.AppendVarint(response, 2, next.ID)
		response = wire.AppendVarint(response, 8, next.DeckID)
		// The local engine does not simulate combat. With verification disabled
		// and no battle_result the client explicitly preserves its blue team;
		// fabricating a verified result would overwrite HP, SP and action state.
		// Dynamic official turn/SP values are not available in this request.
		state.Phase++
		state.Index, state.Deck, state.PhaseStarted = next.DeckID, next.DeckID, false
		state.PhaseSeq, state.PhaseReply = seq, append([]byte(nil), response...)
		s.transientVersion++
		return 632, response, true, nil
	case "/BattleVerifyState":
		if !state.Entered {
			return 0, nil, true, errors.New("battle: verify before enter")
		}
		// Packet code 142 follows BattleVerify(141). State 3 is the protocol's
		// explicit SUCCESS value; PVE does not require authoritative team lists.
		return 142, wire.AppendVarint(nil, 1, 3), true, nil
	case "/BattleEnter":
		Deck, deckFound, err := wire.Varint(request, 4)
		if err != nil || !deckFound || Deck == 0 {
			return 0, nil, true, errors.New("battle: missing battle Deck")
		}
		Mode, modeFound, err := wire.Varint(request, 5)
		if err != nil || !modeFound || Mode == 0 {
			return 0, nil, true, errors.New("battle: missing battle Mode")
		}
		packID := 0
		if s.currentPack != nil {
			packID, err = s.currentPack(ctx)
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: resolve current Pack: %w", err)
			}
			if packID <= 0 {
				return 0, nil, true, errors.New("battle: current Pack is invalid")
			}
		} else if s.gameDataRoot != "" {
			return 0, nil, true, errors.New("battle: current Pack resolver is unavailable")
		}
		Monster, _, _ := wire.Varint(request, 3)
		var questID uint64
		var huntResponse []byte
		eventRuntime := s.eventBattle(Mode)
		if eventRuntime != nil {
			seq, _, _ := wire.Varint(request, 1)
			huntResponse, err = eventRuntime.EnterBattle(ctx, request, fmt.Sprintf("%s:%d", ctx.SessionID, seq))
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: enter event: %w", err)
			}
		}
		if isMonsterHunt(Mode) {
			if s.monsterHunt == nil {
				return 0, nil, true, errors.New("battle: Monster hunt runtime unavailable")
			}
			if validator, ok := s.monsterHunt.(interface {
				ValidatePack(ctx command.Context, _ int, _ []byte) error
			}); ok {
				if err := validator.ValidatePack(ctx, packID, request); err != nil {
					return 0, nil, true, err
				}
			}
			seq, _, _ := wire.Varint(request, 1)
			huntResponse, err = s.monsterHunt.EnterBattle(ctx, request, fmt.Sprintf("%s:%d", ctx.SessionID, seq))
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: enter Monster hunt: %w", err)
			}
		}
		if Mode == huntingGroundMode {
			if s.hunting == nil {
				return 0, nil, true, errors.New("battle: hunting runtime unavailable")
			}
			if err := s.hunting.ValidateBattle(ctx, packID, Mode, Monster, Deck); err != nil {
				return 0, nil, true, err
			}
		}
		if Mode == 1 && s.currentDifficulty != nil {
			difficulty, resolveErr := s.currentDifficulty(ctx)
			if resolveErr != nil {
				return 0, nil, true, fmt.Errorf("battle: resolve difficulty: %w", resolveErr)
			}
			loader := s.loadDifficultyDeck
			if loader == nil {
				loader = gamedata.ResolveQuestBattleDeck
			}
			selection, err := loader(s.gameDataRoot, s.gameDataVersion, packID, Monster, Deck, difficulty)
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: select difficulty Deck: %w", err)
			}
			if s.validateQuest == nil {
				return 0, nil, true, errors.New("battle: quest ownership validator unavailable")
			}
			if err := s.validateQuest(ctx, packID, selection.QuestIDs); err != nil {
				return 0, nil, true, fmt.Errorf("battle: quest ownership: %w", err)
			}
			for _, candidate := range selection.QuestIDs {
				if s.validateQuest(ctx, packID, []uint64{candidate}) == nil {
					questID = candidate
					break
				}
			}
			Deck = selection.DeckID
		}
		var Phases []gamedata.BattlePhase
		FieldInstance := ""
		if gamedata.IsSkyWayMode(Mode) {
			if s.skyway == nil {
				return 0, nil, true, errors.New("battle: SkyWay runtime unavailable")
			}
			FieldInstance, err = s.skyway.SkyWayBeginBattle(ctx, packID, Mode, Monster, Deck)
			if err != nil {
				return 0, nil, true, err
			}
		}
		if Mode == 2 && eventRuntime == nil && s.fieldMonsters != nil && Monster != 0 {
			instance, _, e := s.fieldMonsters.BeginFieldMonsterBattle(ctx, packID, Monster, Deck)
			if e != nil {
				return 0, nil, true, e
			}
			FieldInstance = instance
		}
		if !isMonsterHunt(Mode) && eventRuntime == nil && (s.loadPhases != nil || (s.gameDataRoot != "" && packID > 0 && Monster != 0)) {
			loader := s.loadPhases
			if loader == nil {
				loader = gamedata.BattleDeckPhases
			}
			Phases, err = loader(s.gameDataRoot, s.gameDataVersion, packID, Monster, Deck)
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: load Phases: %w", err)
			}
			if len(Phases) != 0 && Phases[0].DeckID != Deck {
				return 0, nil, true, errors.New("battle: enter must select first Phase Deck")
			}
		}
		response := wire.AppendVarint(nil, 2, Deck)
		response = append(response, huntResponse...)
		if s.buffs != nil {
			buffs, err := s.buffs(ctx)
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
		identity := fmt.Sprintf("%s:%d", ctx.SessionID, seq)
		if s.consumeFieldBuff != nil {
			if err := s.consumeFieldBuff(ctx, identity); err != nil {
				return 0, nil, true, fmt.Errorf("battle: consume field buff: %w", err)
			}
		}
		state.Entered, state.Index, state.Round, state.InitialBlue = true, 0, 0, nil
		state.Retryable = false
		state.Monster, state.Deck, state.Pack = Monster, Deck, packID
		state.Quest = questID
		state.Mode = Mode
		state.EnterReceipt = identity
		state.FieldInstance = FieldInstance
		state.Phases, state.Phase, state.PhaseStarted, state.PhaseSeq, state.PhaseReply = Phases, 0, false, 0, nil
		s.transientVersion++
		return 52, response, true, nil
	case "/BattleRetry":
		if !state.Entered && !state.Retryable {
			return 0, nil, true, errors.New("battle: retry before enter")
		}
		Index, found, err := wire.Varint(request, 2)
		if err != nil || !found || Index == 0 {
			return 0, nil, true, errors.New("battle: retry missing battle Index")
		}
		if len(state.InitialBlue) == 0 {
			return 0, nil, true, errors.New("battle: retry before initial battle state")
		}
		if Index != state.Index {
			return 0, nil, true, errors.New("battle: retry Index does not match current battle")
		}
		if state.Retryable {
			if s.currentPack == nil {
				return 0, nil, true, errors.New("battle: retry Pack resolver unavailable")
			}
			Pack, err := s.currentPack(ctx)
			if err != nil || Pack != state.Pack {
				return 0, nil, true, errors.New("battle: retry outside failed encounter Pack")
			}
			firstDeck := state.Deck
			if len(state.Phases) > 0 {
				firstDeck = state.Phases[0].DeckID
			}
			instance, err := s.skyway.SkyWayBeginBattle(ctx, state.Pack, state.Mode, state.Monster, firstDeck)
			if err != nil || instance != state.FieldInstance {
				return 0, nil, true, errors.New("battle: failed SkyWay encounter no longer available")
			}
		}
		if len(state.Phases) != 0 {
			// Retry requests carry the current Deck; the response must restore
			// the first Phase's Deck, as PhaseBattleManager.ApplyRetryResponse does.
			Index = state.Phases[0].DeckID
		}
		var response []byte
		for _, character := range state.InitialBlue {
			response = wire.AppendBytes(response, 2, character)
		}
		response = wire.AppendVarint(response, 3, Index)
		state.Index, state.Round = Index, 0
		if len(state.Phases) != 0 {
			state.Deck = state.Phases[0].DeckID
		}
		state.Phase, state.PhaseStarted, state.PhaseSeq, state.PhaseReply = 0, false, 0, nil
		state.Entered, state.Retryable = true, false
		s.transientVersion++
		return 58, response, true, nil
	case "/BattleStart":
		if !state.Entered {
			return 0, nil, true, errors.New("battle: start before enter")
		}
		Index, found, err := wire.Varint(request, 2)
		if err != nil || !found || Index == 0 {
			return 0, nil, true, errors.New("battle: invalid battle Index")
		}
		if state.Index != 0 && Index != state.Index {
			return 0, nil, true, fmt.Errorf("battle: Index changed from %d to %d", state.Index, Index)
		}
		if len(state.Phases) != 0 && Index != state.Phases[state.Phase].DeckID {
			return 0, nil, true, errors.New("battle: start Index does not match current Phase")
		}
		if runtime := s.eventBattle(state.Mode); runtime != nil {
			if validator, ok := runtime.(interface {
				ValidateBattleStart(command.Context, []byte) error
			}); ok {
				if err := validator.ValidateBattleStart(ctx, request); err != nil {
					return 0, nil, true, err
				}
			}
		}
		nextRound := state.Round + 1
		if nextRound == 1 && gamedata.IsSkyWayMode(state.Mode) {
			if err := s.skyway.SkyWayBattleStarted(ctx, state.Mode, state.EnterReceipt); err != nil {
				return 0, nil, true, err
			}
		}
		var InitialBlue [][]byte
		var response []byte
		err = wire.Walk(request, func(field wire.Field) error {
			if field.Type != 2 {
				return nil
			}
			if field.Number == 4 { //nolint:staticcheck // QF1003
				response = wire.AppendBytes(response, 1, field.Value)
			} else if field.Number == 5 {
				response = wire.AppendBytes(response, 2, field.Value)
				if nextRound == 1 {
					InitialBlue = append(InitialBlue, append([]byte(nil), field.Value...))
				}
			}
			return nil
		})
		if err != nil {
			return 0, nil, true, err
		}
		state.Index, state.Round, state.PhaseStarted = Index, nextRound, true
		if nextRound == 1 {
			state.InitialBlue = InitialBlue
		}
		s.transientVersion++
		// Stable per-battle/Round seed; reproducible across retries.
		seed := Index*7919 + state.Round*104729
		response = wire.AppendVarint(response, 3, seed)
		return 14, response, true, nil
	case "/BattleEnd":
		for _, runtime := range s.eventBattles {
			if replay, ok := runtime.(interface {
				BattleEndReplay(command.Context, []byte) ([]byte, bool, error)
			}); ok {
				extra, found, e := replay.BattleEndReplay(ctx, request)
				if e != nil {
					return 0, nil, true, e
				}
				if found {
					result, _, e := wire.Varint(request, 2)
					if e != nil {
						return 0, nil, true, e
					}
					response := append(wire.AppendVarint(nil, 1, result), extra...)
					return endCode, response, true, nil
				}
			}
		}
		EndSeq, _, _ := wire.Varint(request, 1)
		if state.EndSeq == EndSeq && state.EndRequest != nil {
			if !bytes.Equal(state.EndRequest, request) {
				return 0, nil, true, errors.New("battle: changed settlement retry")
			}
			return endCode, append([]byte(nil), state.EndReply...), true, nil
		}
		if !state.Entered {
			return 0, nil, true, errors.New("battle: end before enter")
		}
		result, found, err := wire.Varint(request, 2)
		if err != nil || !found || result == 0 || result > 4 {
			return 0, nil, true, errors.New("battle: invalid result")
		}
		if result == 1 && len(state.Phases) != 0 && (state.Phase != len(state.Phases)-1 || !state.PhaseStarted) {
			return 0, nil, true, errors.New("battle: victory before final Phase start")
		}
		response := wire.AppendVarint(nil, 1, result)
		if eventRuntime := s.eventBattle(state.Mode); eventRuntime != nil {
			extra, err := eventRuntime.CompleteBattle(ctx, request, state.EnterReceipt)
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: settle event: %w", err)
			}
			response = append(response, extra...)
			state.rememberEnd(request, response)
			state.Entered, state.Deck, state.Pack, state.InitialBlue = false, 0, 0, nil
			s.transientVersion++
			return 15, response, true, nil
		}
		if isMonsterHunt(state.Mode) {
			// Monster Hunt owns its remaining HP, progression and daily/season
			// rewards. Field HP and ordinary Pack rewards must not settle here.
			extra, err := s.monsterHunt.CompleteBattle(ctx, request, state.EnterReceipt)
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: settle Monster hunt: %w", err)
			}
			response = append(response, extra...)
			state.rememberEnd(request, response)
			state.Entered, state.Deck, state.Pack, state.InitialBlue = false, 0, 0, nil
			s.transientVersion++
			return 15, response, true, nil
		}
		var resultCharacters [][]byte
		finishedHealth := make(map[uint64]uint64)
		participants := make(map[uint64]bool)
		borrowed := make(map[uint64]bool)
		seenResult := make(map[uint64]bool)
		if s.commitHealth != nil {
			for _, character := range state.InitialBlue {
				Index, _, err := wire.Varint(character, 2)
				if err != nil {
					return 0, nil, true, err
				}
				if Index != 0 {
					participants[Index] = true
					borrowed[Index] = value(character, 38) != 0
				}
			}
		}
		err = wire.Walk(request, func(field wire.Field) error {
			if field.Number == 3 && field.Type == 2 {
				if s.commitHealth != nil {
					Index, present, err := wire.Varint(field.Value, 1)
					if err != nil || !present || !participants[Index] {
						return errors.New("battle: result character was not a battle participant")
					}
					if seenResult[Index] {
						return errors.New("battle: duplicate result character")
					}
					seenResult[Index] = true
					hp, _, err := wire.Varint(field.Value, 3)
					if err != nil || hp > math.MaxInt64 {
						return errors.New("battle: invalid result health")
					}
					if borrowed[Index] {
						return nil
					}
					finishedHealth[Index] = hp
				}
				resultCharacters = append(resultCharacters, field.Value)
			}
			return nil
		})
		if err != nil {
			return 0, nil, true, err
		}
		if s.commitHealth != nil && len(finishedHealth) != 0 {
			if err := s.commitHealth(ctx, finishedHealth); err != nil {
				return 0, nil, true, fmt.Errorf("battle: persist completed character health: %w", err)
			}
		}
		for _, character := range resultCharacters {
			if s.commitHealth != nil {
				Index, _, _ := wire.Varint(character, 1)
				character, _, err = wire.ReplaceVarint(character, 3, finishedHealth[Index])
				if err != nil {
					return 0, nil, true, err
				}
			}
			response = wire.AppendBytes(response, 3, character)
		}
		rewardBundle := false
		if result == 1 && gamedata.IsSkyWayMode(state.Mode) {
			seq, _, _ := wire.Varint(request, 1)
			bundle, bonus, monsters, err := s.skyway.SkyWayCompleteBattle(ctx, state.Pack, state.Mode, state.Monster, state.Deck, state.FieldInstance, fmt.Sprintf("%s:%d", ctx.SessionID, seq))
			if err != nil {
				return 0, nil, true, err
			}
			for _, Monster := range monsters {
				response = wire.AppendBytes(response, 4, Monster)
			}
			response = wire.AppendBytes(response, 5, bundle)
			if len(bonus) > 0 {
				response = wire.AppendBytes(response, 6, bonus)
			}
			rewardBundle = true
		}
		if result == 1 && state.Mode == huntingGroundMode {
			seq, _, _ := wire.Varint(request, 1)
			bundle, monsters, err := s.hunting.CompleteBattle(ctx, state.Pack, state.Mode, state.Monster, state.Deck,
				fmt.Sprintf("%s:%d", ctx.SessionID, seq))
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: settle hunting encounter: %w", err)
			}
			for _, Monster := range monsters {
				response = wire.AppendBytes(response, 4, Monster)
			}
			response = wire.AppendBytes(response, 5, bundle)
			rewardBundle = true
		}
		if result == 1 && state.Mode != huntingGroundMode && !gamedata.IsSkyWayMode(state.Mode) && state.Monster != 0 && s.gameDataRoot != "" {
			if state.Pack <= 0 {
				return 0, nil, true, errors.New("battle: victory has no locked Pack")
			}
			loader := s.loadRewards
			if loader == nil {
				loader = gamedata.BattleDeckRewards
			}
			rewards, rewardErr := loader(s.gameDataRoot, s.gameDataVersion, state.Pack, state.Deck)
			if rewardErr != nil {
				return 0, nil, true, fmt.Errorf("battle: Pack %d Monster %d Deck %d rewards: %w", state.Pack, state.Monster, state.Deck, rewardErr)
			}
			rewardIdentity := fmt.Sprintf("Pack%d:Monster%d:Deck%d", state.Pack, state.Monster, state.Deck)
			if state.FieldInstance != "" {
				rewardIdentity = state.FieldInstance
			}
			if s.grantRewards == nil {
				return 0, nil, true, errors.New("battle: reward runtime unavailable")
			}
			definitions := make([]gamedata.Reward, len(rewards))
			for i, reward := range rewards {
				definitions[i] = gamedata.Reward(reward)
			}
			bundle, grantErr := s.grantRewards(ctx, rewardIdentity, definitions)
			if grantErr != nil {
				return 0, nil, true, grantErr
			}
			if len(bundle) != 0 {
				response = wire.AppendBytes(response, 5, bundle)
				rewardBundle = true
			}
			if state.FieldInstance != "" {
				monsterRow, e := s.fieldMonsters.CompleteFieldMonsterBattle(ctx, state.Pack, state.Monster, state.FieldInstance)
				if e != nil {
					return 0, nil, true, e
				}
				response = wire.AppendBytes(response, 4, monsterRow)
			}
		}
		if result == 1 && state.Monster != 0 && s.onMonsterWin != nil {
			if err := s.onMonsterWin(ctx); err != nil {
				return 0, nil, true, fmt.Errorf("battle: Monster win mission: %w", err)
			}
		}
		if result == 1 && s.onTutorialWin != nil {
			if err := s.onTutorialWin(ctx); err != nil {
				return 0, nil, true, fmt.Errorf("battle: update tutorial kill mission: %w", err)
			}
		}
		for _, field := range []int{5, 6, 7, 8, 10, 11, 15, 16, 25} {
			if field == 5 && rewardBundle {
				continue
			}
			if field == 6 && gamedata.IsSkyWayMode(state.Mode) && result == 1 {
				continue
			}
			response = wire.AppendBytes(response, field, nil)
		}
		state.rememberEnd(request, response)
		state.Entered = false
		state.Retryable = (result == 2 || result == 3) && gamedata.IsSkyWayMode(state.Mode)
		if !state.Retryable {
			state.Deck, state.Pack, state.InitialBlue = 0, 0, nil
		}
		s.transientVersion++
		return endCode, response, true, nil
	case "/BattleExit":
		if runtime := s.eventBattle(state.Mode); runtime != nil {
			if exit, ok := runtime.(interface{ AbandonBattle(command.Context) error }); ok {
				if e := exit.AbandonBattle(ctx); e != nil {
					return 0, nil, true, e
				}
			}
		}
		state.Retryable = false
		state.Entered, state.Index, state.Round, state.Deck, state.Pack, state.InitialBlue = false, 0, 0, 0, 0, nil
		s.transientVersion++
		return 388, nil, true, nil
	}
	panic("unreachable")
}

func (state *battleState) rememberEnd(request, response []byte) {
	state.EndSeq, _, _ = wire.Varint(request, 1)
	state.EndRequest = append([]byte(nil), request...)
	state.EndReply = append([]byte(nil), response...)
}
