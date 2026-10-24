package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	chatpolicy "bd2server/internal/server/domain/chat"
	"bd2server/internal/server/domain/social"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/room"
	"bd2server/internal/server/storage/shared"
)

type guildChatRecord struct {
	Owner uint64
	AtMS  int64
	Body  []byte
}
type guildChatReceipt struct{ Digest string }

// The native Chat connection also carries matchmaking packets.
type realtimeChat struct {
	rooms interface {
		Handle(context.Context, room.Connection, room.Frame) ([]room.Delivery, error)
		Disconnect(context.Context, room.Connection) ([]room.Delivery, error)
	}
	shared       *sharedRuntime
	rules        room.TextRules
	historyCount int
	historyAge   time.Duration
	emojis       map[uint64]bool
	now          func() time.Time
	mu           sync.Mutex
	connections  map[string]room.Connection
}

func chatJSON(tx *shared.Tx, bucket, key string, value any) (bool, error) {
	raw, found, err := tx.Get(bucket, key)
	if err != nil || !found {
		return found, err
	}
	return true, json.Unmarshal(raw, value)
}
func putChatJSON(tx *shared.Tx, bucket, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return tx.Put(bucket, key, raw)
}
func chatScalar(raw []byte, n int) uint64 { v, _, _ := wire.Varint(raw, n); return v }
func chatBytes(raw []byte, n int) []byte  { v, _, _ := wire.Bytes(raw, n); return v }
func chatPolicy(tx *shared.Tx, account string) (chatpolicy.Policy, error) {
	var p chatpolicy.Policy
	_, err := chatJSON(tx, "chat.policy", account, &p)
	return p, err
}

