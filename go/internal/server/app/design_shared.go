package app

import (
	"fmt"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/design/schedule"
	"bd2server/internal/server/domain/battle/pvp"
)

func (d *designCatalog) loadSharedDesign(c *configuration) error {
	friendRules, err := gamedata.LoadFriendRules(c.gameData, c.gameDataVersion)
	if err != nil {
		return fmt.Errorf("load friend design: %w", err)
	}
	d.friends = friendRules
	d.chat, err = gamedata.LoadChatDesign(c.gameData, c.gameDataVersion)
	if err != nil {
		return fmt.Errorf("load chat design: %w", err)
	}
	design, err := gamedata.LoadPvpDesign(c.gameData, c.gameDataVersion)
	if err != nil {
		return fmt.Errorf("load PVP design: %w", err)
	}
	policy, err := pvp.NewLocalScorePolicy(design)
	if err != nil {
		return err
	}
	var seasons []pvp.Season
	for _, content := range c.calendars.RegularService.Contents {
		var mode pvp.Mode
		switch content.ID {
		case 1:
			mode = pvp.Mirror
		case 8:
			mode = pvp.Colosseum
		default:
			continue
		}
		seen := map[uint64]bool{}
		for _, s := range []schedule.Season{content.Current, content.Next} {
			if seen[s.ID] {
				continue
			}
			seen[s.ID] = true
			regular := false
			for _, marker := range c.calendars.RegularService.Regular {
				if marker.ContentID == content.ID && s.ID >= marker.Season {
					regular = true
				}
			}
			seasons = append(seasons, pvp.Season{Mode: mode, ID: s.ID, Start: s.StartMilliseconds, End: s.EndMilliseconds, CalculateEnd: s.EndMilliseconds + c.calendars.RegularService.CalculateMilliseconds, Regular: regular})
		}
	}
	d.pvp, err = pvp.NewRules(design, seasons, policy)
	if err != nil {
		return err
	}
	d.guild, err = gamedata.LoadGuildDesign(c.gameData, c.gameDataVersion)
	if err != nil {
		return fmt.Errorf("load guild design: %w", err)
	}
	return nil
}
