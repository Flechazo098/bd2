package gamedata

import (
	"fmt"
	"time"
)

type NativeFishing struct {
	Capacity                                int
	LoadingLimit, ReconnectWindow, Duration time.Duration
}

func (c *EventPlayCatalog) NativeFishingRules(mapID, boatLevel, skin uint64) (NativeFishing, error) {
	var out NativeFishing
	if _, err := c.Row("FishingMapGroupTable", 5, mapID); err != nil {
		return out, err
	}
	b, err := c.Row("FishingBoatTable", 8, boatLevel)
	if err != nil {
		return out, err
	}
	capacity, _ := optionalScalar(b, 9)
	if capacity == 0 || capacity > 5 {
		return out, fmt.Errorf("gamedata: boat does not support multiplayer")
	}
	skins, err := packedInts(b, 5)
	if err != nil {
		return out, err
	}
	found := false
	for _, id := range skins {
		found = found || id == skin
	}
	if !found {
		return out, fmt.Errorf("gamedata: boat skin incompatible")
	}
	d, err := c.Row("FishingDefaultTable", 25, 0)
	if err != nil {
		return out, err
	}
	load, _ := optionalScalar(d, 26)
	out.Capacity = int(capacity)
	out.LoadingLimit = time.Duration(load) * time.Millisecond
	out.ReconnectWindow = time.Duration(fishingDouble(d, 41) * float64(time.Second))
	out.Duration = time.Duration(fishingDouble(d, 42) * float64(time.Second))
	if out.LoadingLimit <= 0 || out.ReconnectWindow <= 0 || out.Duration <= 0 {
		return out, fmt.Errorf("gamedata: invalid fishing room limits")
	}
	return out, nil
}

func fishingDouble(b []byte, field int) float64 {
	v, _ := optionalDouble(b, field)
	return v
}
