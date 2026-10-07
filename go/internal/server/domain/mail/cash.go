package mail

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"strings"
	"time"
)

// Cash attachments are already resolved at purchase, so manual boxes must not
// be expanded again and selected random rewards must not be rolled on claim.
type CashRewardEconomy interface {
	ApplyResolved(ctx command.Context, _ string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error)
}

func (s *Service) AttachCashRewards(ctx command.Context, e CashRewardEconomy, templates map[uint64]bool) error {
	if e == nil || len(templates) == 0 {
		return errors.New("mail: missing cash economy or templates")
	}
	s.cashEconomy = e
	s.cashTemplates = make(map[uint64]bool, len(templates))
	maps.Copy(s.cashTemplates, templates)
	return nil
}
func (s *Service) IssueCashOnce(ctx command.Context, identity string, template uint64, rewards []gamedata.Reward, sentAt time.Time) error {

	if s.cashEconomy == nil || !s.cashTemplates[template] {
		return fmt.Errorf("mail: cash template %d or economy unavailable", template)
	}
	if identity == "" || strings.TrimSpace(identity) != identity || len(identity) > 450 || sentAt.UnixMilli() <= 0 || sentAt.UnixMilli() >= 253402300799000 {
		return errors.New("mail: invalid cash mail identity or time")
	}
	if err := validateCashAttachments(rewards); err != nil {
		return err
	}
	key := "cash:" + identity
	if id, ok := s.issued[key]; ok {
		previous := s.dynamic[id]
		if !previous.IsCash || previous.TemplateID != template || !reflect.DeepEqual(mailAttachments(previous), rewards) {
			return errors.New("mail: cash identity reused with different attachments")
		}
		return nil
	}
	return s.enqueueCompensations(ctx, []compensation{{identity: key, templateID: template, isCash: true, rewards: rewards, sentAt: sentAt}})
}
func validateCashAttachments(rewards []gamedata.Reward) error {
	if len(rewards) == 0 {
		return errors.New("mail: empty cash attachments")
	}
	for _, r := range rewards {
		if r.Type < 2 || r.Type > 72 || r.Count == 0 || r.Count > math.MaxInt32 || r.ID > math.MaxInt32 {
			return fmt.Errorf("mail: invalid cash attachment %+v", r)
		}
	}
	return nil
}
