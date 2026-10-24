package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

var ErrStoryCheckpointConflict = errors.New("world: story checkpoint conflict")

func storyCheckpointConflict(pending, requested StoryCheckpoint) error {
	return fmt.Errorf("%w: pending={pack=%d quest=%d difficulty=%d phase=%s} requested={pack=%d quest=%d difficulty=%d phase=%s}",
		ErrStoryCheckpointConflict, pending.PackID, pending.QuestID, pending.Difficulty, pending.Phase,
		requested.PackID, requested.QuestID, requested.Difficulty, requested.Phase)
}

type StoryCheckpoint struct {
	ID                string `json:"checkpoint_id"`
	PackID            int    `json:"pack_id"`
	QuestID           int    `json:"quest_id"`
	Difficulty        int    `json:"difficulty"`
	Phase             string `json:"phase"`
	Timeline          string `json:"timeline"`
	ObjectID          int    `json:"object_id"`
	MapID             int    `json:"map_id"`
	SimpleTalkGroupID uint64 `json:"simple_talk_group_id"`
}

type StoryCheckpointRequest struct {
	StoryCheckpoint
	Operation string `json:"operation"`
}

type StoryCheckpointReply struct {
	Checkpoint *StoryCheckpoint `json:"checkpoint"`
}

func loadStoryCheckpoint(ctx command.Context) (*StoryCheckpoint, error) {
	raw, found, err := ctx.State.LoadEntry("world", "story_checkpoint", "pending")
	if err != nil || !found {
		return nil, err
	}
	var checkpoint StoryCheckpoint
	if err = json.Unmarshal(raw, &checkpoint); err != nil {
		return nil, err
	}
	if checkpoint.ID == "" || checkpoint.PackID <= 0 || checkpoint.QuestID <= 0 {
		return nil, fmt.Errorf("%w: saved story checkpoint %q has pack %d quest %d", ErrInvalidRequest, checkpoint.ID, checkpoint.PackID, checkpoint.QuestID)
	}
	return &checkpoint, nil
}

func saveStoryCheckpoint(ctx command.Context, checkpoint StoryCheckpoint) error {
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	return ctx.State.PutEntry("world", "story_checkpoint", "pending", raw)
}

func (s *Service) StoryCheckpoint(ctx command.Context, request StoryCheckpointRequest) (StoryCheckpointReply, error) {
	pending, err := loadStoryCheckpoint(ctx)
	if err != nil {
		return StoryCheckpointReply{}, err
	}
	reply := StoryCheckpointReply{Checkpoint: pending}
	switch request.Operation {
	case "read":
		return reply, nil
	case "complete":
		if request.ID == "" {
			return reply, fmt.Errorf("%w: story checkpoint completion names no checkpoint", ErrInvalidRequest)
		}
		if pending == nil || pending.ID != request.ID {
			return reply, nil
		}
		if err = ctx.State.PutEntry("world", "story_checkpoint_completed", pending.ID, []byte("true")); err != nil {
			return reply, err
		}
		if _, err = ctx.State.DeleteEntry("world", "story_checkpoint", "pending"); err != nil {
			return reply, err
		}
		return StoryCheckpointReply{}, nil
	case "begin":
		if request.ID == "" || len(request.ID) > 128 {
			return reply, fmt.Errorf("%w: story checkpoint id %q is empty or longer than 128 bytes", ErrInvalidRequest, request.ID)
		}
		if _, completed, err := ctx.State.LoadEntry("world", "story_checkpoint_completed", request.ID); err != nil {
			return reply, err
		} else if completed {
			return reply, nil
		}
		if pending != nil {
			candidate := request.StoryCheckpoint
			candidate.ID = pending.ID
			candidate.MapID = pending.MapID
			if candidate == *pending {
				return reply, nil
			}
			return reply, storyCheckpointConflict(*pending, request.StoryCheckpoint)
		}
		if err = s.validateStoryCheckpoint(ctx, request.StoryCheckpoint); err != nil {
			return reply, err
		}
		if err = saveStoryCheckpoint(ctx, request.StoryCheckpoint); err != nil {
			return reply, err
		}
		return StoryCheckpointReply{Checkpoint: &request.StoryCheckpoint}, nil
	default:
		return reply, fmt.Errorf("%w: unknown story checkpoint operation %q", ErrInvalidRequest, request.Operation)
	}
}

