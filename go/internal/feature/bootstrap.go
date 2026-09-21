package feature

import "bd2server/internal/wire"

// initialResponses are locally constructed, typed defaults for the 2.34.13
// new-player account. These do not reuse recorded response bytes. Stateful
// entities (inventory, characters, quests, decks) are deliberately excluded.
func initialResponse(path string) (int, []byte, bool) {
	switch path {
	case "/PackInfo":
		// No purchased packs yet. SquareSceneId is the new-account square.
		return 4, wire.AppendVarint(nil, 5, 3), true
	case "/RecipeInfo":
		// The starter crafting recipe is ID 101 (packed repeated int32).
		return 46, wire.AppendBytes(nil, 2, []byte{101}), true
	case "/CashMailInfo":
		// MaxInvenIndex is explicitly present even with zero cash mails.
		return 140, wire.AppendVarint(nil, 3, 0), true
	case "/AvatarInfo":
		return 467, wire.AppendBytes(nil, 1, nil), true
	case "/GuildRaidSeasonReward":
		return 310, wire.AppendBytes(nil, 1, nil), true
	case "/DeckInfo":
		// No active deck yet; talent slots are four zero-valued packed ints.
		return 8, wire.AppendBytes(nil, 2, []byte{0, 0, 0, 0}), true
	case "/RootSortIdInfo":
		entry := wire.AppendVarint(nil, 1, 2)
		entry = wire.AppendVarint(entry, 2, 140)
		entry = wire.AppendVarint(entry, 3, 1)
		return 367, wire.AppendBytes(nil, 1, entry), true
	}
	return 0, nil, false
}
