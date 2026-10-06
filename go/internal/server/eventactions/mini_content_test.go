package eventactions

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"bytes"
	"errors"
	"testing"
)

type miniRoutes []gamedata.MiniContentRoute

func (r miniRoutes) ListMiniContentRoutes() ([]gamedata.MiniContentRoute, error) { return r, nil }
func (r miniRoutes) ResolveMiniContentUID(uid uint64) (uint64, uint64, int64, int64, error) {
	for _, v := range r {
		if v.UID == uid {
			return v.ContentType, v.ContentID, v.Start, v.End, nil
		}
	}
	return 0, 0, 0, 0, errors.New("unknown content UID")
}

func TestMiniContentStoryAndQuizCompleteReplayAndWindows(t *testing.T) {
	s, e, store := setup(t)
	now := s.now().UnixMilli()
	routes := miniRoutes{{UID: 10000031, ContentType: 14, ContentID: 3, Start: now - 86400000, End: now + 86400000}, {UID: 10000032, ContentType: 13, ContentID: 3, Start: now - 86400000, End: now + 86400000}}
	d := &gamedata.MiniContentDesign{Stories: map[uint64]gamedata.Reward{99: {Type: 4, Count: 10}, 100: {Type: 4, Count: 20}}, Groups: map[uint64][]uint64{3: {99}}}
	s.design.Tables["NpcQuizTable"] = []gamedata.EventActionRow{{Values: map[int]uint64{1: 3, 2: 1, 8: 0}, Rewards: []gamedata.Reward{{Type: 4, Count: 5}}}}
	if err := s.AttachMiniContent(routes, d); err != nil {
		t.Fatal(err)
	}
	s.design.Tables["NpcQuizTable"] = []gamedata.EventActionRow{{Values: map[int]uint64{1: 3, 2: 1, 8: 0}, Rewards: []gamedata.Reward{{Type: 4, Count: 5}}}}
	story := wire.AppendVarint(req(1), 2, 99)
	code, reply, handled, err := s.Handle("/DailyStoryClear", story)
	if err != nil || code != 539 || !handled || val(reply, 2) != 99 {
		t.Fatal("story clear", code, err)
	}
	if e.calls != 1 {
		t.Fatal("story reward omitted")
	}
	if _, _, _, err := s.Handle("/DailyStoryClear", wire.AppendVarint(req(2), 2, 100)); err == nil {
		t.Fatal("unpublished story claimed")
	}
	if _, _, _, err := s.Handle("/DailyStoryClear", wire.AppendVarint(req(3), 2, 99)); err != nil || e.calls != 1 {
		t.Fatal("story repeated reward", err)
	}
	_, info, _, err := s.Handle("/DailyStoryInfo", req(4))
	if err != nil || val(info, 1) != 99 {
		t.Fatal("story completion info", err)
	}
	quiz := wire.AppendVarint(req(5), 2, 10000032)
	quiz = wire.AppendVarint(quiz, 3, 3)
	quiz = wire.AppendVarint(quiz, 4, 1)
	_, quizReply, _, err := s.Handle("/NpcQuizClear", quiz)
	if err != nil || e.calls != 2 {
		t.Fatal("dedicated quiz clear", err)
	}
	_, quizInfo, _, err := s.Handle("/NpcQuizInfo", wire.AppendVarint(req(6), 2, 10000032))
	if err != nil || len(quizInfo) == 0 {
		t.Fatal("quiz completion info", err)
	}
	reopened, err := Open(store, s.design, s.registry, e)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = s.now
	reopened.BeginSession("test")
	if err := reopened.AttachMiniContent(routes, d); err != nil {
		t.Fatal(err)
	}
	_, replay, _, err := reopened.Handle("/NpcQuizClear", quiz)
	if err != nil || !bytes.Equal(replay, quizReply) || e.calls != 2 {
		t.Fatal("quiz restart replay", err)
	}
	_, replay, _, err = reopened.Handle("/DailyStoryClear", story)
	if err != nil || !bytes.Equal(replay, reply) || e.calls != 2 {
		t.Fatal("story restart replay", err)
	}
	reopened.state.Claims = map[string]bool{}
	routes[0].End = now - 1
	routes[1].End = now - 1
	if _, _, _, err := reopened.Handle("/DailyStoryClear", wire.AppendVarint(req(7), 2, 99)); err == nil {
		t.Fatal("expired story claimed")
	}
	quiz = wire.AppendVarint(req(8), 2, 10000032)
	quiz = wire.AppendVarint(quiz, 3, 3)
	quiz = wire.AppendVarint(quiz, 4, 1)
	if _, _, _, err := reopened.Handle("/NpcQuizClear", quiz); err == nil {
		t.Fatal("expired quiz claimed")
	}
	if e.calls != 2 {
		t.Fatal("inactive content changed reward ledger")
	}
}
