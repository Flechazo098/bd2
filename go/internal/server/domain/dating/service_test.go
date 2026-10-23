//go:build integration

package dating

import (
	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"fmt"
	"maps"
	"testing"
	"time"
)

type ledger struct{ balances map[[2]uint64]uint64 }

func (l *ledger) CanApply(_ command.Context, costs []gamedata.Reward) error {
	for _, r := range costs {
		if l.balances[[2]uint64{r.Type, r.ID}] < r.Count {
			return fmt.Errorf("insufficient funds")
		}
	}
	return nil
}
func (l *ledger) Apply(ctx command.Context, _ string, costs, rewards []gamedata.Reward) ([]byte, error) {
	if e := l.CanApply(ctx, costs); e != nil {
		return nil, e
	}
	for _, r := range costs {
		l.balances[[2]uint64{r.Type, r.ID}] -= r.Count
	}
	for _, r := range rewards {
		l.balances[[2]uint64{r.Type, r.ID}] += r.Count
	}
	return nil, nil
}
func liveDesign(t *testing.T) *gamedata.DatingDesign {
	t.Helper()
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	versionconfig.Use(versions)
	root, version := versions.Resolve("data/resources/GameData"), versions.GameDataVersion
	d, e := gamedata.LoadDatingDesign(root, version)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func TestDatingFullConversationPurchaseAndReconnect(t *testing.T) {
	d := liveDesign(t)
	store := stateio.NewMemory()
	ctx := command.Context{Identity: command.Identity{SessionID: "dating"}, State: store}
	money := &ledger{balances: map[[2]uint64]uint64{{41, 0}: d.Default.V(5)}}
	now := time.Date(2026, 10, 17, 0, 1, 0, 0, time.UTC)
	var group uint64
	for g, c := range d.CostumeGroup {
		if c.V(3) != 0 {
			continue
		}
		for _, epID := range d.Episodes.Groups[g] {
			ep, _ := d.Episodes.Row(g, epID)
			for _, msgID := range d.Messages.Groups[ep.V(3)] {
				msg, _ := d.Messages.Row(ep.V(3), msgID)
				if msg.V(5) == 3 {
					group = g
					break
				}
			}
			if group != 0 {
				break
			}
		}
		if group != 0 {
			break
		}
	}
	if group == 0 {
		t.Fatal("GameData has no ordinary encounter")
	}
	open := func() *Service {
		s, e := Open(ctx, stateio.RootStore{}, d, money)
		if e != nil {
			t.Fatal(e)
		}
		s.now = func() time.Time { return now }
		s.AttachFirstMeeting(func(_ command.Context, g uint64) (bool, error) { return g == group, nil })
		return s
	}
	s := open()
	seq := uint64(0)
	call := func(path string, args ...uint64) ([]byte, error) {
		seq++
		b := wire.AppendVarint(nil, 1, seq)
		for i, v := range args {
			b = wire.AppendVarint(b, i+2, v)
		}
		_, out, handled, e := s.Handle(ctx, path, b)
		if !handled {
			t.Fatalf("unhandled %s", path)
		}
		return out, e
	}
	must := func(path string, args ...uint64) []byte {
		t.Helper()
		out, e := call(path, args...)
		if e != nil {
			t.Fatalf("%s %v: %v", path, args, e)
		}
		return out
	}
	if _, e := call("/DatingEpisodeClear", group, 2); e == nil {
		t.Fatal("unmet character bypassed first meeting")
	}
	first, _ := d.Episodes.Row(group, 1)
	firstReward := [2]uint64{first.V(11), first.V(10)}
	firstBalance := money.balances[firstReward]
	must("/DatingEpisodeClear", group, 1)
	if money.balances[firstReward]-firstBalance != first.V(8) {
		t.Fatal("first meeting reward did not arrive")
	}
	if money.balances[[2]uint64{41, 0}] != d.Default.V(5) {
		t.Fatal("first meeting consumed encounter AP")
	}
	balances := maps.Clone(money.balances)
	must("/DatingEpisodeClear", group, 1)
	if !maps.Equal(balances, money.balances) {
		t.Fatal("first meeting rewarded twice")
	}
	giftsRead := 0
	read := func(messageGroup uint64) {
		t.Helper()
		for _, id := range d.Messages.Groups[messageGroup] {
			r, _ := d.Messages.Row(messageGroup, id)
			// The client sends zero when either answer text id is empty.
			text := uint64(0)
			if r.V(5) == 1 && r.V(9) == 1 && r.V(3) != 0 && r.V(4) != 0 {
				text = r.V(3)
			}
			must("/DatingMessageUpdate", messageGroup, id, text)
			if r.V(5) == 3 {
				expected := maps.Clone(money.balances)
				for i, count := range r.A(6) {
					expected[[2]uint64{r.A(8)[i], r.A(7)[i]}] += count
				}
				must("/DatingMessageReward", messageGroup, id)
				if !maps.Equal(expected, money.balances) {
					t.Fatal("gift reward did not arrive in the configured quantities")
				}
				giftsRead++
				balances := maps.Clone(money.balances)
				must("/DatingMessageReward", messageGroup, id)
				if !maps.Equal(balances, money.balances) {
					t.Fatal("gift rewarded twice")
				}
			}
		}
	}
	read(first.V(3))
	next, _ := d.Episodes.Row(group, 2)
	if _, e := call("/DatingEpisodeClear", group, 2); e == nil {
		t.Fatal("episode cleared without prerequisite conversation")
	}
	read(next.V(2))
	ap := money.balances[[2]uint64{41, 0}]
	must("/DatingEpisodeClear", group, 2)
	if money.balances[[2]uint64{41, 0}]+1 != ap {
		t.Fatal("encounter AP was not consumed")
	}
	read(next.V(3))
	before := must("/DatingInfo")
	s = open()
	after := must("/DatingInfo")
	if !bytes.Equal(before, after) {
		t.Fatal("dating progress and answers changed across login")
	}
	price := d.Default.A(2)[0]
	typ := d.Default.A(4)[0]
	id := d.Default.A(3)[0]
	count := d.Default.V(1)
	money.balances[[2]uint64{typ, id}] = price * count
	item := wire.AppendVarint(nil, 2, id)
	item = wire.AppendVarint(item, 3, typ)
	item = wire.AppendVarint(item, 4, price*count)
	seq++
	b := wire.AppendVarint(nil, 1, seq)
	b = wire.AppendVarint(b, 2, count)
	b = wire.AppendBytes(b, 3, item)
	_, out, _, e := s.Handle(ctx, "/DatingApBuy", b)
	if e != nil {
		t.Fatal(e)
	}
	if val(out, 2) != count || money.balances[[2]uint64{typ, id}] != 0 || money.balances[[2]uint64{42, 0}] != count {
		t.Fatal("purchase did not exchange payment for stacked AP")
	}
	balances = maps.Clone(money.balances)
	_, replayed, _, e := s.Handle(ctx, "/DatingApBuy", b)
	if e != nil || !bytes.Equal(out, replayed) || !maps.Equal(balances, money.balances) {
		t.Fatal("purchase replay changed balance or reply")
	}
	money.balances[[2]uint64{typ, id}] = price
	balances = maps.Clone(money.balances)
	seq++
	excessItem := wire.AppendVarint(nil, 2, id)
	excessItem = wire.AppendVarint(excessItem, 3, typ)
	excessItem = wire.AppendVarint(excessItem, 4, price)
	excess := wire.AppendVarint(nil, 1, seq)
	excess = wire.AppendVarint(excess, 2, 1)
	excess = wire.AppendBytes(excess, 3, excessItem)
	if _, _, _, e = s.Handle(ctx, "/DatingApBuy", excess); e == nil {
		t.Fatal("daily purchase cap bypassed")
	}
	if !maps.Equal(balances, money.balances) || val(must("/DatingInfo"), 4) != count {
		t.Fatal("rejected over-cap purchase changed balances or purchase count")
	}
	s = open()
	if val(must("/DatingInfo"), 4) != count {
		t.Fatal("daily purchases lost across login")
	}
	now = now.Add(4 * 24 * time.Hour)
	if val(must("/DatingInfo"), 4) != 0 {
		t.Fatal("daily purchase limit did not reset")
	}
	if money.balances[[2]uint64{42, 0}] != count {
		t.Fatal("daily reset destroyed stacked AP")
	}
	for _, epID := range d.Episodes.Groups[group] {
		if epID <= 2 {
			continue
		}
		ep, _ := d.Episodes.Row(group, epID)
		read(ep.V(2))
		if money.balances[[2]uint64{41, 0}] == 0 && money.balances[[2]uint64{42, 0}] == 0 {
			now = now.Add(24 * time.Hour)
			money.balances[[2]uint64{41, 0}] = d.Default.V(5)
		}
		prior := maps.Clone(money.balances)
		currency := uint64(41)
		if prior[[2]uint64{41, 0}] == 0 {
			currency = 42
		}
		prior[[2]uint64{currency, 0}]--
		prior[[2]uint64{ep.V(11), ep.V(10)}] += ep.V(8)
		must("/DatingEpisodeClear", group, epID)
		if !maps.Equal(prior, money.balances) {
			t.Fatal("encounter did not consume its AP and grant its episode reward")
		}
		read(ep.V(3))
	}
	if giftsRead == 0 {
		t.Fatal("conversation never exercised a gift reward")
	}
	before = must("/DatingInfo")
	s = open()
	if !bytes.Equal(before, must("/DatingInfo")) {
		t.Fatal("completed conversation and claimed gift did not survive login")
	}
}
func TestNewRelationshipRejectsPurchasedEncounterAP(t *testing.T) {
	d := liveDesign(t)
	var group uint64
	for g, c := range d.CostumeGroup {
		if c.V(3) == 1 && len(d.Episodes.Groups[g]) > 1 {
			group = g
			break
		}
	}
	if group == 0 {
		t.Fatal("current GameData has no new relationship")
	}
	ctx := command.Context{Identity: command.Identity{SessionID: "new-relation"}, State: stateio.NewMemory()}
	money := &ledger{balances: map[[2]uint64]uint64{{42, 0}: 10}}
	s, e := Open(ctx, stateio.RootStore{}, d, money)
	if e != nil {
		t.Fatal(e)
	}
	s.AttachFirstMeeting(func(_ command.Context, g uint64) (bool, error) { return g == group, nil })
	seq := uint64(0)
	call := func(path string, args ...uint64) error {
		seq++
		b := wire.AppendVarint(nil, 1, seq)
		for i, v := range args {
			b = wire.AppendVarint(b, i+2, v)
		}
		_, _, _, e := s.Handle(ctx, path, b)
		return e
	}
	must := func(path string, args ...uint64) {
		t.Helper()
		if e := call(path, args...); e != nil {
			t.Fatalf("%s: %v", path, e)
		}
	}
	must("/DatingEpisodeClear", group, 1)
	for _, episodeID := range []uint64{1, 2} {
		r, _ := d.Episodes.Row(group, episodeID)
		msg := r.V(3)
		if episodeID == 2 {
			msg = r.V(2)
		}
		for _, id := range d.Messages.Groups[msg] {
			m, _ := d.Messages.Row(msg, id)
			// The client sends zero when either answer text id is empty.
			text := uint64(0)
			if m.V(5) == 1 && m.V(9) == 1 && m.V(3) != 0 && m.V(4) != 0 {
				text = m.V(3)
			}
			must("/DatingMessageUpdate", msg, id, text)
			if m.V(5) == 3 {
				must("/DatingMessageReward", msg, id)
			}
		}
	}
	before := maps.Clone(money.balances)
	if e := call("/DatingEpisodeClear", group, 2); e == nil {
		t.Fatal("purchased AP paid for a new relationship")
	}
	if !maps.Equal(before, money.balances) {
		t.Fatal("rejected encounter changed balances")
	}
	money.balances[[2]uint64{41, 0}] = 1
	must("/DatingEpisodeClear", group, 2)
	if money.balances[[2]uint64{41, 0}] != 0 || money.balances[[2]uint64{42, 0}] != 10 {
		t.Fatal("new relationship consumed the wrong AP")
	}
}
