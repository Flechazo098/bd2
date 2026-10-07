package app

import (
	"bd2server/internal/server/domain/battle/monsterhunt"
	"bd2server/internal/server/protocol/staticdata"
)

func monsterHuntSeasons(seed *readonly.Seed) []monsterhunt.Season {
	var seasons []monsterhunt.Season
	for _, field := range seed.Responses["/MonsterHuntScheduleInfo"].Fields {
		if field.Number != 1 || field.Type != 2 {
			continue
		}
		var season monsterhunt.Season
		for _, value := range field.Fields {
			switch value.Number {
			case 1:
				for _, nested := range value.Fields {
					switch nested.Number {
					case 1:
						season.ID = nested.Varint
					case 2:
						season.Start = nested.Varint
					case 3:
						season.End = nested.Varint
					}
				}
			case 2:
				season.Hunt = value.Varint
			case 4:
				season.Calculate = value.Varint
			case 6:
				season.Independent = value.Varint != 0
			case 7:
				season.RankGroup = value.Varint
			}
		}
		seasons = append(seasons, season)
	}
	return seasons
}
