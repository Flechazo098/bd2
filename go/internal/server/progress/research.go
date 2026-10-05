package progress

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func (s *Store) ResearchObjects(pack int) ([]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.fieldRewardEntries()
	if err != nil {
		return nil, err
	}
	entries, err := store.ListEntries("progress", "field_research")
	if err != nil {
		return nil, err
	}
	var ids []int
	prefix := fmt.Sprintf("%d:", pack)
	for key, raw := range entries {
		if string(raw) != "once" {
			return nil, fmt.Errorf("progress: invalid research entry")
		}
		if strings.HasPrefix(key, prefix) {
			id, e := strconv.Atoi(strings.TrimPrefix(key, prefix))
			if e != nil || id <= 0 {
				return nil, fmt.Errorf("progress: invalid research identity")
			}
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	return ids, nil
}
func (s *Store) ResearchObjectReply(pack, id int) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.fieldRewardEntries()
	if err != nil {
		return nil, false, err
	}
	return store.LoadEntry("progress", "field_research_rewards", fieldRewardKey(pack, id))
}
func (s *Store) MarkResearchObject(pack, id int, rewardItems []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pack <= 0 || id <= 0 {
		return fmt.Errorf("progress: invalid research identity")
	}
	store, err := s.fieldRewardEntries()
	if err != nil {
		return err
	}
	if err = store.PutEntry("progress", "field_research_rewards", fieldRewardKey(pack, id), rewardItems); err != nil {
		return err
	}
	return store.PutEntry("progress", "field_research", fieldRewardKey(pack, id), []byte("once"))
}
