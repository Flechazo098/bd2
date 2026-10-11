package gamedata

import (
	"fmt"
	"time"
)

type NativeMinigame struct {
	GroupID, StageID, CharacterID, MonsterID, LastWave uint64
	Capacity                                           int
	LoadingLimit, Duration                             time.Duration
}

// NativeMinigameRules validates the selected event and client character/monster
// against installed design rows. Party limits come from the current client UI.
func (c *EventPlayCatalog) NativeMinigameRules(family string, eventID, character, monster uint64, solo bool) (NativeMinigame, error) {
	event, err := c.Row("PackEventMiniGameTable", 8, eventID)
	if err != nil {
		return NativeMinigame{}, err
	}
	kind, _ := optionalScalar(event, 3)
	group, _ := optionalScalar(event, 4)
	r := NativeMinigame{}
	switch family {
	case "Action":
		if kind != 8 {
			return r, fmt.Errorf("gamedata: event is not action")
		}
		game, e := c.Row("ActionGameTable", 3, group)
		if e != nil {
			return r, e
		}
		r.GroupID, _ = optionalScalar(game, 4)
		for _, row := range c.Rows("ActionGameCharTable", 14, character) {
			g, _ := optionalScalar(row, 12)
			t, _ := optionalScalar(row, 16)
			if g == 1 && t == 0 {
				r.CharacterID = character
			}
		}
		if r.CharacterID == 0 {
			return r, fmt.Errorf("gamedata: action character unavailable")
		}
		for _, stage := range c.Rows("ActionGameStageTable", 3, r.GroupID) {
			m, _ := optionalScalar(stage, 1)
			typ, _ := optionalScalar(stage, 11)
			if m != monster || solo && typ != 1 || !solo && typ != 2 {
				continue
			}
			r.MonsterID = m
			r.StageID, _ = optionalScalar(stage, 4)
			limit, _ := optionalScalar(stage, 12)
			r.Duration = time.Duration(limit) * time.Second
		}
		if r.StageID == 0 || r.Duration <= 0 {
			return r, fmt.Errorf("gamedata: action stage unavailable")
		}
		d, e := c.Row("ActionGameDefaultTable", 10, 0)
		if e != nil {
			return r, e
		}
		loading, _ := optionalScalar(d, 13)
		r.LoadingLimit = time.Duration(loading) * time.Millisecond
		r.Capacity = 2
	case "Defense":
		if kind != 7 {
			return r, fmt.Errorf("gamedata: event is not defense")
		}
		for _, row := range c.Tables["MGDWaveTable"] {
			id, _ := optionalScalar(row, 2)
			if id > r.LastWave {
				r.LastWave = id
			}
		}
		if r.LastWave == 0 {
			return r, fmt.Errorf("gamedata: defense waves unavailable")
		}
		d, e := c.Row("MGDDefaultTable", 9, 0)
		if e != nil {
			return r, e
		}
		loading, _ := optionalScalar(d, 10)
		r.LoadingLimit = time.Duration(loading) * time.Millisecond
		r.Capacity = 4
	default:
		return r, fmt.Errorf("gamedata: unknown native minigame")
	}
	if solo {
		r.Capacity = 1
	}
	return r, nil
}