func (s *Service) validateStoryCheckpoint(ctx command.Context, checkpoint StoryCheckpoint) error {
	if field, exists := s.fieldPacks[checkpoint.PackID]; exists && field.IntroQuest != nil {
		return s.validateFieldIntroCheckpoint(ctx, checkpoint, field)
	}
	pack, exists := s.storyCatalog.Packs[checkpoint.PackID]
	if !exists || !s.packUnlocked(ctx, checkpoint.PackID) || checkpoint.Difficulty != s.questDifficultyFor(checkpoint.PackID, checkpoint.QuestID) {
		return fmt.Errorf("%w: checkpoint pack or difficulty unavailable", ErrInvalidRequest)
	}
	if s.packDetailDesign == nil {
		return fmt.Errorf("world: checkpoint map design unavailable")
	}
	maps, err := s.packDetailDesign(checkpoint.PackID)
	if err != nil {
		return err
	}
	if !slices.Contains(maps.MapIDs, checkpoint.MapID) {
		return fmt.Errorf("%w: checkpoint map outside pack", ErrInvalidRequest)
	}
	quest, exists := pack.Quests[checkpoint.QuestID]
	if !exists {
		return fmt.Errorf("%w: pack %d has no quest %d for checkpoint %q", ErrInvalidRequest, checkpoint.PackID, checkpoint.QuestID, checkpoint.ID)
	}
	cleared := s.state.QuestCleared(checkpoint.QuestID, checkpoint.PackID, checkpoint.Difficulty)
	switch checkpoint.Phase {
	case "accept":
		if cleared || !s.canClear(ctx, checkpoint.PackID, checkpoint.QuestID) || checkpoint.Timeline == "" || checkpoint.Timeline != quest.AcceptCinemaName || checkpoint.ObjectID != 0 || checkpoint.SimpleTalkGroupID != 0 {
			return fmt.Errorf("%w: accept checkpoint %q for quest %d (cleared=%t, timeline %q, want %q, object %d, talk %d)", ErrInvalidRequest, checkpoint.ID, checkpoint.QuestID, cleared, checkpoint.Timeline, quest.AcceptCinemaName, checkpoint.ObjectID, checkpoint.SimpleTalkGroupID)
		}
	case "complete":
		if !cleared || checkpoint.Timeline == "" || checkpoint.Timeline != quest.CompleteCinemaName || checkpoint.ObjectID != 0 || checkpoint.SimpleTalkGroupID != 0 {
			return fmt.Errorf("%w: complete checkpoint %q for quest %d (cleared=%t, timeline %q, want %q, object %d, talk %d)", ErrInvalidRequest, checkpoint.ID, checkpoint.QuestID, cleared, checkpoint.Timeline, quest.CompleteCinemaName, checkpoint.ObjectID, checkpoint.SimpleTalkGroupID)
		}
	case "interaction":
		if cleared || !s.canClear(ctx, checkpoint.PackID, checkpoint.QuestID) || checkpoint.ObjectID <= 0 || s.presentationSource == nil {
			return fmt.Errorf("%w: interaction checkpoint %q for quest %d (cleared=%t, object %d)", ErrInvalidRequest, checkpoint.ID, checkpoint.QuestID, cleared, checkpoint.ObjectID)
		}
		interaction, err := s.presentationSource.StoryInteraction(checkpoint.PackID, checkpoint.ObjectID)
		if err != nil {
			return err
		}
		if !slices.Contains(interaction.QuestIDs, uint64(checkpoint.QuestID)) || checkpoint.Timeline != interaction.Timeline || checkpoint.SimpleTalkGroupID != interaction.SimpleTalkGroupID || checkpoint.Timeline == "" && checkpoint.SimpleTalkGroupID == 0 {
			return fmt.Errorf("%w: interaction checkpoint %q object %d does not match quest %d (timeline %q, want %q, talk %d, want %d)", ErrInvalidRequest, checkpoint.ID, checkpoint.ObjectID, checkpoint.QuestID, checkpoint.Timeline, interaction.Timeline, checkpoint.SimpleTalkGroupID, interaction.SimpleTalkGroupID)
		}
	default:
		return fmt.Errorf("%w: unknown story checkpoint phase %q", ErrInvalidRequest, checkpoint.Phase)
	}
	return nil
}

func (s *Service) validateFieldIntroCheckpoint(ctx command.Context, checkpoint StoryCheckpoint, pack gamedata.FieldPack) error {
	intro := pack.IntroQuest
	current, err := s.CurrentPackID(ctx)
	if err != nil {
		return err
	}
	if current != checkpoint.PackID || !s.packUnlocked(ctx, checkpoint.PackID) || intro == nil ||
		checkpoint.QuestID != intro.ID || checkpoint.Difficulty != 0 || checkpoint.Phase != "accept" ||
		checkpoint.Timeline != intro.AcceptCinemaName || checkpoint.ObjectID != 0 || checkpoint.SimpleTalkGroupID != 0 || !pack.MapIDs[checkpoint.MapID] {
		return fmt.Errorf("%w: field introduction checkpoint unavailable", ErrInvalidRequest)
	}
	return nil
}

func (s *Service) beginQuestCompletionPresentation(ctx command.Context, pack, quest, difficulty int) error {
	design := s.storyCatalog.Packs[pack].Quests[quest]
	if design.CompleteCinemaName == "" {
		return nil
	}
	mapID := design.TimelineStartMapID
	if saved, found := s.state.Position(); found && saved.PackID == pack {
		mapID = saved.Position.MapID
	}
	if mapID == 0 && len(s.storyCatalog.Packs[pack].MapIDs) > 0 {
		mapID = s.storyCatalog.Packs[pack].MapIDs[0]
	}
	checkpoint := StoryCheckpoint{ID: fmt.Sprintf("quest-complete:%d:%d:%d", pack, difficulty, quest), PackID: pack, QuestID: quest, Difficulty: difficulty, Phase: "complete", Timeline: design.CompleteCinemaName, MapID: mapID}
	pending, err := loadStoryCheckpoint(ctx)
	if err != nil {
		return err
	}
	if pending != nil && (pending.PackID != pack || pending.QuestID != quest || pending.Difficulty != difficulty || pending.Phase != "interaction" && pending.Phase != "complete") {
		return storyCheckpointConflict(*pending, checkpoint)
	}
	return saveStoryCheckpoint(ctx, checkpoint)
}
