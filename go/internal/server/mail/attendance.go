package mail

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"bd2server/internal/server/gamedata"
)

// AttendanceRewardEconomy resolves attachment wrappers only when claimed and
// applies every reward to its owning domain under the account transaction.
type AttendanceRewardEconomy interface {
	Apply(string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}

func (s *Service) AttachAttendanceRewardEconomy(e AttendanceRewardEconomy) error {
	if s == nil || e == nil {
		return errors.New("mail: invalid attendance reward economy")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attendanceEconomy = e
	return nil
}

// IssueAttachmentsOnce implements the eventtasks attendance issuer. No wallet,
// inventory mutation or random reward selection occurs during mail creation.
func (s *Service) IssueAttachmentsOnce(identity, title, body string, rewards []gamedata.Reward, sentAt time.Time) error {
	if s == nil {
		return errors.New("mail: attendance issuer unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attendanceEconomy == nil {
		return errors.New("mail: attendance reward economy unavailable")
	}
	if strings.TrimSpace(identity) == "" || identity != strings.TrimSpace(identity) || len(identity) > 450 || strings.TrimSpace(title) == "" || len(title) > 2000 || strings.TrimSpace(body) == "" || len(body) > 20000 || sentAt.UnixMilli() <= 0 || sentAt.UnixMilli() > math.MaxInt64-int64(30*24*time.Hour/time.Millisecond) {
		return errors.New("mail: invalid attendance mail")
	}
	if err := s.validateAttendanceAttachments(rewards); err != nil {
		return err
	}
	key := "attendance:" + identity
	if id, exists := s.issued[key]; exists {
		previous := s.dynamic[id]
		if previous.Title != title || previous.Body != body || !reflect.DeepEqual(mailAttachments(previous), rewards) {
			return errors.New("mail: attendance identity reused with different attachments")
		}
		return nil
	}
	return s.enqueueCompensations([]compensation{{identity: key, title: title, body: body, rewards: rewards, sentAt: sentAt}})
}

func (s *Service) isAttendanceMail(id uint64) bool {
	for identity, mailID := range s.issued {
		if mailID == id && strings.HasPrefix(identity, "attendance:") {
			return true
		}
	}
	return false
}

func mailAttachments(entry MailDBInfo) []gamedata.Reward {
	rewards := make([]gamedata.Reward, len(entry.RewardTypes))
	for i, typ := range entry.RewardTypes {
		rewards[i] = gamedata.Reward{Type: typ, ID: entry.RewardIDs[i], Count: entry.RewardCounts[i]}
	}
	return rewards
}

func (s *Service) validateAttendanceAttachments(rewards []gamedata.Reward) error {
	if len(rewards) == 0 {
		return errors.New("mail: empty attendance attachments")
	}
	currencies := map[uint64]bool{2: true, 3: true, 4: true, 12: true, 15: true, 16: true, 18: true, 20: true, 21: true, 22: true, 23: true, 24: true, 30: true, 31: true, 32: true, 33: true, 43: true, 44: true, 60: true, 68: true}
	items := map[uint64]bool{5: true, 7: true, 8: true, 9: true, 10: true, 11: true, 13: true, 14: true, 17: true, 19: true, 25: true, 26: true, 27: true, 29: true, 34: true, 45: true, 46: true, 47: true, 49: true}
	for _, r := range rewards {
		if r.Count == 0 || r.Count > math.MaxInt32 || r.ID > math.MaxInt32 || (!currencies[r.Type] && !items[r.Type]) || (currencies[r.Type] && r.ID != 0) || (items[r.Type] && r.ID == 0) {
			return fmt.Errorf("mail: invalid attendance reward %d:%d:%d", r.Type, r.ID, r.Count)
		}
		if r.Type == 19 && !s.supportedItemDBInfoReward(r) {
			return errors.New("mail: unsupported attendance content ticket")
		}
	}
	return nil
}
