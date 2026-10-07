package progress

import (
	"bd2server/internal/server/domain/command"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func (s *Store) ResearchObjects(ctx command.Context, pack int) ([]int, error) {

	store, err := s.fieldRewardEntries()
	if err != nil {
		return nil, err
	}
	entries, err := store.ListEntries(ctx.State, "progress", "field_research")
	if err != nil {
		return nil, err
	}
	var ids []int
	prefix := fmt.Sprintf("%d:", pack)
	for key, raw := range entries {
		if string(raw) != "once" {
			return nil, fmt.Errorf("progress: invalid research entry")
		}
		if suffix, found := strings.CutPrefix(key, prefix); found {
			id, e := strconv.Atoi(suffix)
			if e != nil || id <= 0 {
				return nil, fmt.Errorf("progress: invalid research identity")
			}
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	return ids, nil
}
func (s *Store) ResearchObjectReply(ctx command.Context, pack, id int) ([]byte, bool, error) {

	store, err := s.fieldRewardEntries()
	if err != nil {
		return nil, false, err
	}
	return store.LoadEntry(ctx.State, "progress", "field_research_rewards", fieldRewardKey(pack, id))
}
func (s *Store) MarkResearchObject(ctx command.Context, pack, id int, rewardItems []byte) error {

	if pack <= 0 || id <= 0 {
		return fmt.Errorf("progress: invalid research identity")
	}
	store, err := s.fieldRewardEntries()
	if err != nil {
		return err
	}
	if err = store.PutEntry(ctx.State, "progress", "field_research_rewards", fieldRewardKey(pack, id), rewardItems); err != nil {
		return err
	}
	return store.PutEntry(ctx.State, "progress", "field_research", fieldRewardKey(pack, id), []byte("once"))
}
