package world

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestCarriedFieldObjectSQLiteReconnectQuestResetAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	s := testService()
	s.state, err = progress.OpenStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.state.SetActivePackID(21); err != nil {
		t.Fatal(err)
	}
	if err = s.state.SaveUserPosition(wire.AppendString(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 21), 3, `{"MapId":211,"PlayerPosition":{"x":0,"y":0,"z":0},"ColleaguePositions":null}`)); err != nil {
		t.Fatal(err)
	}
	s.WithFieldObjects(map[int]gamedata.FieldObjectDesign{21: {Objects: map[int]gamedata.FieldRewardObject{}, Actions: map[int]gamedata.FieldActionObject{6011: {ID: 6011, GroupID: 601, MapID: 211, Type: 1, QuestID: 1}}}})
	position := wire.AppendVarint(nil, 1, 211)
	position = append(position, 0x15)
	position = binary.LittleEndian.AppendUint32(position, math.Float32bits(2.5))
	request := wire.AppendBytes(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, 21), 3, 601), 4, 6011), 5, position)
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	code, _, handled, err := s.Handle("/FieldObjecPositionUpdate", request)
	if err != nil || !handled || code != 95 {
		t.Fatalf("carry save: %d %v %v", code, handled, err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.state, err = progress.OpenStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	infoRequest := wire.AppendVarint(wire.AppendVarint(nil, 1, 3), 2, 21)
	_, info, _, err := s.Handle("/FieldObjectInfo", infoRequest)
	if err != nil {
		t.Fatal(err)
	}
	object, found, err := wire.Bytes(info, 2)
	saved, _, _ := wire.Bytes(object, 2)
	x := float32(0)
	_ = wire.Walk(saved, func(f wire.Field) error {
		if f.Number == 2 {
			x = math.Float32frombits(binary.LittleEndian.Uint32(f.Value))
		}
		return nil
	})
	if err != nil || !found || x != 2.5 {
		t.Fatalf("reconnect lost carried position: %x %v", info, err)
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	changed := append([]byte(nil), request...)
	changed[len(changed)-1] ^= 0x80
	if _, _, _, err = s.Handle("/FieldObjecPositionUpdate", changed); err != nil {
		t.Fatal(err)
	}
	if err = op.Rollback(); err != nil && !errors.Is(err, stateio.ErrStateRecoveryRequired) {
		t.Fatal(err)
	}
	if err = repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.state, err = progress.OpenStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	_, after, _, err := s.Handle("/FieldObjectInfo", infoRequest)
	if err != nil || string(info) != string(after) {
		t.Fatal("rolled-back position survived")
	}
	bad := wire.AppendBytes(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 4), 2, 21), 3, 999), 4, 6011), 5, position)
	if _, _, _, err = s.Handle("/FieldObjecPositionUpdate", bad); err == nil {
		t.Fatal("forged group accepted")
	}
	if err = s.state.ClearQuest(1, 21, 0); err != nil {
		t.Fatal(err)
	}
	_, after, _, err = s.Handle("/FieldObjectInfo", infoRequest)
	_, found, _ = wire.Bytes(after, 2)
	if err != nil || found {
		t.Fatal("quest transition restored stale carried position")
	}
}

func TestInstalledFieldObjectCountersUseClientIndependentCategories(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("set BD2_REAL_GAMEDATA to verify current field summary categories")
	}
	design, err := gamedata.LoadFieldObjects(root, "20260923193640", 21)
	if err != nil {
		t.Fatal(err)
	}
	s := testService()
	s.WithFieldObjects(map[int]gamedata.FieldObjectDesign{21: design})
	if len(design.Objects) != 9 {
		t.Fatal("authored field regression requires review")
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 21)
	request = wire.AppendVarint(wire.AppendVarint(request, 3, 2), 3, 3)
	_, body, _, err := s.Handle("/PackRewardObjectCount", request)
	if err != nil {
		t.Fatal(err)
	}
	totals := map[uint64]uint64{}
	if err = wire.Walk(body, func(f wire.Field) error {
		if f.Number == 1 {
			category, _, e := wire.Varint(f.Value, 1)
			total, _, e2 := wire.Varint(f.Value, 4)
			if e != nil {
				return e
			}
			if e2 != nil {
				return e2
			}
			totals[category] = total
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if totals[2] != 9 || totals[3] != 0 {
		t.Fatalf("one-time vs renewable counts: %v", totals)
	}
}
