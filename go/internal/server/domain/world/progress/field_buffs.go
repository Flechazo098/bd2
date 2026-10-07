package progress

import (
	"bd2server/internal/server/domain/command"
	"fmt"
	"strconv"
)

func (s *Store) RemoveFieldBuff(ctx command.Context, id uint64) error {

	store, err := s.fieldRewardEntries()
	if err != nil {
		return err
	}
	_, err = store.DeleteEntry(ctx.State, "progress", "field_buffs", strconv.FormatUint(id, 10))
	return err
}

func (s *Store) ClaimFieldBuffBattle(ctx command.Context, identity string) (bool, error) {

	if identity == "" {
		return false, fmt.Errorf("progress: empty field buff battle identity")
	}
	store, err := s.fieldRewardEntries()
	if err != nil {
		return false, err
	}
	_, found, err := store.LoadEntry(ctx.State, "progress", "field_buff_battles", identity)
	if err != nil || found {
		return false, err
	}
	if err = store.PutEntry(ctx.State, "progress", "field_buff_battles", identity, []byte("used")); err != nil {
		return false, err
	}
	return true, nil
}
