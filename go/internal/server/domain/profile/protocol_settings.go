package profile

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

var profileCodes = map[string]int{"/UserContentsInfo": 215, "/UserContentsInfoOption": 216, "/UpdateUserContentsInfoOption": 217, "/UserGreetingChange": 219, "/UserTitleChange": 225, "/UserPortraitChange": 75, "/IdCardSave": 446, "/IdCardShopInfo": 447, "/IdCardShopBuy": 448, "/IdCardPresetSave": 449, "/IdCardPresetInfo": 450, "/IdCardPresetDelete": 451, "/IdCardRecovery": 452, "/SupporterDetail": 434, "/SupporterRegister": 435, "/SupporterRemove": 436, "/SupporterStatus": 438, "/SupporterInfo": 443}

func (s *Service) handleSettings(ctx command.Context, path string, request []byte, seq uint64) (int, []byte, bool, error) {
	code := profileCodes[path]
	fail := func(err error) (int, []byte, bool, error) { return code, nil, true, err }
	state, err := s.load(ctx)
	if err != nil {
		return fail(err)
	}
	identity := fmt.Sprintf("%s:%s:%d", ctx.SessionID, path, seq)
	if ctx.SessionID == "" {
		return fail(fmt.Errorf("profile: missing session"))
	}
	receiptKey := "profile:receipt:" + identity
	economic := path == "/IdCardShopBuy" || path == "/IdCardRecovery"
	rememberEconomic := path == "/IdCardShopBuy" || path == "/IdCardRecovery" && !state.Settings.CardRecovered
	if economic {
		raw, err := s.store.Load(ctx.State, receiptKey)
		if err != nil {
			return fail(err)
		}
		if raw != nil {
			if err := stateio.RequireExactJSONObject(raw, "request", "response"); err != nil {
				return fail(err)
			}
			var r receipt
			if err := json.Unmarshal(raw, &r); err != nil {
				return fail(err)
			}
			if !bytes.Equal(r.Request, request) {
				return fail(fmt.Errorf("profile: conflicting retry"))
			}
			return code, append([]byte(nil), r.Response...), true, nil
		}
	}
	var out []byte
	mutation := false
	settings := &state.Settings
	switch path {
	case "/UserContentsInfoOption":
		if settings.IsAllPrivate {
			out = wire.AppendVarint(out, 1, 1)
		}
		for _, n := range settings.Options {
			out = wire.AppendVarint(out, 2, n)
		}
	case "/UpdateUserContentsInfoOption":
		private, e := profileScalar(request, 2, 1)
		if e != nil {
			return fail(e)
		}
		options, e := profileNumbers(request, 3)
		if e != nil || !validOptions(options) {
			return fail(fmt.Errorf("profile: invalid hidden options"))
		}
		sorts, e := profileNumbers(request, 4)
		if e != nil {
			return fail(e)
		}
		if len(sorts) == 0 {
			sorts = []uint64{0, 5, 1, 12, 6, 3, 4, 7, 8, 9, 10, 11}
		}
		if !validSort(sorts) {
			return fail(fmt.Errorf("profile: invalid display order"))
		}
		settings.IsAllPrivate, settings.Options, settings.SortIDs = private == 1, options, sorts
		mutation = true
	case "/UserGreetingChange":
		text, _, e := wire.Bytes(request, 2)
		if e != nil || !validGreeting(string(text)) {
			return fail(fmt.Errorf("profile: invalid greeting"))
		}
		if e := s.checkText(ctx, GreetingText, string(text)); e != nil {
			return fail(e)
		}
		settings.Greeting = string(text)
		mutation = true
	case "/UserTitleChange":
		id, e := profileScalar(request, 2, math.MaxInt32)
		if e != nil {
			return fail(e)
		}
		if id != 0 {
			owned := false
			for _, item := range s.source.OwnedItems(ctx) {
				owned = owned || item.Type == 26 && item.ID == id && item.Count > 0
			}
			if !s.design.Titles[id] || !owned {
				return fail(fmt.Errorf("profile: title is not owned"))
			}
		}
		settings.TitleID = id
		mutation = true
	case "/UserPortraitChange":
		id, e := profileScalar(request, 2, math.MaxInt32)
		if e != nil {
			return fail(e)
		}
		if id != 0 && id != s.design.DefaultPortraitCostumeID {
			if _, ok := s.source.OwnedCostume(id); !ok {
				return fail(fmt.Errorf("profile: portrait costume is not owned"))
			}
		}
		if e := s.source.SelectPortrait(ctx, id); e != nil {
			return fail(e)
		}
		out = wire.AppendVarint(nil, 1, id)
		mutation = true
	case "/IdCardSave", "/IdCardPresetSave":
		body, found, e := wire.Bytes(request, 2)
		if e != nil || !found {
			return fail(fmt.Errorf("profile: missing ID card"))
		}
		var index uint64
		if path == "/IdCardPresetSave" {
			index, e = profileScalar(body, 1, s.design.PresetLimit-1)
			if e != nil {
				return fail(e)
			}
			body, found, e = wire.Bytes(body, 2)
			if e != nil || !found {
				return fail(fmt.Errorf("profile: missing preset layout"))
			}
		}
		card, e := parseCard(body)
		if e != nil {
			return fail(e)
		}
		if e = s.validateCard(ctx, &card, true); e != nil {
			return fail(e)
		}
		if path == "/IdCardSave" {
			settings.Card = card
		} else {
			settings.Presets[strconv.FormatUint(index, 10)] = card
		}
		mutation = true
	case "/IdCardPresetInfo":
		for i := uint64(0); i < s.design.PresetLimit; i++ {
			if card, ok := settings.Presets[strconv.FormatUint(i, 10)]; ok {
				row := wire.AppendVarint(nil, 1, i)
				row = wire.AppendBytes(row, 2, cardWire(card))
				out = wire.AppendBytes(out, 1, row)
			}
		}
	case "/IdCardPresetDelete":
		index, e := profileScalar(request, 2, s.design.PresetLimit-1)
		if e != nil {
			return fail(e)
		}
		delete(settings.Presets, strconv.FormatUint(index, 10))
		mutation = true
	case "/IdCardRecovery":
		if !settings.CardRecovered {
			bundle, e := s.economy.Apply(ctx, "profile:idcard:default", nil, []gamedata.Reward{s.design.DefaultReward})
			if e != nil {
				return fail(e)
			}
			if len(bundle) > 0 {
				out = wire.AppendBytes(out, 1, bundle)
			}
			settings.CardRecovered = true
		}
		card := settings.Card
		if card.Background == nil {
			card = s.defaultCard()
		}
		if e := s.validateCard(ctx, &card, true); e != nil {
			card = s.defaultCard()
			if e := s.validateCard(ctx, &card, true); e != nil {
				return fail(e)
			}
		}
		settings.Card = card
		out = wire.AppendBytes(out, 2, cardWire(card))
		mutation = true
	case "/IdCardShopInfo":
		for _, id := range sortedMapKeys(settings.ShopBought) {
			row := wire.AppendVarint(nil, 1, id)
			row = wire.AppendVarint(row, 2, settings.ShopBought[strconv.FormatUint(id, 10)])
			out = wire.AppendBytes(out, 1, row)
		}
	case "/IdCardShopBuy":
		bundle, e := s.buyCardItems(ctx, identity, request, settings)
		if e != nil {
			return fail(e)
		}
		out = wire.AppendBytes(out, 1, bundle)
		mutation = true
	case "/SupporterRegister", "/SupporterRemove", "/SupporterInfo", "/SupporterStatus", "/SupporterDetail", "/SupporterReward":
		out, mutation, err = s.handleSupporters(ctx, path, request, settings)
		if err != nil {
			return fail(err)
		}
	}
	if mutation {
		if err := s.save(ctx, state); err != nil {
			return fail(err)
		}
	}
	if rememberEconomic {
		raw, err := json.Marshal(receipt{Request: request, Response: out})
		if err != nil {
			return fail(err)
		}
		if err := s.store.Save(ctx.State, receiptKey, raw); err != nil {
			return fail(err)
		}
	}
	return code, out, true, nil
}

