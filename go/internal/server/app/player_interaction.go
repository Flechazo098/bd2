package app

import "bd2server/internal/server/domain/command"

func (p *playerAssembly) interactionAvailable(ctx command.Context, group uint64) (bool, error) {
	for _, access := range p.design.interaction.Access[group] {
		if access.Prestige != 0 {
			for _, item := range p.ownedItems.All(ctx) {
				if item.Type == 45 && item.ID == access.Prestige && item.Count != 0 {
					return true, nil
				}
			}
			continue
		}
		if access.Costume != 0 {
			if _, owned := p.collection.CostumeByID(access.Costume); owned {
				return true, nil
			}
			continue
		}
		switch access.Provide {
		case 0:
			return true, nil
		case 1:
			if p.worldService.PackAvailable(ctx, access.Pack) {
				return true, nil
			}
		case 2:
			for _, item := range p.ownedItems.All(ctx) {
				if item.Type == 25 && item.ID == access.Item && item.Count != 0 {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
