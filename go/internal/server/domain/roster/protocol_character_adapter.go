package roster

import (
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

func (s *Starter) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	var code int
	var response []byte
	switch path {
	case "/ItemInfo":
		code = 21
		for _, entry := range s.Items {
			response = wire.AppendBytes(response, 1, assets.ItemWire(entry))
		}
	case "/CostumeInfo":
		code = 40
		for _, entry := range s.Costumes {
			response = wire.AppendBytes(response, 1, CostumeWire(entry))
		}
	case "/CharInfo":
		code = 9
		for _, entry := range s.Characters {
			var character []byte
			character = add(character, 1, entry.InvenIndex)
			character = add(character, 2, entry.ID)
			character = add(character, 3, entry.HP)
			character = add(character, 4, entry.Level)
			character = add(character, 5, entry.CostumeID)
			character = add(character, 6, entry.Exp)
			character = add(character, 7, entry.UseCostume)
			character = add(character, 8, entry.TalentLevel)
			character = add(character, 9, entry.TalentExp)
			character = add(character, 10, entry.SolidarityReward)
			character = add(character, 11, entry.ExpiryTime)
			character = add(character, 13, entry.ConnectPotentialCostume)
			response = wire.AppendBytes(response, 1, character)
		}
		response = add(response, 2, s.FieldCharControlDeckType)
	default:
		return 0, nil, false, nil
	}
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 {
		return 0, nil, true, fmt.Errorf("player: %s invalid sequence", path)
	}
	return code, response, true, nil
}

func (s *MasterTitleService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/MasterTitleInfo" && path != "/MasterTitleInfoUpdate" {
		return 0, nil, false, nil
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return 0, nil, true, errors.New("player: invalid master title sequence")
	}
	if path == "/MasterTitleInfo" {
		title, err := s.load(ctx)
		if err != nil {
			return 0, nil, true, err
		}
		body := wire.AppendString(nil, 1, title.Name)
		if title.Month != 0 {
			body = wire.AppendVarint(body, 2, title.Month)
		}
		if title.Day != 0 {
			body = wire.AppendVarint(body, 3, title.Day)
		}
		return 590, body, true, nil
	}
	rawName, found, err := wire.Bytes(request, 2)
	if err != nil || !found || !validMasterTitleName(string(rawName), true) {
		return 0, nil, true, errors.New("player: invalid master title name")
	}
	month, _, err := wire.Varint(request, 3)
	if err != nil {
		return 0, nil, true, err
	}
	day, _, err := wire.Varint(request, 4)
	if err != nil || !validMasterBirthday(month, day) {
		return 0, nil, true, errors.New("player: invalid master title birthday")
	}
	next := masterTitle{Name: string(rawName), Month: month, Day: day}
	current, err := s.load(ctx)
	if err != nil {
		return 0, nil, true, err
	}
	if current == next {
		return 592, nil, true, nil
	}
	core, err := s.store.Load(ctx.State, "progress")
	if err != nil {
		return 0, nil, true, err
	}
	if len(core) == 0 {
		return 0, nil, true, errors.New("player: master title requires initialized progress")
	}
	payload, err := json.Marshal(next)
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.store.SaveWithEntries(ctx.State, "progress", nil, []stateio.EntryMutation{{Bucket: "master_title", Key: "identity", Payload: payload}}); err != nil {
		return 0, nil, true, err
	}
	return 592, nil, true, nil
}

func add(dst []byte, field int, value uint64) []byte {
	if value != 0 {
		return wire.AppendVarint(dst, field, value)
	}
	return dst
}

func decodeVarints(proto []byte, fields map[int]*uint64) error {
	return wire.Walk(proto, func(field wire.Field) error {
		value, known := fields[field.Number]
		if !known {
			return fmt.Errorf("player: unsupported starter field %d", field.Number)
		}
		if field.Type != 0 {
			return fmt.Errorf("player: starter field %d wire type %d", field.Number, field.Type)
		}
		*value, _ = binary.Uvarint(field.Value)
		return nil
	})
}

// CollectionRewardBundle encodes a persisted character/costume grant for
// any reward-bearing response. Its copy upgrades and post-max exchanges are
// identical to recruitment rewards and are replayed from the collection ledger.
func CollectionRewardBundle(c *CollectionStore, g CollectionGrant) []byte {
	return recruitRewardBundle(c, g)
}
