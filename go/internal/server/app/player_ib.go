package app

import (
	"fmt"
	"time"

	"bd2server/internal/server/design/schedule"
	"bd2server/internal/server/domain/battle/ib"
	"bd2server/internal/server/domain/command"
)

func (p *playerAssembly) openIB(ctx command.Context) error {
	var err error
	p.ibService, err = ib.Open(ctx, p.gameplayStore, p.design.ib, p.eventEconomy, ib.Options{CostumeLevel: func(_ command.Context, id uint64) (uint64, error) {
		costume, owned := p.collection.CostumeByID(id)
		if !owned {
			return 0, nil
		}
		return uint64(costume.Level), nil
	}, Period: func(command.Context) (ib.Period, error) {
		calendar := p.options.calendars.RegularService
		if calendar == nil {
			return ib.Period{}, fmt.Errorf("ib: missing regular calendar")
		}
		var regular uint64
		for _, entry := range calendar.Regular {
			if entry.ContentID == 9 {
				regular = entry.Season
				break
			}
		}
		now := uint64(time.Now().UnixMilli())
		for _, content := range calendar.Contents {
			if content.ID != 9 {
				continue
			}
			for _, season := range []schedule.Season{content.Current, content.Next} {
				if now >= season.StartMilliseconds && now < season.EndMilliseconds {
					return ib.Period{Season: season.ID, Regular: regular, Open: !season.Error}, nil
				}
			}
			return ib.Period{Season: content.Current.ID, Regular: regular}, nil
		}
		return ib.Period{}, fmt.Errorf("ib: missing content calendar")
	}})
	return err
}
