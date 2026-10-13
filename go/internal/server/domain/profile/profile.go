package profile

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

var ErrNotFound = errors.New("profile: target public profile not found")

type Snapshot struct {
	OwnerIndex, TitleID, PortraitCostumeID, PortraitDesignID               uint64
	AchievementLevel, TotalBattlePower, MonsterHuntRank, LikeCount         uint64
	UserID                                                                 string
	MonsterHuntTopPercent                                                  float64
	Greeting                                                               string
	IsAllPrivate, IsFriend                                                 bool
	Options, SortIDs                                                       []uint64
	Card                                                                   Card
	Supporters                                                             []CostumeBase
	GuildBase, RoomInfo                                                    []byte
	RoomLikeCount                                                          uint64
	PvpSeason, PvpVP, PvpRank, ColosseumSeason, ColosseumVP, ColosseumRank uint64
	GuildRaidRank, GuildRaidScore                                          uint64
	ColosseumTopPercent                                                    float64
	AvatarUse                                                              *AvatarUse
}

type Source interface {
	PublicProfile(command.Context, uint64) (Snapshot, error)
	OwnedItems(command.Context) []assets.Item
	OwnedCostume(uint64) (CostumeBase, bool)
	SupporterDetails(command.Context, uint64, uint64) (SupporterDetails, error)
	CardUnlocked(command.Context, gamedata.ProfileCardItem) (bool, error)
	SelectPortrait(command.Context, uint64) error
	ProfileOwner(command.Context) (uint64, error)
}

func (s *Service) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	code, ok := profileCodes[path]
	if !ok {
		return 0, nil, false, nil
	}
	fail := func(err error) (int, []byte, bool, error) { return code, nil, true, err }
	var seq, target uint64
	seen := map[int]bool{}
	err := wire.Walk(request, func(f wire.Field) error {
		if f.Number != 1 && (f.Number != 2 || path != "/UserContentsInfo") {
			return nil
		}
		if f.Type != 0 || seen[f.Number] {
			return wire.ErrMalformed
		}
		seen[f.Number] = true
		v, _ := binary.Uvarint(f.Value)
		if f.Number == 1 {
			seq = v
		} else {
			target = v
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if seq == 0 || seq > math.MaxInt32 || s.source == nil {
		return fail(fmt.Errorf("profile: invalid request sequence or source"))
	}
	if path != "/UserContentsInfo" {
		return s.handleSettings(ctx, path, request, seq)
	}
	if target == 0 || target > math.MaxInt64 {
		return fail(ErrNotFound)
	}
	p, err := s.source.PublicProfile(ctx, target)
	if err != nil {
		return fail(err)
	}
	if p.OwnerIndex != target || p.UserID == "" {
		return fail(ErrNotFound)
	}
	for _, v := range []uint64{p.TitleID, p.PortraitCostumeID, p.PortraitDesignID, p.AchievementLevel, p.TotalBattlePower, p.MonsterHuntRank, p.LikeCount} {
		if v > math.MaxInt32 {
			return fail(fmt.Errorf("profile: integer exceeds protocol range"))
		}
	}
	out := wire.AppendVarint(nil, 1, p.OwnerIndex)
	out = wire.AppendString(out, 2, p.UserID)
	out = wire.AppendString(out, 5, p.Greeting)
	if p.IsAllPrivate {
		out = wire.AppendVarint(out, 11, 1)
	}
	for _, n := range p.Options {
		out = wire.AppendVarint(out, 12, n)
	}
	for _, n := range p.SortIDs {
		out = wire.AppendVarint(out, 27, n)
	}
	for _, field := range []struct {
		Number int
		Value  uint64
	}{{3, p.TitleID}, {4, p.PortraitCostumeID}, {6, p.PvpSeason}, {7, p.PvpVP}, {8, p.PvpRank}, {9, p.MonsterHuntRank}, {10, p.LikeCount}, {16, p.TotalBattlePower}, {19, p.PortraitDesignID}, {20, p.GuildRaidRank}, {21, p.GuildRaidScore}, {26, p.AchievementLevel}, {31, p.ColosseumSeason}, {32, p.ColosseumVP}, {33, p.ColosseumRank}} {
		if field.Value != 0 {
			out = wire.AppendVarint(out, field.Number, field.Value)
		}
	}
	if p.IsFriend {
		out = wire.AppendVarint(out, 13, 1)
	}
	room := wire.AppendVarint(nil, 1, p.OwnerIndex)
	room = wire.AppendString(room, 2, p.UserID)
	room = wire.AppendVarint(room, 3, p.PortraitCostumeID)
	room = wire.AppendVarint(room, 11, p.PortraitDesignID)
	if len(p.RoomInfo) > 0 {
		room = p.RoomInfo
	}
	out = wire.AppendBytes(out, 14, room)
	out = wire.AppendVarint(out, 18, p.RoomLikeCount)
	// UserInfoPacket dereferences GuildBaseInfo.Id even for a guildless user.
	out = wire.AppendBytes(out, 17, p.GuildBase)
	out = wire.AppendBytes(out, 28, cardWire(p.Card))
	if p.ColosseumTopPercent > 0 {
		out = wire.AppendDouble(out, 35, p.ColosseumTopPercent)
	}
	for _, c := range p.Supporters {
		out = wire.AppendBytes(out, 30, costumeBaseWire(c))
	}
	if p.AvatarUse != nil {
		out = wire.AppendBytes(out, 34, AvatarUseWire(*p.AvatarUse))
	}
	if p.MonsterHuntRank != 0 {
		out = wire.AppendDouble(out, 29, p.MonsterHuntTopPercent)
	}
	return code, out, true, nil
}

func cardWire(card Card) []byte {
	var out []byte
	appendItem := func(number int, item CardItem) {
		b := wire.AppendVarint(nil, 2, item.ID)
		if item.InvenIndex != 0 {
			b = wire.AppendVarint(b, 1, item.InvenIndex)
		}
		b = wire.AppendVarint(b, 3, uint64(int64(item.X)))
		b = wire.AppendVarint(b, 4, uint64(int64(item.Y)))
		b = binary.AppendUvarint(b, 5<<3|5)
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(item.Rotate))
		b = binary.AppendUvarint(b, 6<<3|5)
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(item.Scale))
		b = wire.AppendVarint(b, 7, item.Layer)
		b = wire.AppendString(b, 8, item.Color)
		out = wire.AppendBytes(out, number, b)
	}
	for number, item := range map[int]*CardItem{1: card.Background, 2: card.SubBackground, 3: card.BackgroundEffect, 5: card.MyInfo} {
		if item != nil {
			appendItem(number, *item)
		}
	}
	for _, item := range card.Stickers {
		appendItem(4, item)
	}
	out = wire.AppendVarint(out, 6, card.Rotate)
	return out
}

func costumeBaseWire(c CostumeBase) []byte {
	b := wire.AppendVarint(nil, 1, c.ID)
	b = wire.AppendVarint(b, 2, c.Level)
	return wire.AppendVarint(b, 3, c.DesignID)
}
