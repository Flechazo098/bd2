package npcinn

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

func (s *Service) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	code := 109
	if path == "/CharAllRevival" {
		code = 11
	} else if path != "/InnOpen" {
		return 0, nil, false, nil
	}

	fail := func(e error) (int, []byte, bool, error) { return code, nil, true, e }
	seq, ok, err := wire.Varint(request, 1)
	if err != nil || !ok || seq == 0 || seq > math.MaxInt32 || ctx.SessionID == "" {
		return fail(errors.New("npcinn: invalid sequence or session"))
	}
	if err = wire.Walk(request, func(wire.Field) error { return nil }); err != nil {
		return fail(err)
	}
	if s.battle(ctx) {
		return fail(errors.New("npcinn: unavailable during battle"))
	}
	if path == "/InnOpen" {
		_, state, err := s.context(ctx, 0, 0)
		if err != nil {
			return fail(err)
		}
		return code, wire.AppendVarint(nil, 1, state), true, nil
	}
	pack, _, err := wire.Varint(request, 2)
	if err != nil || pack == 0 || pack > math.MaxInt32 {
		return fail(errors.New("npcinn: invalid pack"))
	}
	npc, _, err := wire.Varint(request, 3)
	if err != nil || npc == 0 || npc > math.MaxInt32 {
		return fail(errors.New("npcinn: invalid NPC"))
	}
	keyBytes := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", ctx.SessionID, path, seq)))
	key := hex.EncodeToString(keyBytes[:])
	hash := sha256.Sum256(request)
	digest := hex.EncodeToString(hash[:])
	var replies map[string]reply
	data, err := s.store.Load(ctx.State, "npcinn")
	if err != nil {
		return fail(err)
	}
	if data != nil && json.Unmarshal(data, &replies) != nil {
		return fail(errors.New("npcinn: invalid receipt store"))
	}
	if replies == nil {
		replies = map[string]reply{}
	}
	if prior, exists := replies[key]; exists {
		if prior.Digest != digest {
			return fail(errors.New("npcinn: changed request replay"))
		}
		return code, prior.Body, true, nil
	}
	rule, state, err := s.context(ctx, pack, npc)
	if err != nil {
		return fail(err)
	}
	level, err := s.level(ctx)
	if err != nil {
		return fail(err)
	}
	var indices []uint64
	seen := map[uint64]bool{}
	err = wire.Walk(request, func(f wire.Field) error {
		if f.Number != 4 {
			return nil
		}
		if f.Type != 0 && f.Type != 2 {
			return errors.New("npcinn: invalid character list")
		}
		for raw := f.Value; len(raw) > 0; {
			idx, n := binary.Uvarint(raw)
			if n <= 0 || idx == 0 || idx > math.MaxInt64 || seen[idx] || len(indices) >= 4096 {
				return errors.New("npcinn: invalid character index")
			}
			indices = append(indices, idx)
			seen[idx] = true
			raw = raw[n:]
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if len(indices) == 0 {
		return fail(errors.New("npcinn: empty recovery targets"))
	}
	discount := uint64(0)
	if state == 2 {
		discount = rule.GoodDiscount
	}
	if discount > 100 || rule.Currency != 4 || rule.ItemCount == 0 || rule.ItemCount > math.MaxInt32 {
		return fail(errors.New("npcinn: invalid recovery design"))
	}
	rate := rule.ItemCount * (100 - discount)
	const divisor uint64 = 4000
	available := s.wallet.Snapshot(ctx).Gold
	var total uint64
	var targets []roster.Character
	for _, idx := range indices {
		c, found := s.characters.Find(ctx, idx)
		if !found {
			return fail(errors.New("npcinn: recovery character not owned"))
		}
		maximum, err := s.characters.MaxHealth(ctx, idx)
		if err != nil {
			return fail(err)
		}
		current, err := s.characters.CurrentHealth(ctx, idx)
		if err != nil {
			return fail(err)
		}
		if current >= maximum {
			c.HP = current
			targets = append(targets, c)
			continue
		}
		if current == 0 {
			c.HP = 1
			targets = append(targets, c)
			continue
		}
		if level <= rule.FreeSquadLevel || rate == 0 {
			c.HP = maximum
			targets = append(targets, c)
			continue
		}
		missing := maximum - current
		if missing > (math.MaxUint64-divisor+1)/rate {
			return fail(errors.New("npcinn: recovery price overflow"))
		}
		cost := (missing*rate + divisor - 1) / divisor
		if cost <= available {
			c.HP = maximum
		} else {
			if available > math.MaxUint64/divisor {
				return fail(errors.New("npcinn: recovery amount overflow"))
			}
			heal := min(available*divisor/rate, missing)
			cost = heal * rate / divisor
			c.HP = current + heal
		}
		available -= cost
		total += cost
		targets = append(targets, c)
	}
	if total > 0 {
		if _, err = s.wallet.SpendGoldOnce(ctx, "npcinn:"+key, total); err != nil {
			return fail(err)
		}
	}
	var out []byte
	for _, c := range targets {
		if err = s.characters.SetCurrentHealth(ctx, c.InvenIndex, c.HP); err != nil {
			return fail(err)
		}
		out = wire.AppendBytes(out, 1, roster.CharacterWire(c))
	}
	replies[key] = reply{Digest: digest, Body: out}
	data, err = json.Marshal(replies)
	if err != nil {
		return fail(err)
	}
	if err = s.store.Save(ctx.State, "npcinn", data); err != nil {
		return fail(err)
	}
	return code, out, true, nil
}
