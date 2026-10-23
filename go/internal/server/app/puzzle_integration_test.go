//go:build integration

package app

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
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
	ctx := command.Context{State: tx, InstanceIDs: g.factory.instanceIDs()}
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
	items, err := g.owner.assembly.ownedItems.SelectMutable(command.Context{State: tx, InstanceIDs: g.factory.instanceIDs()}, 8, 2021, count)
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
