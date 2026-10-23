package app

import (
	"strconv"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle"
	"bd2server/internal/server/domain/battle/castle"
	"bd2server/internal/server/domain/battle/pvp"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/progression/conditions"
	"bd2server/internal/server/domain/roster"
)

// recordMission applies one authoritative occurrence to the regular missions
// and, through their event handler, to the scheduled event missions.
func (p *playerAssembly) recordMission(ctx command.Context, event conditions.Event) error {
	return p.missionService.RecordEvent(ctx, event, p.worldService.MissionsUnlocked)
}

// missionAccountState derives the progress of conditions that describe
// account state rather than an occurrence, so progress made before a mission
// opened, or before the server recorded the action, still counts. Values are
// raised only; the event mission service never lowers recorded progress.
func (p *playerAssembly) missionAccountState(ctx command.Context, specs []conditions.Spec) ([]uint64, error) {
	values := make([]uint64, len(specs))
	var castleProgress *castle.MissionProgress
	loadCastle := func() (*castle.MissionProgress, error) {
		if castleProgress == nil {
			progress, err := p.castleService.MissionProgress(ctx)
			if err != nil {
				return nil, err
			}
			castleProgress = &progress
		}
		return castleProgress, nil
	}
	var bestRank *uint64
	for i, spec := range specs {
		switch spec.Type {
		case conditions.PlatformConnectOrLogin:
			// Every session belongs to an authenticated platform account, so
			// the account is always bound.
			values[i] = 1
		case conditions.PackQuestIDClear: // pack = SubType, quests = Params
			for _, quest := range spec.Params {
				if quest != 0 && p.worldService.MissionsUnlocked(ctx, spec.SubType, quest) {
					values[i]++
				}
			}
		case conditions.CharTargetLevelUp:
			values[i] = p.countCharacters(spec, func(c roster.Character) uint64 { return c.Level })
		case conditions.CharTargetGradeUp:
			values[i] = p.countCharacters(spec, func(c roster.Character) uint64 { return p.design.achievementGrades.CharacterGrowth[c.ID] })
		case conditions.CostumePotentialNodeKind: // SubType = CostumeNodeTable NodeType
			for _, costume := range p.collection.Costumes() {
				for _, id := range costume.PotentialIDs {
					if node, ok := p.design.costumePotentialDesign.Nodes[costume.ID][id]; ok && node.NodeType == spec.SubType {
						values[i]++
					}
				}
			}
		case conditions.PvpRankGoal: // SubType = MirrorRankTable ID; lower IDs rank higher
			if p.pvpService == nil {
				continue
			}
			if bestRank == nil {
				best, err := p.pvpService.BestMirrorRankID(ctx)
				if err != nil {
					return nil, err
				}
				bestRank = &best
			}
			if *bestRank != 0 && *bestRank <= spec.SubType {
				values[i] = 1
			}
		case conditions.TalentSkillUpgrade: // SubType = talent class, Params = level
			for _, c := range p.permanentCharacters() {
				character, ok := p.design.talentUseDesign.Characters[c.ID]
				rule, known := p.design.talentUseDesign.Rules[[2]uint64{character.Group, c.TalentLevel}]
				if ok && known && rule.Class == spec.SubType && (conditions.AnyParam(spec.Params) || c.TalentLevel >= spec.Params[0]) {
					values[i]++
				}
			}
		case conditions.OtherGroupMissionComplete: // SubType = regular GroupId
			values[i] = p.missionService.CompletedMissionCount(spec.SubType)
		case conditions.DatingEpisodeClear:
			if p.datingService != nil {
				values[i] = p.datingService.ClearedEpisodeGroups()
			}
		case conditions.CharImprintLevelUp:
			for _, awake := range p.collection.CharAwakeStates() {
				for _, level := range awake.ImprintLevels {
					values[i] += level
				}
			}
		case conditions.CharAwakeActive:
			for _, awake := range p.collection.CharAwakeStates() {
				if awake.IsAwake {
					values[i]++
				}
			}
		case conditions.EvilCastleRageTowerClear, conditions.EvilCastleEnvyTowerClear, conditions.EvilCastleGreedTowerClear: // SubType = floor
			if p.castleService == nil {
				continue
			}
			progress, err := loadCastle()
			if err != nil {
				return nil, err
			}
			// Define_BattleModeType: 19 RAGE, 20 ENVY, 16 GREED.
			mode := map[uint64]uint64{conditions.EvilCastleRageTowerClear: 19, conditions.EvilCastleEnvyTowerClear: 20, conditions.EvilCastleGreedTowerClear: 16}[spec.Type]
			if progress.Towers[mode][spec.SubType] {
				values[i] = 1
			}
		case conditions.EvilCastleFloorBattle, conditions.EvilCastleFloorClear: // SubType = Pride floor
			if p.castleService == nil {
				continue
			}
			progress, err := loadCastle()
			if err != nil {
				return nil, err
			}
			reached := progress.PrideCleared
			if spec.Type == conditions.EvilCastleFloorBattle {
				reached = progress.PrideFought
			}
			if reached[spec.SubType] {
				values[i] = 1
			}
		case conditions.TotalWarResultScore:
			if p.castleService == nil {
				continue
			}
			progress, err := loadCastle()
			if err != nil {
				return nil, err
			}
			if spec.SubTypeReached(progress.TotalWarBest) {
				values[i] = 1
			}
		case conditions.EquipmentUpgradeToLevel:
			for _, equipment := range p.ownedEquipment.All(ctx) {
				if equipment.Level >= spec.SubType {
					values[i]++
				}
			}
		case conditions.FishingBoatUpgrade: // SubType = boat level
			if p.fishingService != nil && p.fishingService.Profile().BoatLevel >= spec.SubType {
				values[i] = 1
			}
		case conditions.FriendshipTargetLevelUp: // SubType = friendship level
			for _, entry := range p.collection.FriendshipEntries() {
				if entry.State != nil && entry.State.Level >= spec.SubType {
					values[i]++
				}
			}
		case conditions.FriendshipCostumeLevelUp: // SubType = costume; value = its friendship level
			if entry, ok := p.collection.FriendshipEntries()["costume:"+strconv.FormatUint(spec.SubType, 10)]; ok && entry.State != nil {
				values[i] = entry.State.Level
			}
		}
	}
	return values, nil
}

