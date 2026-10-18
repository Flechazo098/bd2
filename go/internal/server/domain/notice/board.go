// Package notice serves the client's news board (/NoticeInfo and
// /NoticeDetailInfo). Notices come from named sources, such as the release
// changelog and scheduled maintenance, which replace their own entries
// independently. Every change advances a sequence that the server pushes in
// Notify.notice_last_seq, so online clients fetch only what changed.
package notice

import (
	"maps"
	"slices"
	"sync"
	"time"

	"bd2server/internal/common/i18n"
)

// Type values follow the client's Define_NoticeType.
type Type int

const (
	TypeNotice        Type = 0
	TypeEvent         Type = 1
	TypeUpdate        Type = 2
	TypeSurvey        Type = 3
	TypeEmergency     Type = 4
	TypeDeveloperNote Type = 5
)

type Block struct {
	Image bool
	Value string
}

type Notice struct {
	ID        int32
	SubType   int32
	Type      Type
	Thumbnail string
	Start     time.Time
	End       time.Time
	URL       string
	Pinned    bool
	Sort      int32
	// Text is keyed by locale; the client is answered in its game language,
	// falling back to en_us.
	Text map[string]Content
}

type Content struct {
	Title  string
	Blocks []Block
}

// In picks locale, then en_us, then any written language.
func (n Notice) In(locale string) Content {
	if c, ok := n.Text[locale]; ok {
		return c
	}
	if c, ok := n.Text[i18n.Fallback]; ok {
		return c
	}
	for _, l := range i18n.Locales {
		if c, ok := n.Text[l]; ok {
			return c
		}
	}
	return Content{}
}

type entry struct {
	notice   Notice
	source   string
	revision int32
}

// Board is safe for concurrent use.
type Board struct {
	mu        sync.RWMutex
	entries   []entry
	sequence  int32
	now       func() time.Time
	languages map[string]string
}

// sequenceEpoch keeps sequences positive int32 values that keep increasing
// across restarts, because a reconnecting client may remember a sequence
// from the previous process.
var sequenceEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func NewBoard(now func() time.Time) *Board {
	if now == nil {
		now = time.Now
	}
	b := &Board{now: now, languages: map[string]string{}}
	b.sequence = b.nextSequence()
	return b
}

func (b *Board) nextSequence() int32 {
	next := int32(b.now().Sub(sequenceEpoch) / time.Second)
	if next <= b.sequence {
		next = b.sequence + 1
	}
	return next
}

// Replace sets the notices owned by source. Unchanged notices keep their
// revision, so incremental fetches return only new or edited ones.
func (b *Board) Replace(source string, notices []Notice) {
	b.mu.Lock()
	defer b.mu.Unlock()
	previous := map[int32]entry{}
	kept := b.entries[:0:0]
	for _, e := range b.entries {
		if e.source == source {
			previous[e.notice.ID] = e
		} else {
			kept = append(kept, e)
		}
	}
	changed := len(previous) != len(notices)
	var revision int32
	for _, n := range notices {
		old, ok := previous[n.ID]
		if ok && equal(old.notice, n) {
			kept = append(kept, old)
			continue
		}
		changed = true
		if revision == 0 {
			revision = b.nextSequence()
		}
		kept = append(kept, entry{notice: n, source: source, revision: revision})
	}
	if !changed {
		return
	}
	if revision == 0 {
		revision = b.nextSequence()
	}
	b.sequence = revision
	b.entries = kept
}

func equal(a, b Notice) bool {
	return a.ID == b.ID && a.SubType == b.SubType && a.Type == b.Type && a.Thumbnail == b.Thumbnail &&
		a.Start.Equal(b.Start) && a.End.Equal(b.End) && a.URL == b.URL && a.Pinned == b.Pinned && a.Sort == b.Sort &&
		maps.EqualFunc(a.Text, b.Text, func(x, y Content) bool { return x.Title == y.Title && slices.Equal(x.Blocks, y.Blocks) })
}

// SetLanguage records the game language an account logged in with.
func (b *Board) SetLanguage(account, locale string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.languages[account] = locale
}

// Language is the account's game language, or en_us before it logged in.
func (b *Board) Language(account string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if locale, ok := b.languages[account]; ok {
		return locale
	}
	return i18n.Fallback
}

// Sequence is the value pushed to clients as Notify.notice_last_seq.
func (b *Board) Sequence() int32 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.sequence
}

// Since lists notices that have started, have not ended and changed after
// the given sequence; zero lists them all.
func (b *Board) Since(sequence int32) []Notice {
	now := b.now()
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []Notice
	for _, e := range b.entries {
		if e.revision > sequence && !e.notice.Start.After(now) && e.notice.End.After(now) {
			out = append(out, e.notice)
		}
	}
	return out
}

func (b *Board) Find(id int32) (Notice, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, e := range b.entries {
		if e.notice.ID == id {
			return e.notice, true
		}
	}
	return Notice{}, false
}
