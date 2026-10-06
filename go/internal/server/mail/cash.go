package mail

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

// Cash attachments are already resolved at purchase, so manual boxes must not
// be expanded again and selected random rewards must not be rolled on claim.
type CashRewardEconomy interface {
	ApplyResolved(string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}

func (s *Service) AttachCashRewards(e CashRewardEconomy, templates map[uint64]bool) error {
	if e == nil || len(templates) == 0 {
		return errors.New("mail: missing cash economy or templates")
	}
	s.cashEconomy = e
	s.cashTemplates = make(map[uint64]bool, len(templates))
	maps.Copy(s.cashTemplates, templates)
	return nil
}
func (s *Service) IssueCashOnce(identity string, template uint64, rewards []gamedata.Reward, sentAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	return s.enqueueCompensations([]compensation{{identity: key, templateID: template, isCash: true, rewards: rewards, sentAt: sentAt}})
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
func (s *Service) cashInfo(request []byte) ([]byte, error) {
	start, _, err := wire.Varint(request, 2)
	if err != nil || start > math.MaxInt64 {
		return nil, errors.New("mail: invalid cash cursor")
	}
	count, present, err := wire.Varint(request, 3)
	if err != nil || !present || count == 0 || count > math.MaxInt32 {
		return nil, errors.New("mail: invalid cash select count")
	}
	if count > mailHistoryPageMax {
		count = mailHistoryPageMax
	}
	entries := map[uint64]MailDBInfo{}
	for _, entry := range s.Starter.Mails {
		if entry.IsCash && !containsID(s.state.Opened, entry.MailID) {
			entries[entry.MailID] = entry
		}
	}
	for id, entry := range s.dynamic {
		if entry.IsCash && !containsID(s.state.Opened, id) {
			entries[id] = entry
		}
	}
	ids := make([]uint64, 0, len(entries))
	var max uint64
	for id := range entries {
		ids = append(ids, id)
		if id > max {
			max = id
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	var result []byte
	var selected uint64
	for _, id := range ids {
		if start != 0 && id >= start {
			continue
		}
		if selected >= count {
			break
		}
		result = wire.AppendBytes(result, 1, entries[id].encode())
		selected++
	}
	result = wire.AppendVarint(result, 2, uint64(len(entries)))
	return wire.AppendVarint(result, 3, max), nil
}
