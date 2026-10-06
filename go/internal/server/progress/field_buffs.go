package progress

import (
	"bd2server/internal/server/wire"
	"fmt"
	"slices"
	"strconv"
)

func (s *Store) SaveFieldBuff(id uint64, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	storedID, _, err := wire.Varint(raw, 1)
	if err != nil || id == 0 || id > 0x7fffffff || storedID != id {
		return fmt.Errorf("progress: invalid field buff identity")
	}
	store, err := s.fieldRewardEntries()
	if err != nil {
		return err
	}
	return store.PutEntry("progress", "field_buffs", strconv.FormatUint(id, 10), raw)
}

func (s *Store) FieldBuffs() ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.fieldRewardEntries()
	if err != nil {
		return nil, err
	}
	entries, err := store.ListEntries("progress", "field_buffs")
	if err != nil {
		return nil, err
	}
	ids := make([]uint64, 0, len(entries))
	for key, raw := range entries {
		id, err := strconv.ParseUint(key, 10, 32)
		storedID, _, parseErr := wire.Varint(raw, 1)
		if err != nil || parseErr != nil || id == 0 || id > 0x7fffffff || id != storedID {
			return nil, fmt.Errorf("progress: invalid saved field buff")
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var out [][]byte
	for _, id := range ids {
		out = append(out, entries[strconv.FormatUint(id, 10)])
	}
	return out, nil
}

func (s *Store) RemoveFieldBuff(id uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.fieldRewardEntries()
	if err != nil {
		return err
	}
	_, err = store.DeleteEntry("progress", "field_buffs", strconv.FormatUint(id, 10))
	return err
}

func (s *Store) ClaimFieldBuffBattle(identity string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if identity == "" {
		return false, fmt.Errorf("progress: empty field buff battle identity")
	}
	store, err := s.fieldRewardEntries()
	if err != nil {
		return false, err
	}
	_, found, err := store.LoadEntry("progress", "field_buff_battles", identity)
	if err != nil || found {
		return false, err
	}
	if err = store.PutEntry("progress", "field_buff_battles", identity, []byte("used")); err != nil {
		return false, err
	}
	return true, nil
}
