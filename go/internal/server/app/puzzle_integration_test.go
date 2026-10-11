//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/player"
)

type puzzleGameplay struct {
	t       *testing.T
	factory *PlayerFactory
	account string
	owner   *playerInstance
	seq     uint64
}

func (g *puzzleGameplay) request(path string, fields []byte) player.Command {
	g.seq++
	request := registryCommand(g.account, fmt.Sprintf("puzzle-%d", g.seq), path, append(appScalar(1, g.seq), fields...))
	if path == "/LoginUser" {
		request.LoginSessionKey = []byte("0123456789abcdef0123456789abcdef")
	}
	return request
}

func (g *puzzleGameplay) execute(request player.Command, wantFailure bool) []byte {
	g.t.Helper()
	reply, err := g.owner.Execute(context.Background(), request)
	if wantFailure {
		if err == nil {
			g.t.Fatalf("%s unexpectedly succeeded", request.Requests[0].Path)
		}
		if recoverErr := g.owner.Recover(context.Background()); recoverErr != nil {
			g.t.Fatal(recoverErr)
		}
		return []byte(err.Error())
	}
	if err != nil || len(reply.Responses) != 1 {
		g.t.Fatalf("%s: responses=%d error=%v", request.Requests[0].Path, len(reply.Responses), err)
	}
	return reply.Responses[0].Body
}

func (g *puzzleGameplay) call(path string, fields []byte) []byte {
	return g.execute(g.request(path, fields), false)
}

func (g *puzzleGameplay) reopen() {
	g.t.Helper()
	if err := g.owner.Close(); err != nil {
		g.t.Fatal(err)
	}
	g.owner = nil
	var err error
	g.owner, err = g.factory.open(g.account)
	if err != nil {
		g.t.Fatal(err)
	}
}

func (g *puzzleGameplay) itemCount(itemType, id uint64) uint64 {
	g.t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var count uint64
	for _, item := range g.owner.assembly.ownedItems.All(command.Context{State: tx}) {
		if item.Type == itemType && item.ID == id {
			count += item.Count
		}
	}
	return count
}

func (g *puzzleGameplay) wordCurrency() uint64 {
	g.t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	balances, err := g.owner.assembly.eventEconomy.AdditionalCurrencies(command.Context{State: tx})
	if err != nil {
		g.t.Fatal(err)
	}
	return balances[54]
}

func (g *puzzleGameplay) grantTokens(count uint64) {
	g.t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := command.Context{State: tx}
	if _, err := g.owner.assembly.ownedItems.GrantOnce(ctx, fmt.Sprintf("puzzle-budget-%d", g.seq), []gamedata.BattleReward{{Type: 8, ID: 2021, Count: count}}); err != nil {
		g.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		g.t.Fatal(err)
	}
}

func (g *puzzleGameplay) cost(field int, count uint64) []byte {
	g.t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	items, err := g.owner.assembly.ownedItems.SelectMutable(command.Context{State: tx}, 8, 2021, count)
	if err != nil {
		g.t.Fatal(err)
	}
	var fields []byte
	for _, item := range items {
		row := append(appScalar(1, item.InvenIndex), appScalar(2, item.ID)...)
		row = append(row, appScalar(3, item.Type)...)
		row = append(row, appScalar(4, item.Count)...)
		fields = append(fields, appMessage(field, row)...)
	}
	return fields
}

