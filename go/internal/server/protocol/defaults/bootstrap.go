package feature

import "bd2server/internal/server/protocol/wire"

// initialResponses are locally constructed, typed defaults for the
// new-player account. These do not reuse recorded response bytes. Stateful
// entities (inventory, characters, quests, decks) are deliberately excluded.
func initialResponse(path string) (int, []byte, bool) {
	switch path {
	case "/AvatarInfo":
		return 467, wire.AppendBytes(nil, 1, nil), true
	case "/GuildRaidSeasonReward":
		return 310, wire.AppendBytes(nil, 1, nil), true
	}
	return 0, nil, false
}
