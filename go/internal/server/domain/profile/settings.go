package profile

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

type CardItem struct {
	InvenIndex, ID uint64
	X, Y           int32
	Rotate, Scale  float32
	Layer          uint64
	Color          string
}

type Card struct {
	Background, SubBackground, BackgroundEffect, MyInfo *CardItem
	Stickers                                            []CardItem
	Rotate                                              uint64
}

type Supporter struct{ CharacterIndex, CostumeIndex, Power, Date uint64 }

type CostumeBase struct{ ID, Level, DesignID uint64 }

type SupporterDetails struct {
	Character roster.Character
	Costumes  []roster.Costume
	Equipment []assets.Equipment
	UniqueID  uint64
	Awake     roster.CharAwakeProgress
}

type Settings struct {
	Greeting      string               `json:"greeting"`
	IsAllPrivate  bool                 `json:"is_all_private"`
	Options       []uint64             `json:"options"`
	SortIDs       []uint64             `json:"sort_ids"`
	TitleID       uint64               `json:"title_id"`
	Card          Card                 `json:"card"`
	Presets       map[string]Card      `json:"presets"`
	Supporters    map[string]Supporter `json:"supporters"`
	ShopBought    map[string]uint64    `json:"shop_bought"`
	CardRecovered bool                 `json:"card_recovered"`
}

type receipt struct {
	Request  []byte `json:"request"`
	Response []byte `json:"response"`
}
type savedProfile struct {
	Version  uint64   `json:"version"`
	Settings Settings `json:"settings"`
}

type Economy interface {
	Apply(command.Context, string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}

type Service struct {
	store        stateio.Store
	source       Source
	design       *gamedata.ProfileDesign
	economy      Economy
	checkText    TextCheck
	initialTitle uint64
	now          func() time.Time
}

func Open(ctx command.Context, store stateio.Store, source Source, design *gamedata.ProfileDesign, economy Economy, checkText TextCheck, initialTitle uint64) (*Service, error) {
	if store == nil || source == nil || design == nil || economy == nil || checkText == nil {
		return nil, fmt.Errorf("profile: incomplete dependencies")
	}
	s := &Service{store: store, source: source, design: design, economy: economy, checkText: checkText, initialTitle: initialTitle, now: time.Now}
	state, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.save(ctx, state); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) defaultCard() Card {
	var card Card
	for _, d := range s.design.Defaults {
		item := CardItem{ID: d.ID, X: d.X, Y: d.Y, Rotate: d.Rotate, Scale: d.Scale, Layer: d.Layer, Color: d.Color}
		switch d.Kind {
		case 1:
			card.Background = &item
		case 2:
			card.SubBackground = &item
		case 3:
			card.BackgroundEffect = &item
		case 4, 5:
			card.Stickers = append(card.Stickers, item)
		case 6:
			card.MyInfo = &item
		}
	}
	return card
}

func (s *Service) load(ctx command.Context) (savedProfile, error) {
	state := savedProfile{Version: 1, Settings: Settings{TitleID: s.initialTitle, Card: s.defaultCard(), SortIDs: []uint64{0, 5, 1, 12, 6, 3, 4, 7, 8, 9, 10, 11}, Presets: map[string]Card{}, Supporters: map[string]Supporter{}, ShopBought: map[string]uint64{}}}
	raw, err := s.store.Load(ctx.State, "profile")
	if err != nil || raw == nil {
		return state, err
	}
	if err := stateio.RequireExactJSONObject(raw, "version", "settings"); err != nil {
		return state, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return state, err
	}
	if err := stateio.RequireExactJSONObject(object["settings"], "greeting", "is_all_private", "options", "sort_ids", "title_id", "card", "presets", "supporters", "shop_bought", "card_recovered"); err != nil {
		return state, err
	}
	state = savedProfile{}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, err
	}
	if state.Version != 1 || state.Settings.Presets == nil || state.Settings.Supporters == nil || state.Settings.ShopBought == nil || !validOptions(state.Settings.Options) || !validSort(state.Settings.SortIDs) || state.Settings.Greeting != "" && !validGreeting(state.Settings.Greeting) {
		return state, fmt.Errorf("profile: invalid persisted settings")
	}
	for key := range state.Settings.Presets {
		if !validIndexKey(key, s.design.PresetLimit) {
			return state, fmt.Errorf("profile: invalid persisted preset")
		}
	}
	for key := range state.Settings.Supporters {
		if !validIndexKey(key, 2) {
			return state, fmt.Errorf("profile: invalid persisted supporter")
		}
	}
	for key, count := range state.Settings.ShopBought {
		id, err := strconv.ParseUint(key, 10, 64)
		shop, ok := s.design.Shop[id]
		if err != nil || key != strconv.FormatUint(id, 10) || !ok || count > shop.MaxCount {
			return state, fmt.Errorf("profile: invalid persisted shop count")
		}
	}
	return state, nil
}

func validIndexKey(key string, limit uint64) bool {
	n, err := strconv.ParseUint(key, 10, 64)
	return err == nil && n < limit && key == strconv.FormatUint(n, 10)
}

func (s *Service) save(ctx command.Context, state savedProfile) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.store.Save(ctx.State, "profile", raw)
}

func (s *Service) Settings(ctx command.Context) (Settings, error) {
	state, err := s.load(ctx)
	return state.Settings, err
}
func (s *Service) SelectedTitle(ctx command.Context) (uint64, error) {
	state, err := s.load(ctx)
	return state.Settings.TitleID, err
}