func puzzleNumbers(t *testing.T, row []byte, wanted int) []uint64 {
	t.Helper()
	var numbers []uint64
	err := wire.Walk(row, func(field wire.Field) error {
		if field.Number != wanted {
			return nil
		}
		if field.Type == 0 {
			value, _ := binary.Uvarint(field.Value)
			numbers = append(numbers, value)
			return nil
		}
		if field.Type != 2 {
			return wire.ErrMalformed
		}
		for value := field.Value; len(value) > 0; {
			number, length := binary.Uvarint(value)
			if length <= 0 {
				return wire.ErrMalformed
			}
			numbers = append(numbers, number)
			value = value[length:]
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return numbers
}

func (g *puzzleGameplay) info(uid uint64) []byte {
	g.t.Helper()
	rows := appRows(g.call("/MiniPuzzleInfo", appScalar(2, uid)), 1)
	if len(rows) != 1 || appValue(rows[0], 1) != uid {
		g.t.Fatalf("puzzle info does not identify schedule %d: %x", uid, rows)
	}
	return rows[0]
}

func puzzleResponseState(t *testing.T, response []byte, field int) []byte {
	t.Helper()
	rows := appRows(response, field)
	if len(rows) != 1 {
		t.Fatalf("puzzle response field %d: got %d rows", field, len(rows))
	}
	return rows[0]
}

func puzzleWordReward(t *testing.T, response []byte, itemType, id, count uint64) {
	t.Helper()
	bundle := puzzleResponseState(t, response, 5)
	rows := appRows(bundle, 1)
	if len(rows) != 1 || appValue(rows[0], 3) != itemType || appValue(rows[0], 2) != id || appValue(rows[0], 4) != count {
		t.Fatalf("wrong word reward: %x; want %d/%d x %d", bundle, itemType, id, count)
	}
}

// 2.36.7 MiniGamePuzzleUI.GetPossiblePlayCount bounds maximum-open by the
// available token balance and unopened cells. GameData 20261008155037 puzzle
// event 10 has 25-cell stages, token 8/2021 x 1, stage-1 word cells 1..7 with
// currency 44 x 500, stage-2 material 8/113 x 600, and a terminal stage 12.
// This exercises actual client requests, the production reward economy and
// the account SQLite transaction rather than an isolated imitation of play.
func TestPuzzleMaximumOpenWordsRenewalAndPersistence(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	g := &puzzleGameplay{t: t, factory: factory, account: accounts[0], owner: owner}
	t.Cleanup(func() {
		if g.owner != nil {
			if err := g.owner.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	var schedule events.Schedule
	for _, candidate := range owner.assembly.eventRegistry.List() {
		if candidate.Type == 17 && candidate.ID == 10 && candidate.Start <= time.Now().UnixMilli() && candidate.End > time.Now().UnixMilli() {
			schedule = candidate
			break
		}
	}
	if schedule.UID == 0 {
		t.Fatal("current puzzle event 10 schedule required")
	}
	uid := appScalar(2, schedule.UID)
	g.call("/LoginUser", nil)
	var loginTask []byte
	for _, row := range appRows(g.call("/EventMissionInfo", nil), 1) {
		if appValue(row, 1) == 30030 && appValue(row, 3) == 300301 {
			loginTask = row
		}
	}
	if appValue(loginTask, 4) != 1 || appValue(loginTask, 5) != 0 {
		t.Fatalf("puzzle login token mission did not complete: %x", loginTask)
	}
	mission := append(appScalar(3, 2), appScalar(4, 30030)...)
	mission = append(mission, appScalar(5, 300301)...)
	mission = append(mission, appScalar(6, 30030)...)
	claimRequest := g.request("/MissionClear", mission)
	beforeTokens := g.itemCount(8, 2021)
	g.execute(claimRequest, false)
	if got := g.itemCount(8, 2021); got != beforeTokens+2 {
		t.Fatalf("puzzle login mission delivered %d tokens, want %d", got, beforeTokens+2)
	}
	claimRequest.Identity.RequestID += "-replay"
	g.execute(claimRequest, false)
	if got := g.itemCount(8, 2021); got != beforeTokens+2 {
		t.Fatal("mission replay delivered tokens twice")
	}
	if beforeTokens != 0 {
		t.Fatal("fresh account unexpectedly contains puzzle tokens")
	}
	g.grantTokens(2)
	first := g.info(schedule.UID)
	board := puzzleNumbers(t, first, 3)
	sorted := slices.Clone(board)
	slices.Sort(sorted)
	if appValue(first, 2) != 1 || len(sorted) != 25 || sorted[0] != 1 || sorted[24] != 25 || len(slices.Compact(sorted)) != 25 {
		t.Fatalf("fresh stage has an invalid client board: %x", first)
	}
	bad := g.execute(g.request("/MiniPuzzleAllOpen", append(uid, g.cost(3, 3)...)), true)
	for _, detail := range []string{"/MiniPuzzleAllOpen", fmt.Sprintf("uid=%d", schedule.UID), "submitted=3", "expected=4", "unit_cost=1"} {
		if !strings.Contains(string(bad), detail) {
			t.Fatalf("cost diagnostic missing %q: %s", detail, bad)
		}
	}
	if !bytes.Equal(first, g.info(schedule.UID)) || g.itemCount(8, 2021) != 4 {
		t.Fatal("invalid maximum-open cost changed progress or inventory")
	}
	forged := append(appScalar(1, 1), appScalar(2, 2021)...)
	forged = append(forged, appScalar(3, 8)...)
	forged = append(forged, appScalar(4, 1)...)
	fields := append(slices.Clone(uid), appScalar(3, board[0])...)
	g.execute(g.request("/MiniPuzzleOpen", append(fields, appMessage(4, forged)...)), true)
	if !bytes.Equal(first, g.info(schedule.UID)) || g.itemCount(8, 2021) != 4 {
		t.Fatal("forged stack consumption changed puzzle progress or inventory")
	}
	maximum := g.request("/MiniPuzzleAllOpen", append(uid, g.cost(3, 4)...))
	response := g.execute(maximum, false)
	after := puzzleResponseState(t, response, 2)
	opened := puzzleNumbers(t, after, 4)
	if appValue(after, 2) != 1 || len(opened) != 4 || len(slices.Compact(slices.Sorted(slices.Values(opened)))) != 4 || g.itemCount(8, 2021) != 0 {
		t.Fatalf("four-token maximum-open did not open and charge four cells: %x", response)
	}
	if !bytes.Equal(first, puzzleResponseState(t, response, 1)) || !slices.Equal(board, puzzleNumbers(t, after, 3)) {
		t.Fatal("maximum-open rearranged the current board or lost its before snapshot")
	}
	g.reopen()
	if !bytes.Equal(after, g.info(schedule.UID)) {
		t.Fatal("restart lost partial puzzle progress")
	}
	maximum.Identity.RequestID += "-reopened-domain-replay"
	if got := g.execute(maximum, false); !bytes.Equal(got, response) || g.itemCount(8, 2021) != 0 {
		t.Fatal("restart replay changed the response or charged twice")
	}
	g.grantTokens(400)
	beforeWordCurrency := g.wordCurrency()
	var completed []byte
	for id := uint64(1); id <= 7; id++ {
		if slices.Contains(opened, id) {
			continue
		}
		fields := append(slices.Clone(uid), appScalar(3, id)...)
		fields = append(fields, g.cost(4, 1)...)
		result := g.call("/MiniPuzzleOpen", fields)
		opened = puzzleNumbers(t, puzzleResponseState(t, result, 2), 4)
		if len(appRows(result, 5)) > 0 {
			completed = result
		}
	}
	puzzleWordReward(t, completed, 44, 0, 500)
	if g.wordCurrency() != beforeWordCurrency+500 {
		t.Fatal("word completion did not credit its currency to durable account state")
	}
	if len(opened) >= 25 {
		t.Fatal("word-only fixture unexpectedly opened the entire board")
	}
	tokens := g.itemCount(8, 2021)
	renewed := g.call("/MiniPuzzleRenew", uid)
	if appValue(puzzleResponseState(t, renewed, 1), 2) != 1 || appValue(puzzleResponseState(t, renewed, 2), 2) != 2 || len(puzzleNumbers(t, puzzleResponseState(t, renewed, 2), 4)) != 0 || g.itemCount(8, 2021) != tokens {
		t.Fatal("word-complete renewal did not abandon incidental cells without charging")
	}
	beforeMaterial := g.itemCount(8, 113)
	fullRequest := g.request("/MiniPuzzleAllOpen", append(slices.Clone(uid), g.cost(3, 25)...))
	full := g.execute(fullRequest, false)
	puzzleWordReward(t, full, 8, 113, 600)
	if appValue(puzzleResponseState(t, full, 1), 2) != 2 || appValue(puzzleResponseState(t, full, 2), 2) != 3 || g.itemCount(8, 2021) != tokens-25 || g.itemCount(8, 113) != beforeMaterial+600 {
		t.Fatal("full second board did not settle its word reward and automatically advance")
	}
	g.reopen()
	fullRequest.Identity.RequestID += "-domain-reward-replay"
	if replay := g.execute(fullRequest, false); !bytes.Equal(replay, full) || g.itemCount(8, 2021) != tokens-25 || g.itemCount(8, 113) != beforeMaterial+600 || g.wordCurrency() != beforeWordCurrency+500 {
		t.Fatal("full-board replay after restart duplicated a cost or reward")
	}
	for stage := uint64(3); stage <= 12; stage++ {
		beforeTickets := g.itemCount(8, 1000)
		full = g.call("/MiniPuzzleAllOpen", append(slices.Clone(uid), g.cost(3, 25)...))
		wantStage := min(stage+1, uint64(12))
		if appValue(puzzleResponseState(t, full, 1), 2) != stage || appValue(puzzleResponseState(t, full, 2), 2) != wantStage {
			t.Fatalf("stage %d did not advance correctly: %x", stage, full)
		}
		if stage == 7 {
			puzzleWordReward(t, full, 8, 1000, 10)
			if g.itemCount(8, 1000) != beforeTickets+10 {
				t.Fatal("stage-seven word tickets were not credited to inventory")
			}
		}
	}
	puzzleWordReward(t, full, 4, 0, 100000)
	final := g.info(schedule.UID)
	if appValue(final, 2) != 12 || len(puzzleNumbers(t, final, 4)) != 25 {
		t.Fatal("terminal puzzle did not retain all completed cells")
	}
	tokens = g.itemCount(8, 2021)
	g.execute(g.request("/MiniPuzzleRenew", uid), true)
	g.execute(g.request("/MiniPuzzleAllOpen", uid), true)
	if !bytes.Equal(final, g.info(schedule.UID)) || g.itemCount(8, 2021) != tokens {
		t.Fatal("terminal operations changed assets or advanced beyond the final stage")
	}
	g.reopen()
	if !bytes.Equal(final, g.info(schedule.UID)) || g.itemCount(8, 2021) != tokens {
		t.Fatal("terminal puzzle progress or token balance changed after reopening SQLite")
	}
	calendar := g.owner.assembly.eventRegistry.List()
	for index := range calendar {
		if calendar[index].UID == schedule.UID {
			calendar[index].End = time.Now().Add(-time.Minute).UnixMilli()
		}
	}
	if err := g.owner.assembly.eventRegistry.Replace(calendar); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(final, g.info(schedule.UID)) {
		t.Fatal("expired info query erased previously completed progress")
	}
	closed := g.execute(g.request("/MiniPuzzleRenew", uid), true)
	if !strings.Contains(string(closed), "event is closed") || g.itemCount(8, 2021) != tokens {
		t.Fatal("expired puzzle allowed mutation or charged resources")
	}
}

// ResourceTable 2021's current stack limit is 9999. A real grant of 10001
// creates two stacks; consuming 9997 leaves two tokens in each. The native
// maximum-open request must sum both concrete stacks while each schedule
// retains its own board and open-cell history.
func TestPuzzleMaximumOpenUsesSplitStacksAndSeparateSchedules(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	g := &puzzleGameplay{t: t, factory: factory, account: accounts[0], owner: owner}
	t.Cleanup(func() {
		if g.owner != nil {
			if err := g.owner.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	calendar := owner.assembly.eventRegistry.List()
	var schedule events.Schedule
	for _, candidate := range calendar {
		if candidate.Type == 17 && candidate.ID == 10 {
			schedule = candidate
			break
		}
	}
	if schedule.UID == 0 {
		t.Fatal("current puzzle schedule required")
	}
	second := schedule
	second.UID = 2000000010
	calendar = append(calendar, second)
	if err := owner.assembly.eventRegistry.Replace(calendar); err != nil {
		t.Fatal(err)
	}
	uid := appScalar(2, schedule.UID)
	firstBoard, secondBoard := g.info(schedule.UID), g.info(second.UID)
	g.grantTokens(10001)
	tx, err := owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx := command.Context{State: tx}
	spent, err := owner.assembly.ownedItems.SelectMutable(ctx, 8, 2021, 9997)
	if err == nil {
		err = owner.assembly.ownedItems.Consume(ctx, spent)
	}
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	cost := g.cost(3, 4)
	stacks := appRows(cost, 3)
	if len(stacks) != 2 || appValue(stacks[0], 4) != 2 || appValue(stacks[1], 4) != 2 {
		t.Fatalf("split-stack fixture is not two independent two-token entries: %x", cost)
	}
	duplicate := append(appMessage(3, stacks[0]), appMessage(3, stacks[0])...)
	g.execute(g.request("/MiniPuzzleAllOpen", append(slices.Clone(uid), duplicate...)), true)
	if !bytes.Equal(firstBoard, g.info(schedule.UID)) || g.itemCount(8, 2021) != 4 {
		t.Fatal("duplicate submitted stack changed progress or token balance")
	}
	// A failed request rebuilds the production assembly from SQLite and its
	// configured calendar; reinstall only the second test schedule afterward.
	if err := g.owner.assembly.eventRegistry.Replace(calendar); err != nil {
		t.Fatal(err)
	}
	result := g.call("/MiniPuzzleAllOpen", append(slices.Clone(uid), cost...))
	if len(puzzleNumbers(t, puzzleResponseState(t, result, 2), 4)) != 4 || g.itemCount(8, 2021) != 0 || !bytes.Equal(secondBoard, g.info(second.UID)) {
		t.Fatal("maximum-open did not consume both stacks or mixed the two schedule states")
	}
	firstAfter := g.info(schedule.UID)
	g.grantTokens(1)
	board := puzzleNumbers(t, secondBoard, 3)
	fields := append(appScalar(2, second.UID), appScalar(3, board[0])...)
	fields = append(fields, g.cost(4, 1)...)
	result = g.call("/MiniPuzzleOpen", fields)
	if len(puzzleNumbers(t, puzzleResponseState(t, result, 2), 4)) != 1 || !bytes.Equal(firstAfter, g.info(schedule.UID)) || g.itemCount(8, 2021) != 0 {
		t.Fatal("single-open used another schedule's cells or did not charge its token")
	}
	g.reopen()
	if err := g.owner.assembly.eventRegistry.Replace(calendar); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstAfter, g.info(schedule.UID)) || len(puzzleNumbers(t, g.info(second.UID), 4)) != 1 {
		t.Fatal("SQLite restart merged independent puzzle schedule progress")
	}
}
