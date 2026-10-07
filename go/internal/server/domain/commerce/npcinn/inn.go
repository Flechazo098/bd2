// Package npcinn implements motel opening and paid/free recovery. Each request
// runs within the account transaction, including currency, health and receipts.
package npcinn

import "bd2server/internal/server/domain/command"

import (
	"bd2server/internal/server/design/gamedata"

	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/storage/stateio"
	"errors"
)

type Context func(ctx command.Context, pack, npc uint64) (gamedata.InnRule, uint64, error)
type reply struct {
	Digest string
	Body   []byte
}
type Service struct {
	store      stateio.Store
	characters *roster.CharacterStore
	wallet     *assets.Wallet
	context    Context
	level      func(ctx command.Context) (uint64, error)
	battle     func(ctx command.Context) bool
}

func New(store stateio.Store, characters *roster.CharacterStore, wallet *assets.Wallet, context Context, level func(ctx command.Context) (uint64, error), battle func(ctx command.Context) bool) (*Service, error) {
	if store == nil || characters == nil || wallet == nil || context == nil || level == nil || battle == nil {
		return nil, errors.New("npcinn: incomplete dependencies")
	}
	return &Service{store: store, characters: characters, wallet: wallet, context: context, level: level, battle: battle}, nil
}