// permanentCharacters excludes temporary charm and story characters, which
// do not belong to the account roster.
func (p *playerAssembly) permanentCharacters() []roster.Character {
	var out []roster.Character
	for _, c := range p.worldService.CharacterService().RawAll() {
		if !roster.IsCharmCharacter(c) && !roster.IsStoryCharacter(c) {
			out = append(out, c)
		}
	}
	return out
}

// countCharacters counts the permanent characters whose value reached the
// SubType and whose UniqueCharId is listed in Params. Reaching a higher level
// or grade includes the lower one the mission names.
func (p *playerAssembly) countCharacters(spec conditions.Spec, value func(roster.Character) uint64) uint64 {
	var count uint64
	for _, c := range p.permanentCharacters() {
		if value(c) >= spec.SubType && spec.ParamAccepts(p.design.costumePotentialDesign.CharacterUnique[c.ID]) {
			count++
		}
	}
	return count
}

// Define_BattleModeType values the battle mission conditions name.
const (
	battleModeHuntingGround       uint64 = 5  // HUNTING_GROUND
	battleModePVP                 uint64 = 6  // PVP
	battleModeMonsterHunt         uint64 = 8  // MONSTER_HUNT
	battleModeMonsterHuntPractice uint64 = 24 // MONSTER_HUNT_PRACTICE
	battleModeRogueLike           uint64 = 21 // ROGUE_LIKE
	packTypePVP                   uint64 = 3  // Define_PackType PVP
)

// recordBattleStart counts the first round of an encounter.
func (p *playerAssembly) recordBattleStart(ctx command.Context, result battle.Completion) error {
	mode := result.Context.BattleMode
	events := []conditions.Event{{Type: conditions.BattleModeStartTry, Sub: mode, Count: 1}}
	seen := map[uint64]bool{}
	for _, index := range result.Participants {
		character, owned := p.worldService.CharacterService().Find(ctx, index)
		unique := p.design.costumePotentialDesign.CharacterUnique[character.ID]
		if owned && unique != 0 && !seen[unique] {
			seen[unique] = true
			events = append(events, conditions.Event{Type: conditions.BattleStartWithUniqueChar, Sub: unique, Count: 1})
		}
	}
	if mode == battleModeMonsterHunt || mode == battleModeMonsterHuntPractice {
		events = append(events, conditions.Event{Type: conditions.MonsterHuntBattleWithTraining, Count: 1})
	}
	if mode == battleModeMonsterHunt {
		events = append(events, conditions.Event{Type: conditions.MonsterHuntBattle, Count: 1})
	}
	return p.recordMissions(ctx, events)
}

// recordBattleEnd counts a settled encounter. McPackBattle names the pack as
// SubType and the battle mode as its param.
func (p *playerAssembly) recordBattleEnd(ctx command.Context, result battle.Completion) error {
	mode := result.Context.BattleMode
	events := []conditions.Event{{Type: conditions.PackBattle, Sub: result.Context.PackID, Param: mode, Count: 1}}
	if mode == battleModeHuntingGround || gamedata.IsSkyWayMode(mode) {
		events = append(events, conditions.Event{Type: conditions.HuntingBattle, Count: 1})
	}
	if mode == battleModeMonsterHunt && result.Win {
		events = append(events, conditions.Event{Type: conditions.MonsterHuntWin, Count: 1})
	}
	return p.recordMissions(ctx, events)
}

// recordPVPEnd counts a settled Mirror battle in the PVP pack, and a
// Colosseum victory.
func (p *playerAssembly) recordPVPEnd(ctx command.Context, mode pvp.Mode, win bool) error {
	if mode == pvp.Colosseum {
		if !win {
			return nil
		}
		return p.recordMission(ctx, conditions.Event{Type: conditions.ColosseumBattleWin, Count: 1})
	}
	for id, pack := range p.design.world.FieldPacks {
		if pack.Type == packTypePVP {
			return p.recordMission(ctx, conditions.Event{Type: conditions.PackBattle, Sub: uint64(id), Param: battleModePVP, Count: 1})
		}
	}
	return nil
}

func (p *playerAssembly) recordMissions(ctx command.Context, events []conditions.Event) error {
	for _, event := range events {
		if err := p.recordMission(ctx, event); err != nil {
			return err
		}
	}
	return nil
}
