//go:build integration

package app

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

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
