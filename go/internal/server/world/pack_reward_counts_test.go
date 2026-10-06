package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"testing"
)

type researchEconomySpy struct {
	calls   int
	rewards []gamedata.Reward
}

func (e *researchEconomySpy) Apply(_ string, _ []gamedata.Reward, rewards []gamedata.Reward) ([]byte, error) {
	e.calls++
	e.rewards = rewards
	item := wire.AppendVarint(nil, 1, 17)
	item = wire.AppendVarint(item, 2, 103)
	item = wire.AppendVarint(item, 3, 1)
	return wire.AppendBytes(nil, 1, item), nil
}
func TestResearchRequiresCurrentMapPersistsRewardAndCounts(t *testing.T) {
	s := testService()
	storage := stateio.NewMemory()
	var err error
	s.state, err = progress.OpenStore(storage)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.state.SetActivePackID(21); err != nil {
		t.Fatal(err)
	}
	save := wire.AppendVarint(nil, 1, 1)
	save = wire.AppendVarint(save, 2, 21)
	save = wire.AppendString(save, 3, `{"MapId":211,"PlayerPosition":{"x":0,"y":0,"z":0},"ColleaguePositions":null}`)
	if err = s.state.SaveUserPosition(save); err != nil {
		t.Fatal(err)
	}
	s.researchDesigns = map[int]gamedata.FieldResearchDesign{21: {Objects: map[int]gamedata.FieldResearchObject{401: {ID: 401, Maps: []int{211}, CollectionID: 103}, 402: {ID: 402, Maps: []int{212}, CollectionID: 104}}}}
	spy := &researchEconomySpy{}
	s.researchEconomy = spy
	req := wire.AppendVarint(nil, 1, 10)
	req = wire.AppendVarint(req, 2, 21)
	bad := wire.AppendVarint(append([]byte(nil), req...), 3, 402)
	if _, _, _, err = s.Handle("/FieldObjectResearch", bad); err == nil {
		t.Fatal("accepted different map")
	}
	req = wire.AppendVarint(req, 3, 401)
	code, first, handled, err := s.Handle("/FieldObjectResearch", req)
	if err != nil || !handled || code != 59 {
		t.Fatalf("research: %d %v %v", code, handled, err)
	}
	if len(spy.rewards) != 1 || spy.rewards[0].Type != 17 || spy.rewards[0].ID != 103 {
		t.Fatalf("collection reward must use type17: %+v", spy.rewards)
	}
	s.state, err = progress.OpenStore(storage)
	if err != nil {
		t.Fatal(err)
	}
	_, again, _, err := s.Handle("/FieldObjectResearch", req)
	_, firstReward, _ := wire.Bytes(first, 2)
	_, repeatedReward, _ := wire.Bytes(again, 2)
	if err != nil || !firstReward || repeatedReward || spy.calls != 1 {
		t.Fatalf("replay duplicated reward or lost response: calls%d err%v", spy.calls, err)
	}
	counts := wire.AppendVarint(nil, 1, 11)
	counts = wire.AppendVarint(counts, 2, 21)
	counts = wire.AppendVarint(counts, 3, 1)
	code, body, _, err := s.Handle("/PackRewardObjectCount", counts)
	if err != nil || code != 226 {
		t.Fatal(err)
	}
	info, _, err := wire.Bytes(body, 1)
	if err != nil {
		t.Fatal(err)
	}
	count, _, _ := wire.Varint(info, 3)
	max, _, _ := wire.Varint(info, 4)
	if count != 1 || max != 2 {
		t.Fatalf("count%d max%d", count, max)
	}
}
