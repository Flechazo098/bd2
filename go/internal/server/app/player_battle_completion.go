package app

import (
	"bd2server/internal/server/domain/battle"
	"bd2server/internal/server/domain/battle/pvp"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
)

func (p *playerAssembly) prepareBattle(ctx command.Context, result battle.Completion) error {
	if len(result.Deck) == 0 {
		return nil
	}
	for _, entry := range result.Deck {
		character, owned := p.worldService.CharacterService().Find(ctx, entry.CharacterInvenIndex)
		if !owned || roster.IsStoryCharacter(character) || roster.IsCharmCharacter(character) {
			return nil
		}
	}
	return p.deckStateStore.PrepareRecommendation(ctx, result.Identity, result.Context, result.Deck)
}

func (p *playerAssembly) completeBattle(ctx command.Context, result battle.Completion) ([]byte, error) {
	partners, err := p.rosterProgressService.ApplyBattlePartners(ctx, result.Identity, result.Participants, result.Win)
	if err != nil {
		return nil, err
	}
	var response []byte
	for _, partner := range partners {
		response = wire.AppendBytes(response, 9, roster.PartnerWire(partner))
	}
	return response, p.deckStateStore.CompleteRecommendation(ctx, result.Identity, result.Win)
}

func (p *playerAssembly) completePVP(ctx command.Context, mode pvp.Mode, identity string, win bool, enteredDeck [][]byte) ([]byte, error) {
	if mode != pvp.Mirror {
		return nil, nil
	}
	var indices []uint64
	for _, entry := range enteredDeck {
		index, _, err := wire.Varint(entry, 1)
		if err != nil {
			return nil, err
		}
		indices = append(indices, index)
	}
	partners, err := p.rosterProgressService.ApplyBattlePartners(ctx, "pvp:"+identity, indices, win)
	if err != nil {
		return nil, err
	}
	var response []byte
	for _, partner := range partners {
		response = wire.AppendBytes(response, 3, roster.PartnerWire(partner))
	}
	return response, p.deckStateStore.CompleteRecommendation(ctx, "pvp:"+identity, win)
}
