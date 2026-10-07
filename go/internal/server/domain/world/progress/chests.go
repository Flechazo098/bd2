package progress

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func fieldRewardKey(pack, id int) string {
	return fmt.Sprintf("%d:%d", pack, id)
}

// FieldRewardOpened reads persisted entries on every call; request rollback
// therefore does not leave an opened-object cache behind.
func (s *Store) FieldRewardOpened(ctx command.Context, pack, id int, period string) (bool, error) {

	store, err := s.fieldRewardEntries()
	if err != nil {
		return false, err
	}
	raw, found, err := store.LoadEntry(ctx.State, "progress", "field_rewards", fieldRewardKey(pack, id))
	return found && string(raw) == period, err
}
func (s *Store) fieldRewardEntries() (stateio.ScopedEntryStore, error) {
	if s.storage == nil {
		s.storage = stateio.RootStore{}
	}
	store, ok := s.storage.(stateio.ScopedEntryStore)
	if !ok {
		return nil, fmt.Errorf("progress: field rewards require entry storage")
	}
	return store, nil
}
func (s *Store) MarkFieldRewardOpened(ctx command.Context, pack, id int, period string) error {

	if pack <= 0 || id <= 0 {
		return fmt.Errorf("progress: invalid field reward identity")
	}
	store, err := s.fieldRewardEntries()
	if err != nil {
		return err
	}
	return store.PutEntry(ctx.State, "progress", "field_rewards", fieldRewardKey(pack, id), []byte(period))
}
func (s *Store) OpenedFieldRewards(ctx command.Context, pack int) ([]int, error) {

	store, err := s.fieldRewardEntries()
	if err != nil {
		return nil, err
	}
	entries, err := store.ListEntries(ctx.State, "progress", "field_rewards")
	if err != nil {
		return nil, err
	}
	prefix := fmt.Sprintf("%d:", pack)
	var ids []int
	for key, raw := range entries {
		if len(raw) == 0 {
			return nil, fmt.Errorf("progress: invalid field reward entry")
		}
		if suffix, found := strings.CutPrefix(key, prefix); found {
			id, e := strconv.Atoi(suffix)
			if e != nil || id <= 0 {
				return nil, fmt.Errorf("progress: invalid field reward key")
			}
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	return ids, nil
}

// OpenedFieldRewardPeriods reads the complete opened-object snapshot once from
// the current request transaction. The returned maps are owned by the caller;
// nothing is cached across requests or transaction rollback.
func (s *Store) OpenedFieldRewardPeriods(ctx command.Context) (map[int]map[int]string, error) {

	store, err := s.fieldRewardEntries()
	if err != nil {
		return nil, err
	}
	entries, err := store.ListEntries(ctx.State, "progress", "field_rewards")
	if err != nil {
		return nil, err
	}
	periods := map[int]map[int]string{}
	for key, raw := range entries {
		packKey, objectKey, found := strings.Cut(key, ":")
		pack, packErr := strconv.Atoi(packKey)
		id, idErr := strconv.Atoi(objectKey)
		if !found || packErr != nil || idErr != nil || pack <= 0 || id <= 0 || key != fieldRewardKey(pack, id) || len(raw) == 0 {
			return nil, fmt.Errorf("progress: invalid field reward entry %q", key)
		}
		if periods[pack] == nil {
			periods[pack] = map[int]string{}
		}
		periods[pack][id] = string(raw)
	}
	return periods, nil
}
