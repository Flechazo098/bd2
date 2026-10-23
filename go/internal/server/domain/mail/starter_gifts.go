package mail

import (
	"fmt"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/startergift"
)

type starterGiftProvider func(command.Context) ([]startergift.Gift, error)

func (s *Service) AttachStarterGifts(ctx command.Context, provider func(command.Context) ([]startergift.Gift, error)) error {
	if s == nil || provider == nil {
		return fmt.Errorf("mail: unavailable starter gift provider")
	}
	s.starterGifts = provider
	return s.refreshStarterGifts(ctx)
}

func (s *Service) refreshStarterGifts(ctx command.Context) error {
	if s.starterGifts == nil {
		return nil
	}
	gifts, err := s.starterGifts(ctx)
	if err != nil {
		return err
	}
	if err := s.validateStarterGifts(); err != nil {
		return err
	}
	seen := map[string]bool{}
	var grants []compensation
	for _, gift := range gifts {
		if seen[gift.Identity] {
			return fmt.Errorf("mail: duplicate starter gift %q", gift.Identity)
		}
		seen[gift.Identity] = true
		if err := startergift.ValidateRewards(gift.Identity, gift.Rewards); err != nil {
			return err
		}
		if _, issued := s.issued[gift.Identity]; issued {
			continue
		}
		grant := compensation{identity: gift.Identity, title: gift.Title, body: gift.Body, rewards: gift.Rewards, sentAt: s.now().UTC()}
		if err := grant.validate(); err != nil {
			return err
		}
		grants = append(grants, grant)
	}
	return s.enqueueCompensations(ctx, grants)
}

func (s *Service) validateStarterGifts() error {
	for identity, id := range s.issued {
		if identity != startergift.LimitedCostumes && identity != startergift.PrestigeSkins && !startergift.IsAvatarGift(identity) {
			continue
		}
		entry := s.dynamic[id]
		if entry.MailID == 0 || len(entry.RewardTypes) != len(entry.RewardIDs) || len(entry.RewardTypes) != len(entry.RewardCounts) {
			return fmt.Errorf("mail: invalid saved starter gift %q", identity)
		}
		rewards := mailAttachments(entry)
		if err := startergift.ValidateRewards(identity, rewards); err != nil {
			return err
		}
		if identity == startergift.LimitedCostumes && s.costumeDesign != nil {
			for _, reward := range rewards {
				costume, ok := s.costumeDesign.Character(reward.ID)
				if !ok || costume.CostumeMaxLevel != 5 {
					return fmt.Errorf("mail: saved starter costume %d does not support +5", reward.ID)
				}
			}
		}
	}
	return nil
}

func (s *Service) unownedStarterAttachments(ctx command.Context, entry MailDBInfo) []gamedata.Reward {
	rewards := mailAttachments(entry)
	if !s.isStarterAvatarGift(entry.MailID) {
		return rewards
	}
	owned := map[uint64]bool{}
	for _, item := range s.inventory.All(ctx) {
		if item.Type == 49 && item.Count != 0 {
			owned[item.ID] = true
		}
	}
	var missing []gamedata.Reward
	for _, reward := range rewards {
		if !owned[reward.ID] {
			missing = append(missing, reward)
		}
	}
	return missing
}

func (s *Service) isStarterAvatarGift(mailID uint64) bool {
	for identity, id := range s.issued {
		if id == mailID && startergift.IsAvatarGift(identity) {
			return true
		}
	}
	return false
}