func canonicalChatUser(tx *shared.Tx, p room.Principal, language uint64) ([]byte, uint64, error) {
	var published social.FriendProfile
	found, err := chatJSON(tx, "friend.directory", fmt.Sprintf("%020d", p.OwnerIndex), &published)
	if err != nil {
		return nil, 0, err
	}
	if found && published.AccountID != p.AccountID {
		return nil, 0, fmt.Errorf("%w: owner %d belongs to %q, chat principal is %q", room.ErrUnauthorized, p.OwnerIndex, published.AccountID, p.AccountID)
	}
	var account social.Account
	if _, err := chatJSON(tx, "social.accounts", p.AccountID, &account); err != nil {
		return nil, 0, err
	}
	guildID := account.GuildID
	if guildID != 0 {
		var guild social.Guild
		found, err := chatJSON(tx, "social.guilds", fmt.Sprintf("%020d", guildID), &guild)
		if err != nil {
			return nil, 0, err
		}
		member, ok := guild.Members[p.AccountID]
		if !found || !ok || member.OwnerIndex != uint64(p.OwnerIndex) {
			return nil, 0, fmt.Errorf("%w: %s is not owner %d in guild %d (present=%t)", room.ErrUnauthorized, p.AccountID, p.OwnerIndex, guildID, found)
		}
		if published.AccountID == "" {
			published.Snapshot.UserID = member.Name
			published.Snapshot.PortraitCostumeID = member.PortraitID
			published.Snapshot.PortraitDesignID = member.PortraitDesignID
			published.Snapshot.TitleID = member.TitleID
		}
	}
	u := wire.AppendVarint(nil, 1, uint64(p.OwnerIndex))
	u = wire.AppendVarint(u, 2, guildID)
	u = wire.AppendString(u, 4, published.Snapshot.UserID)
	u = wire.AppendVarint(u, 5, published.Snapshot.PortraitCostumeID)
	u = wire.AppendVarint(u, 6, published.Snapshot.PortraitDesignID)
	u = wire.AppendVarint(u, 7, published.Snapshot.TitleID)
	return wire.AppendVarint(u, 10, language), guildID, nil
}
func chatMessage(now time.Time, user []byte, code uint64, body []byte) []byte {
	h := wire.AppendVarint(nil, 1, uint64(now.Unix()))
	h = wire.AppendVarint(h, 2, code)
	h = wire.AppendBytes(h, 3, user)
	return append(wire.AppendBytes(nil, 1, h), body...)
}
func chatReply(c room.Connection, f room.Frame, now time.Time, user []byte, code uint64, body []byte) room.Delivery {
	return room.Delivery{ConnectionID: c.ID, Frame: room.Frame{Type: f.Type + 1, Sequence: f.Sequence, OwnerIndex: c.Principal.OwnerIndex, Payload: chatMessage(now, user, code, body)}}
}
func (r *realtimeChat) Handle(ctx context.Context, c room.Connection, f room.Frame) ([]room.Delivery, error) {
	if c.Channel != room.Matching && f.Type != 1 && f.Type != 5 && f.Type != 11 {
		return r.rooms.Handle(ctx, c, f)
	}
	switch f.Type {
	case 1, 5, 11, 13, 15, 18, 10, 180, 188:
	default:
		return r.rooms.Handle(ctx, c, f)
	}
	if f.UDP || f.OwnerIndex != 0 && f.OwnerIndex != c.Principal.OwnerIndex {
		return nil, fmt.Errorf("%w: chat frame %d names owner %d over udp=%t, connection owner is %d", room.ErrUnauthorized, f.Type, f.OwnerIndex, f.UDP, c.Principal.OwnerIndex)
	}
	if err := wire.Walk(f.Payload, func(wire.Field) error { return nil }); err != nil {
		return nil, err
	}
	clientUser := chatBytes(chatBytes(f.Payload, 1), 3)
	if owner := chatScalar(clientUser, 1); owner != 0 && owner != uint64(c.Principal.OwnerIndex) {
		return nil, fmt.Errorf("%w: chat user names owner %d, connection owner is %d", room.ErrUnauthorized, owner, c.Principal.OwnerIndex)
	}
	if f.Type == 10 || f.Type == 18 {
		return nil, nil
	}
	if f.Type == 15 {
		code := uint64(0)
		if err := r.validateGuildPayload(ctx, c, f.Payload); err != nil {
			code = 2
		}
		if f.Sequence <= 0 {
			code = 2
		}
		if code != 0 {
			return []room.Delivery{chatReply(c, f, r.now(), wire.AppendVarint(nil, 1, uint64(c.Principal.OwnerIndex)), code, nil)}, nil
		}
	}
	now := r.now()
	var deliveries []room.Delivery
	transact := r.shared.store.View
	if f.Type == 15 {
		transact = r.shared.store.Update
	}
	err := transact(ctx, func(tx *shared.Tx) error {
		user, guildID, err := canonicalChatUser(tx, c.Principal, chatScalar(clientUser, 10))
		if err != nil {
			return err
		}
		if f.Type == 1 || f.Type == 5 || f.Type == 11 || f.Type == 180 {
			deliveries = append(deliveries, chatReply(c, f, now, user, 0, nil))
			return nil
		}
		if f.Type == 188 {
			sender := chatScalar(f.Payload, 2)
			at := chatScalar(f.Payload, 4)
			translationUser := wire.AppendVarint(nil, 1, sender)
			body := wire.AppendString(nil, 2, string(chatBytes(f.Payload, 3)))
			body = wire.AppendVarint(body, 3, at)
			deliveries = append(deliveries, chatReply(c, f, now, translationUser, 17, body))
			return nil
		}
		if guildID == 0 {
			deliveries = append(deliveries, chatReply(c, f, now, user, 3, nil))
			return nil
		}
		policy, err := chatPolicy(tx, c.Principal.AccountID)
		if err != nil {
			return err
		}
		key := fmt.Sprintf("%020d", guildID)
		var history []guildChatRecord
		if _, err := chatJSON(tx, "chat.guild_history", key, &history); err != nil {
			return err
		}
		live := history[:0]
		for _, record := range history {
			if now.UnixMilli()-record.AtMS < int64(r.historyAge/time.Millisecond) {
				live = append(live, record)
			}
		}
		history = live
		if f.Type == 13 {
			var body []byte
			for _, record := range history {
				if !slices.Contains(policy.Blocked, record.Owner) {
					body = wire.AppendBytes(body, 2, record.Body)
				}
			}
			deliveries = append(deliveries, chatReply(c, f, now, user, 0, body))
			return nil
		}
		if policy.PenaltyUntilMS > now.UnixMilli() {
			deliveries = append(deliveries, chatReply(c, f, now, user, 2, nil))
			return nil
		}
		idRaw := sha256.Sum256([]byte(c.Principal.AccountID + "\x00" + c.Principal.SessionID + "\x00" + fmt.Sprint(f.Sequence)))
		id := hex.EncodeToString(idRaw[:])
		digestRaw := sha256.Sum256(f.Payload)
		digest := hex.EncodeToString(digestRaw[:])
		var receipt guildChatReceipt
		found, err := chatJSON(tx, "chat.guild_receipts", id, &receipt)
		if err != nil {
			return err
		}
		if found {
			code := uint64(0)
			if receipt.Digest != digest {
				code = 2
			}
			deliveries = append(deliveries, chatReply(c, f, now, user, code, nil))
			return nil
		}
		at := now.UnixMilli()
		if len(history) > 0 && at <= history[len(history)-1].AtMS {
			at = history[len(history)-1].AtMS + 1
		}
		body := wire.AppendVarint(nil, 2, chatScalar(f.Payload, 2))
		field := 3 + int(chatScalar(f.Payload, 2))
		body = wire.AppendBytes(body, field, chatBytes(f.Payload, field))
		body = wire.AppendVarint(body, 7, uint64(at))
		packet := chatMessage(now, user, 0, body)
		history = append(history, guildChatRecord{Owner: uint64(c.Principal.OwnerIndex), AtMS: at, Body: packet})
		if len(history) > r.historyCount {
			history = history[len(history)-r.historyCount:]
		}
		if err := putChatJSON(tx, "chat.guild_history", key, history); err != nil {
			return err
		}
		if err := putChatJSON(tx, "chat.guild_receipts", id, guildChatReceipt{Digest: digest}); err != nil {
			return err
		}
		deliveries = append(deliveries, chatReply(c, f, now, user, 0, nil))
		var guild social.Guild
		if _, err := chatJSON(tx, "social.guilds", key, &guild); err != nil {
			return err
		}
		var recipients []room.Connection
		r.mu.Lock()
		for account := range guild.Members {
			if recipient, found := r.connections[account]; found {
				recipients = append(recipients, recipient)
			}
		}
		r.mu.Unlock()
		for _, recipient := range recipients {
			_, recipientGuild, err := canonicalChatUser(tx, recipient.Principal, 0)
			if errors.Is(err, room.ErrUnauthorized) {
				continue
			}
			if err != nil {
				return err
			}
			if recipientGuild != guildID {
				continue
			}
			recipientPolicy, err := chatPolicy(tx, recipient.Principal.AccountID)
			if err != nil {
				return err
			}
			if slices.Contains(recipientPolicy.Blocked, uint64(c.Principal.OwnerIndex)) {
				continue
			}
			deliveries = append(deliveries, room.Delivery{ConnectionID: recipient.ID, Frame: room.Frame{Type: 17, OwnerIndex: c.Principal.OwnerIndex, Payload: packet}})
		}
		return nil
	})
	if err == nil && f.Type == 11 && c.Channel == room.Matching {
		r.mu.Lock()
		if r.connections == nil {
			r.connections = map[string]room.Connection{}
		}
		r.connections[c.Principal.AccountID] = c
		r.mu.Unlock()
	}
	return deliveries, err
}
func (r *realtimeChat) validateGuildPayload(ctx context.Context, c room.Connection, raw []byte) error {
	if len(raw) > 128000 || c.Principal.OwnerIndex <= 0 {
		return fmt.Errorf("%w: guild chat payload of %d bytes from owner %d", room.ErrUnauthorized, len(raw), c.Principal.OwnerIndex)
	}
	typ := chatScalar(raw, 2)
	if typ > 2 {
		return fmt.Errorf("%w: guild chat type %d, want 0..2", wire.ErrMalformed, typ)
	}
	for field := 3; field <= 6; field++ {
		if field != 3+int(typ) && len(chatBytes(raw, field)) != 0 {
			return fmt.Errorf("%w: guild chat type %d carries field %d", wire.ErrMalformed, typ, field)
		}
	}
	value := chatBytes(raw, 3+int(typ))
	if len(value) == 0 {
		return fmt.Errorf("%w: guild chat type %d has no value", wire.ErrMalformed, typ)
	}
	if err := wire.Walk(value, func(wire.Field) error { return nil }); err != nil {
		return err
	}
	if typ == 0 {
		text := string(chatBytes(value, 1))
		if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 100 {
			return fmt.Errorf("%w: guild chat text is empty, not UTF-8 or longer than 100 characters", wire.ErrMalformed)
		}
		return r.rules.CheckRoomText(ctx, c.Principal, text)
	}
	if typ == 1 && !r.emojis[chatScalar(value, 1)] {
		return fmt.Errorf("%w: guild chat emoji %d is unknown", wire.ErrMalformed, chatScalar(value, 1))
	}
	if typ == 2 {
		name := string(chatBytes(value, 2))
		if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 100 {
			return fmt.Errorf("%w: guild chat name is not UTF-8 or longer than 100 characters", wire.ErrMalformed)
		}
		if err := wire.Walk(value, func(f wire.Field) error {
			if f.Number == 1 {
				_, characterPresent, characterErr := wire.Bytes(f.Value, 1)
				if f.Type != 2 || !characterPresent || characterErr != nil {
					return fmt.Errorf("%w: guild chat share lacks its character", wire.ErrMalformed)
				}
				return wire.Walk(f.Value, func(nested wire.Field) error {
					if nested.Number >= 1 && nested.Number <= 4 {
						if nested.Type != 2 {
							return fmt.Errorf("%w: guild chat share field %d has wire type %d", wire.ErrMalformed, nested.Number, nested.Type)
						}
						return wire.Walk(nested.Value, func(wire.Field) error { return nil })
					}
					return nil
				})
			}
			return nil
		}); err != nil {
			return err
		}
		if name != "" {
			return r.rules.CheckRoomText(ctx, c.Principal, name)
		}
	}
	return nil
}
func (r *realtimeChat) Disconnect(ctx context.Context, c room.Connection) ([]room.Delivery, error) {
	r.mu.Lock()
	if current := r.connections[c.Principal.AccountID]; current.ID == c.ID {
		delete(r.connections, c.Principal.AccountID)
	}
	r.mu.Unlock()
	return r.rooms.Disconnect(ctx, c)
}

func (s *sharedRuntime) consumeChatPolicy(ctx context.Context, source string, raw []byte) error {
	var p chatpolicy.Policy
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.AccountID != source || p.OwnerIndex == 0 || p.Version == 0 {
		return fmt.Errorf("%w: chat policy of %q (owner %d, version %d) arrived from %q", social.ErrPermission, p.AccountID, p.OwnerIndex, p.Version, source)
	}
	return s.store.Update(ctx, func(tx *shared.Tx) error {
		old, err := chatPolicy(tx, source)
		if err != nil {
			return err
		}
		if old.Version >= p.Version {
			return nil
		}
		return putChatJSON(tx, "chat.policy", source, p)
	})
}