func profileScalar(raw []byte, field int, max uint64) (uint64, error) {
	var n uint64
	seen := false
	err := wire.Walk(raw, func(f wire.Field) error {
		if f.Number != field {
			return nil
		}
		if f.Type != 0 || seen {
			return wire.ErrMalformed
		}
		seen = true
		n, _ = binary.Uvarint(f.Value)
		if n > max {
			return wire.ErrMalformed
		}
		return nil
	})
	return n, err
}

func profileNumbers(raw []byte, field int) ([]uint64, error) {
	var out []uint64
	err := wire.Walk(raw, func(f wire.Field) error {
		if f.Number != field {
			return nil
		}
		if f.Type == 0 {
			n, _ := binary.Uvarint(f.Value)
			out = append(out, n)
			return nil
		}
		if f.Type != 2 {
			return wire.ErrMalformed
		}
		for b := f.Value; len(b) > 0; {
			n, k := binary.Uvarint(b)
			if k <= 0 {
				return wire.ErrMalformed
			}
			out = append(out, n)
			b = b[k:]
		}
		return nil
	})
	return out, err
}
func validOptions(options []uint64) bool {
	seen := map[uint64]bool{}
	for _, n := range options {
		if n == 0 || n > 12 || seen[n] {
			return false
		}
		seen[n] = true
	}
	return true
}
func validSort(sorts []uint64) bool {
	if len(sorts) != 12 || sorts[0] != 0 || sorts[1] != 5 {
		return false
	}
	seen := map[uint64]bool{}
	for _, n := range sorts {
		if n > 12 || n == 2 || seen[n] {
			return false
		}
		seen[n] = true
	}
	return true
}

func validGreeting(text string) bool {
	if !utf8.ValidString(text) || len(utf16.Encode([]rune(text))) < 4 {
		return false
	}
	for _, r := range text {
		v := unicode.ToLower(r)
		if v == ' ' || v >= '0' && v <= '9' || v >= 'a' && v <= 'z' || v >= 0xAC00 && v <= 0xD7A3 || v >= 0x3131 && v <= 0x3163 || v >= 0x4E00 && v <= 0x9FD5 || v >= 0x3041 && v <= 0x30FE || strings.ContainsRune("~!?,.？！、。～", v) {
			continue
		}
		return false
	}
	return true
}
