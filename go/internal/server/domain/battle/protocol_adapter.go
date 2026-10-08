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

func (s *Service) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
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
		s.transientVersion++
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
			packID, err = s.currentPack(ctx)
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: resolve current pack: %w", err)
			}
			if packID <= 0 {
				return 0, nil, true, errors.New("battle: current pack is invalid")
			}
		} else if s.gameDataRoot != "" {
			return 0, nil, true, errors.New("battle: current pack resolver is unavailable")
		}
		monster, _, _ := wire.Varint(request, 3)
		var huntResponse []byte
		eventRuntime := s.eventBattle(mode)
		if eventRuntime != nil {
			seq, _, _ := wire.Varint(request, 1)
			huntResponse, err = eventRuntime.EnterBattle(ctx, request, fmt.Sprintf("%s:%d", ctx.SessionID, seq))
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: enter event: %w", err)
			}
		}
		if isMonsterHunt(mode) {
			if s.monsterHunt == nil {
				return 0, nil, true, errors.New("battle: monster hunt runtime unavailable")
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
				return 0, nil, true, fmt.Errorf("battle: enter monster hunt: %w", err)
			}
		}
		if mode == huntingGroundMode {
			if s.hunting == nil {
				return 0, nil, true, errors.New("battle: hunting runtime unavailable")
			}
			if err := s.hunting.ValidateBattle(ctx, packID, mode, monster, deck); err != nil {
				return 0, nil, true, err
			}
		}
		if mode == 1 && s.currentDifficulty != nil {
			difficulty, resolveErr := s.currentDifficulty(ctx)
			if resolveErr != nil {
				return 0, nil, true, fmt.Errorf("battle: resolve difficulty: %w", resolveErr)
			}
			loader := s.loadDifficultyDeck
			if loader == nil {
				loader = gamedata.ResolveQuestBattleDeck
			}
			selection, err := loader(s.gameDataRoot, s.gameDataVersion, packID, monster, deck, difficulty)
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: select difficulty deck: %w", err)
			}
			if s.validateQuest == nil {
				return 0, nil, true, errors.New("battle: quest ownership validator unavailable")
			}
			if err := s.validateQuest(ctx, packID, selection.QuestIDs); err != nil {
				return 0, nil, true, fmt.Errorf("battle: quest ownership: %w", err)
			}
			deck = selection.DeckID
		}
		var phases []gamedata.BattlePhase
		fieldInstance := ""
		if gamedata.IsSkyWayMode(mode) {
			if s.skyway == nil {
				return 0, nil, true, errors.New("battle: SkyWay runtime unavailable")
			}
			fieldInstance, err = s.skyway.SkyWayBeginBattle(ctx, packID, mode, monster, deck)
			if err != nil {
				return 0, nil, true, err
			}
		}
		if mode == 2 && eventRuntime == nil && s.fieldMonsters != nil && monster != 0 {
			instance, _, e := s.fieldMonsters.BeginFieldMonsterBattle(ctx, packID, monster, deck)
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
		state.entered, state.index, state.round, state.initialBlue = true, 0, 0, nil
		state.retryable = false
		state.monster, state.deck, state.pack = monster, deck, packID
		state.mode = mode
		state.enterReceipt = identity
		state.fieldInstance = fieldInstance
		state.phases, state.phase, state.phaseStarted, state.phaseSeq, state.phaseReply = phases, 0, false, 0, nil
		s.transientVersion++
		return 52, response, true, nil
	case "/BattleRetry":
		if !state.entered && !state.retryable {
			return 0, nil, true, errors.New("battle: retry before enter")
		}
		index, found, err := wire.Varint(request, 2)
		if err != nil || !found || index == 0 {
			return 0, nil, true, errors.New("battle: retry missing battle index")
		}
		if len(state.initialBlue) == 0 {
			return 0, nil, true, errors.New("battle: retry before initial battle state")
		}
		if index != state.index {
			return 0, nil, true, errors.New("battle: retry index does not match current battle")
		}
		if state.retryable {
			if s.currentPack == nil {
				return 0, nil, true, errors.New("battle: retry pack resolver unavailable")
			}
			pack, err := s.currentPack(ctx)
			if err != nil || pack != state.pack {
				return 0, nil, true, errors.New("battle: retry outside failed encounter pack")
			}
			firstDeck := state.deck
			if len(state.phases) > 0 {
				firstDeck = state.phases[0].DeckID
			}
			instance, err := s.skyway.SkyWayBeginBattle(ctx, state.pack, state.mode, state.monster, firstDeck)
			if err != nil || instance != state.fieldInstance {
				return 0, nil, true, errors.New("battle: failed SkyWay encounter no longer available")
			}
		}
		if len(state.phases) != 0 {
			// Retry requests carry the current deck; the response must restore
			// the first phase's deck, as PhaseBattleManager.ApplyRetryResponse does.
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
		state.entered, state.retryable = true, false
		s.transientVersion++
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
		if nextRound == 1 && gamedata.IsSkyWayMode(state.mode) {
			if err := s.skyway.SkyWayBattleStarted(ctx, state.mode, state.enterReceipt); err != nil {
				return 0, nil, true, err
			}
		}
		var initialBlue [][]byte
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
		s.transientVersion++
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
			return endCode, append([]byte(nil), state.endReply...), true, nil
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
			extra, err := eventRuntime.CompleteBattle(ctx, request, state.enterReceipt)
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: settle event: %w", err)
			}
			response = append(response, extra...)
			state.rememberEnd(request, response)
			state.entered, state.deck, state.pack, state.initialBlue = false, 0, 0, nil
			s.transientVersion++
			return 15, response, true, nil
		}
		if isMonsterHunt(state.mode) {
			// Monster Hunt owns its remaining HP, progression and daily/season
			// rewards. Field HP and ordinary pack rewards must not settle here.
			extra, err := s.monsterHunt.CompleteBattle(ctx, request, state.enterReceipt)
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: settle monster hunt: %w", err)
			}
			response = append(response, extra...)
			state.rememberEnd(request, response)
			state.entered, state.deck, state.pack, state.initialBlue = false, 0, 0, nil
			s.transientVersion++
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
			if err := s.commitHealth(ctx, finishedHealth); err != nil {
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
		if result == 1 && gamedata.IsSkyWayMode(state.mode) {
			seq, _, _ := wire.Varint(request, 1)
			bundle, bonus, monsters, err := s.skyway.SkyWayCompleteBattle(ctx, state.pack, state.mode, state.monster, state.deck, state.fieldInstance, fmt.Sprintf("%s:%d", ctx.SessionID, seq))
			if err != nil {
				return 0, nil, true, err
			}
			for _, monster := range monsters {
				response = wire.AppendBytes(response, 4, monster)
			}
			response = wire.AppendBytes(response, 5, bundle)
			if len(bonus) > 0 {
				response = wire.AppendBytes(response, 6, bonus)
			}
			rewardBundle = true
		}
		if result == 1 && state.mode == huntingGroundMode {
			seq, _, _ := wire.Varint(request, 1)
			bundle, monsters, err := s.hunting.CompleteBattle(ctx, state.pack, state.mode, state.monster, state.deck,
				fmt.Sprintf("%s:%d", ctx.SessionID, seq))
			if err != nil {
				return 0, nil, true, fmt.Errorf("battle: settle hunting encounter: %w", err)
			}
			for _, monster := range monsters {
				response = wire.AppendBytes(response, 4, monster)
			}
			response = wire.AppendBytes(response, 5, bundle)
			rewardBundle = true
		}
		if result == 1 && state.mode != huntingGroundMode && !gamedata.IsSkyWayMode(state.mode) && state.monster != 0 && s.gameDataRoot != "" {
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
			if state.fieldInstance != "" {
				monsterRow, e := s.fieldMonsters.CompleteFieldMonsterBattle(ctx, state.pack, state.monster, state.fieldInstance)
				if e != nil {
					return 0, nil, true, e
				}
				response = wire.AppendBytes(response, 4, monsterRow)
			}
		}
		if result == 1 && state.monster != 0 && s.onMonsterWin != nil {
			if err := s.onMonsterWin(ctx); err != nil {
				return 0, nil, true, fmt.Errorf("battle: monster win mission: %w", err)
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
			if field == 6 && gamedata.IsSkyWayMode(state.mode) && result == 1 {
				continue
			}
			response = wire.AppendBytes(response, field, nil)
		}
		state.rememberEnd(request, response)
		state.entered = false
		state.retryable = (result == 2 || result == 3) && gamedata.IsSkyWayMode(state.mode)
		if !state.retryable {
			state.deck, state.pack, state.initialBlue = 0, 0, nil
		}
		s.transientVersion++
		return endCode, response, true, nil
	case "/BattleExit":
		state.retryable = false
		state.entered, state.index, state.round, state.deck, state.pack, state.initialBlue = false, 0, 0, 0, 0, nil
		s.transientVersion++
		return 388, nil, true, nil
	}
	panic("unreachable")
}

func (state *battleState) rememberEnd(request, response []byte) {
	state.endSeq, _, _ = wire.Varint(request, 1)
	state.endRequest = append([]byte(nil), request...)
	state.endReply = append([]byte(nil), response...)
}
