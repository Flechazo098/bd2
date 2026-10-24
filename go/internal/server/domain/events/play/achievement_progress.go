package eventplay

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/protocol/wire"
	"fmt"
	"maps"
	"slices"
	"strings"
)

func (s *Service) CompletedRhythmResults() []events.RhythmResult {
	if s.completedRhythmResults == nil {
		s.completedRhythmResults = s.readCompletedRhythmResults()
	}
	results := make([]events.RhythmResult, len(s.completedRhythmResults))
	for i, result := range s.completedRhythmResults {
		result.Judgments = maps.Clone(result.Judgments)
		results[i] = result
	}
	return results
}

func (s *Service) readCompletedRhythmResults() []events.RhythmResult {
	keys := make([]string, 0)
	for key := range s.state.Replies {
		if strings.Contains(key, ":/MiniGameRhythmPlayEnd:") {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	results := make([]events.RhythmResult, 0, len(keys))
	for _, key := range keys {
		reply := s.state.Replies[key]
		grade := num(reply.Request, 2)
		if num(reply.Request, 7) != 0 || grade == 0 || grade > 6 {
			continue
		}
		play, found, err := wire.Bytes(reply.Body, 1)
		if err != nil || !found || num(play, 1) == 0 || num(play, 2) > 2 {
			continue
		}
		result := events.RhythmResult{Music: num(play, 1), Difficulty: num(play, 2), Score: num(reply.Request, 4), Grade: grade, Combo: num(reply.Request, 6), Judgments: map[uint64]uint64{}}
		if err := wire.Walk(reply.Request, func(field wire.Field) error {
			if field.Number == 3 {
				if field.Type != 2 {
					return fmt.Errorf("%w: field %d has wire type %d, want a message", wire.ErrMalformed, field.Number, field.Type)
				}
				result.Judgments[num(field.Value, 1)] = num(field.Value, 2)
			}
			return nil
		}); err != nil {
			continue
		}
		results = append(results, result)
	}
	return results
}

func (s *Service) AchievementProgress(design *gamedata.AchievementCounterDesign) (map[[2]uint64]uint64, error) {
	if design == nil {
		return nil, fmt.Errorf("eventplay: achievement design unavailable")
	}
	if s.achievementProgress != nil && s.achievementDesign == design {
		return maps.Clone(s.achievementProgress), nil
	}
	var stars, survivalBest, defenseBest, rhythmBest, packManBest, packManChain, packManPlays uint64
	var sichuanBest float64
	sichuanStages := map[uint64]bool{}
	rhythmClears := map[uint64]map[uint64]bool{}
	var hopscotchRecords, hopscotchReplies [3]uint64
	type actionEvidence struct {
		records, missions uint64
		best              map[uint64]bool
	}
	action := map[[2]uint64]*actionEvidence{}
	actionScope := func(group, stage uint64) *actionEvidence {
		key := [2]uint64{group, stage}
		if action[key] == nil {
			action[key] = &actionEvidence{best: map[uint64]bool{}}
		}
		return action[key]
	}
	for _, r := range s.state.Records {
		switch r.Family {
		case "Sichuan":
			if r.Mode == 1 {
				if r.Clears["clear"] {
					sichuanStages[r.Stage] = true
				}
				for i := range 3 {
					if r.Clears[fmt.Sprintf("star:%d", i)] {
						stars++
					}
				}
			} else if r.Mode == 2 && r.BestScore > 0 && (sichuanBest == 0 || r.BestScore < sichuanBest) {
				sichuanBest = r.BestScore
			}
		case "Survival":
			if r.Stage == 0 {
				continue
			}
			row, err := s.survivalMap(r.Mode, r.Stage)
			if err != nil {
				return nil, err
			}
			if num(row, 11) == 1 {
				survivalBest = max(survivalBest, r.Best)
			}
		case "Defense":
			defenseBest = max(defenseBest, r.Best)
		case "Rhythm":
			rhythmBest = max(rhythmBest, r.Best)
		case "Hopscotch":
			for i, key := range []string{"clear", "full", "no-continue"} {
				if r.Clears[key] {
					hopscotchRecords[i]++
				}
			}
		case "PackMan":
			if r.Mode == 2 {
				packManBest = max(packManBest, r.Best)
			}
		case "Action":
			if r.Stage > 0 && r.Best > 0 {
				actionScope(r.Mode, r.Stage).records++
			}
		}
	}
	for _, result := range s.CompletedRhythmResults() {
		if rhythmClears[result.Difficulty] == nil {
			rhythmClears[result.Difficulty] = map[uint64]bool{}
		}
		rhythmClears[result.Difficulty][result.Music] = true
	}
	seenAction := map[string]bool{}
	for key, reply := range s.state.Replies {
		start, end := strings.LastIndex(key, ":/MiniGame"), strings.LastIndexByte(key, ':')
		if start < 0 || end <= start {
			continue
		}
		switch key[start+1 : end] {
		case "/MiniGameHopscotchGameEnd":
			if len(s.design.Tables["HopscotchDefaultTable"]) == 0 {
				return nil, fmt.Errorf("eventplay: Hopscotch default missing")
			}
			area := num(reply.Request, 2)
			if area < num(s.design.Tables["HopscotchDefaultTable"][0], 4) {
				continue
			}
			hopscotchReplies[0]++
			if area == 100 {
				hopscotchReplies[1]++
			}
			if num(reply.Request, 4) == 0 {
				hopscotchReplies[2]++
			}
		case "/MiniGamePackManEnd":
			packManPlays++
			if num(reply.Request, 5) == 0 {
				continue
			}
			progress, _, err := wire.Bytes(reply.Request, 4)
			if err != nil {
				return nil, err
			}
			packManChain = max(packManChain, num(progress, 5))
		case "/MiniGameActionEnd":
			guid, _, err := wire.Bytes(reply.Request, 2)
			if err != nil {
				return nil, err
			}
			if len(guid) == 0 || seenAction[string(guid)] {
				continue
			}
			seenAction[string(guid)] = true
			evidence := actionScope(num(reply.Request, 3), num(reply.Request, 4))
			if best := num(reply.Body, 1); best > 0 {
				evidence.best[best] = true
			}
			missions, err := list(reply.Body, 2)
			if err != nil {
				return nil, err
			}
			if len(missions) > 0 {
				evidence.missions++
			}
		}
	}
	actionDifficulty, actionMonster := map[uint64]uint64{}, map[uint64]uint64{}
	for scope, evidence := range action {
		count := max(evidence.records, evidence.missions, uint64(len(evidence.best)))
		if count == 0 {
			continue
		}
		for _, stage := range s.design.Rows("ActionGameStageTable", 3, scope[0]) {
			if num(stage, 4) != scope[1] {
				continue
			}
			monster, err := s.design.Row("ActionGameCharTable", 14, num(stage, 1))
			if err != nil {
				return nil, err
			}
			actionDifficulty[num(stage, 8)] += count
			actionMonster[num(monster, 16)] += count
			break
		}
	}
	allChallengeSongs := len(s.design.Tables["RhythmGameMusicTable"]) > 0
	for _, music := range s.design.Tables["RhythmGameMusicTable"] {
		allChallengeSongs = allChallengeSongs && rhythmClears[2][num(music, 5)]
	}
	values := map[[2]uint64]uint64{}
	for _, condition := range design.Conditions {
		sub := condition.SubType
		var value uint64
		switch condition.Type {
		case 116:
			value = boolWire(sichuanStages[sub])
		case 122:
			value = boolWire(survivalBest > 0 && survivalBest/60 >= sub)
		case 123:
			value = boolWire(defenseBest > 0 && defenseBest >= sub)
		case 124:
			value = actionDifficulty[sub]
		case 125:
			switch sub {
			case 1:
				value = boolWire(len(rhythmClears[1]) > 0)
			case 2:
				value = boolWire(allChallengeSongs)
			}
		case 154:
			value = stars
		case 155:
			value = actionMonster[sub]
		case 156:
			value = boolWire(rhythmBest > 0 && rhythmBest >= sub)
		case 158, 159, 160:
			index := condition.Type - 158
			value = max(hopscotchRecords[index], hopscotchReplies[index])
		case 171:
			value = packManPlays
		case 172:
			value = boolWire(packManChain > 0 && packManChain >= sub)
		case 173:
			value = boolWire(packManBest > 0 && packManBest >= sub)
		case 174:
			value = boolWire(sichuanBest > 0 && sichuanBest/100 <= float64(sub))
		default:
			continue
		}
		values[[2]uint64{condition.Type, sub}] = value
	}
	s.achievementDesign, s.achievementProgress = design, values
	return maps.Clone(values), nil
}
