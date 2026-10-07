package gamedata

import (
	"bd2server/internal/server/protocol/wire"
	"fmt"
	"strconv"
	"strings"
)

type HuntingAPDesign struct {
	Max          uint64
	ResetSeconds int64
}

func LoadHuntingAPDesign(root, version string) (HuntingAPDesign, error) {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return HuntingAPDesign{}, e
	}
	defer done()
	var raw []byte
	if e = db.QueryRow("SELECT ProtoBuf FROM GameDefaultTable WHERE id=0").Scan(&raw); e != nil {
		return HuntingAPDesign{}, e
	}
	max, e := optionalScalar(raw, 56)
	if e != nil {
		return HuntingAPDesign{}, e
	}
	text, _, e := wire.Bytes(raw, 23)
	if e != nil {
		return HuntingAPDesign{}, e
	}
	parts := strings.Split(string(text), ":")
	if max == 0 || max > 2147483647 || len(parts) != 3 {
		return HuntingAPDesign{}, fmt.Errorf("gamedata: invalid hunting AP defaults")
	}
	var secs int64
	for i, p := range parts {
		n, e := strconv.ParseInt(p, 10, 64)
		limit := int64(60)
		if i == 0 {
			limit = 24
		}
		if e != nil || n < 0 || n >= limit {
			return HuntingAPDesign{}, fmt.Errorf("gamedata: invalid daily reset time")
		}
		secs = secs*60 + n
	}
	return HuntingAPDesign{Max: max, ResetSeconds: secs}, nil
}
