package fishing

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func (s *Service) weighted(ids, weights []uint64) (uint64, error) {
	if len(ids) == 0 || len(ids) != len(weights) {
		return 0, fmt.Errorf("fishing: invalid weighted pool")
	}
	var total uint64
	for _, w := range weights {
		if w > math.MaxUint64-total {
			return 0, fmt.Errorf("fishing: pool overflow")
		}
		total += w
	}
	pick, e := s.sample(total)
	if e != nil {
		return 0, e
	}
	if pick >= total {
		return 0, fmt.Errorf("fishing: invalid sample")
	}
	for i, w := range weights {
		if pick < w {
			return ids[i], nil
		}
		pick -= w
	}
	return 0, fmt.Errorf("fishing: empty sample")
}
func (s *Service) buffs(n *snapshot) map[uint64]float64 {
	out := map[uint64]float64{}
	add := func(id uint64) {
		if row, e := s.design.Row("FishingBuffTable", 4, id); e == nil {
			out[num(row, 2)] += dbl(row, 3)
		}
	}
	for _, rod := range n.Rods {
		if rod.Index == n.Rod {
			r, _ := s.design.Row("FishingRodTable", 5, rod.ID)
			ids, _ := ints(r, 2)
			for _, id := range ids {
				add(id)
			}
		}
	}
	boatLevel := n.Boat
	if n.Voyage != nil && n.Voyage.BoatLevel > 0 {
		boatLevel = n.Voyage.BoatLevel
	}
	boat, _ := s.design.Row("FishingBoatTable", 8, boatLevel)
	groups, _ := ints(boat, 1)
	members := uint64(1)
	if n.Voyage != nil && n.Voyage.Members > 0 {
		members = n.Voyage.Members
	}
	for _, group := range groups {
		for _, r := range s.design.Rows("FishingBoatBuffTable", 2, group) {
			if num(r, 3) == members {
				add(num(r, 1))
			}
		}
	}
	for _, b := range n.Buffs {
		if b.End > s.now().Unix() {
			add(b.ID)
		}
	}
	cast := n.Cast
	if cast == 0 && n.Encounter != nil {
		cast = n.Encounter.Cast
	}
	if cast != 0 {
		r, _ := s.design.Row("FishingCastingTable", 3, cast)
		add(num(r, 1))
	}
	return out
}
func (s *Service) maxDamage(n *snapshot) int64 {
	char, _ := s.design.Row("FishingCharTable", 1, n.Level)
	d, _ := s.design.Row("FishingDefaultTable", 25, 0)
	return int64(math.Ceil((dbl(char, 3) + s.buffs(n)[2]) * dbl(d, 11)))
}
func (s *Service) encounter(n *snapshot, automatic bool) (*Encounter, error) {
	var poolID uint64
	phase := uint64(1)
	cycle := int64(0)
	starts := map[uint64]int64{}
	for _, row := range s.design.Tables["FishingTimeTable"] {
		raw, _, _ := wire.Bytes(row, 2)
		parts := strings.Split(string(raw), ":")
		if len(parts) != 2 {
			continue
		}
		m, e1 := strconv.ParseInt(parts[0], 10, 64)
		sec, e2 := strconv.ParseInt(parts[1], 10, 64)
		if e1 != nil || e2 != nil || m < 0 || sec < 0 || sec > 59 {
			continue
		}
		at := m*60 + sec
		starts[num(row, 1)] = at
		if at > cycle {
			cycle = at
		}
	}
	if cycle > 0 {
		elapsed := s.now().Unix() % (cycle * 2)
		latest := int64(-1)
		for id, at := range starts {
			if at <= elapsed && at > latest {
				phase = id
				latest = at
			}
		}
	}
	for _, row := range s.design.Rows("FishingMapTable", 2, n.Map) {
		if num(row, 6) == 1 && num(row, 3) == phase {
			poolID = num(row, 1)
			break
		}
	}
	if poolID == 0 {
		return nil, fmt.Errorf("fishing: map has no fishing pool")
	}
	luck := s.buffs(n)
	var gradeRow []byte
	best := int64(-1)
	for _, row := range s.design.Tables["FishingGradePoolTable"] {
		v := int64(num(row, 1))
		if num(row, 5) == 1 && float64(v) <= luck[6] && v > best {
			gradeRow = row
			best = v
		}
	}
	grades, e := ints(gradeRow, 2)
	if automatic {
		d, err := s.design.Row("FishingDefaultTable", 25, 0)
		if err != nil {
			return nil, err
		}
		gradeRow, err = s.design.Row("FishingGradePoolTable", 3, num(d, 9))
		if err != nil {
			return nil, err
		}
		grades, e = ints(gradeRow, 2)
	}
	if e != nil {
		return nil, e
	}
	weights, e := ints(gradeRow, 4)
	if e != nil {
		return nil, e
	}
	grade, e := s.weighted(grades, weights)
	if e != nil {
		return nil, e
	}
	var pool []byte
	for _, row := range s.design.Rows("FishingFishPoolTable", 2, poolID) {
		if num(row, 3) == grade {
			pool = row
			break
		}
	}
	ids, e := ints(pool, 1)
	if e != nil {
		return nil, e
	}
	weights, e = ints(pool, 4)
	if e != nil {
		return nil, e
	}
	id, e := s.weighted(ids, weights)
	if e != nil {
		return nil, e
	}
	fish, e := s.design.Row("FishingFishTable", 12, id)
	if e != nil {
		return nil, e
	}
	size, e := s.fishSize(fish, luck[5])
	if e != nil {
		return nil, e
	}
	char, e := s.design.Row("FishingCharTable", 1, n.Level)
	if e != nil {
		return nil, e
	}
	d, _ := s.design.Row("FishingDefaultTable", 25, 0)
	duration := dbl(d, 22) + (dbl(char, 4)+luck[1])/float64(num(d, 47))
	if !isFinite(duration) || duration <= 0 {
		return nil, fmt.Errorf("fishing: invalid fight duration")
	}
	hp := int64(num(fish, 22))
	if hp <= 0 {
		return nil, fmt.Errorf("fishing: invalid fish stamina")
	}
	cast := n.Cast
	if automatic {
		cast = 0
	}
	return &Encounter{Fish: id, Size: size, Cast: cast, HP: hp, MaxHP: hp, Started: s.now().Unix(), Expires: s.now().Unix() + int64(math.Ceil(duration))}, nil
}
func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func (s *Service) purchase(ctx command.Context, path string, req []byte, n *snapshot, key string) ([]byte, error) {
	var row []byte
	var err error
	var fields [3]int
	var id uint64
	var out []byte
	switch path {
	case "/FishingBoatUpgrade":
		t, e := loadTrap(ctx)
		if e != nil {
			return nil, e
		}
		if e = s.refreshTrap(n, &t); e != nil {
			return nil, e
		}
		if t.Receipt > 0 {
			t.Cursor = s.now().Unix()
		}
		if e = saveTrap(ctx, t); e != nil {
			return nil, e
		}
		id = n.Boat + 1
		row, err = s.design.Row("FishingBoatTable", 8, id)
		fields = [3]int{4, 3, 2}
	case "/FishingMapBuy":
		id = num(req, 2)
		if slices.Contains(n.Maps, id) {
			return nil, fmt.Errorf("fishing: map already owned")
		}
		row, err = s.design.Row("FishingMapGroupTable", 5, id)
		fields = [3]int{3, 2, 1}
	case "/FishingBoatSkinBuy":
		id = num(req, 2)
		if slices.Contains(n.Skins, id) {
			return nil, fmt.Errorf("fishing: skin already owned")
		}
		row, err = s.design.Row("FishingBoatDesignTable", 2, id)
		fields = [3]int{7, 6, 5}
	case "/FishingBoatSkinSet":
		id = num(req, 2)
		if !slices.Contains(n.Skins, id) {
			return nil, fmt.Errorf("fishing: skin not owned")
		}
		boat, e := s.design.Row("FishingBoatTable", 8, n.Boat)
		if e != nil {
			return nil, e
		}
		skins, e := ints(boat, 5)
		if e != nil || !slices.Contains(skins, id) {
			return nil, fmt.Errorf("fishing: incompatible skin")
		}
		n.Skin = id
		return nil, s.emit(ctx, n, 0, key)
	}
	if err != nil {
		return nil, err
	}
	var costs []gamedata.Reward
	if count := num(row, fields[2]); count > 0 {
		costs = []gamedata.Reward{{Type: num(row, fields[0]), ID: num(row, fields[1]), Count: count}}
	}
	bundle, err := s.economy.Apply(ctx, "fishing:"+key, costs, nil)
	if err != nil {
		return nil, err
	}
	switch path {
	case "/FishingBoatUpgrade":
		n.Boat = id
		skins, e := ints(row, 5)
		if e != nil || len(skins) == 0 {
			return nil, fmt.Errorf("fishing: upgraded boat has no skin")
		}
		if !slices.Contains(n.Skins, skins[0]) {
			n.Skins = append(n.Skins, skins[0])
		}
		n.Skin = skins[0]
		out = wire.AppendVarint(nil, 1, id)
		t, e := loadTrap(ctx)
		if e != nil {
			return nil, e
		}
		if e = s.refreshTrap(n, &t); e != nil {
			return nil, e
		}
		if e = saveTrap(ctx, t); e != nil {
			return nil, e
		}
		out = wire.AppendVarint(out, 2, uint64(t.Receipt))
		if len(bundle) > 0 {
			out = wire.AppendBytes(out, 3, bundle)
		}
	case "/FishingMapBuy":
		n.Maps = append(n.Maps, id)
	case "/FishingBoatSkinBuy":
		n.Skins = append(n.Skins, id)
	}
	return out, s.emit(ctx, n, 0, key)
}

func (s *Service) fishSize(fish []byte, luck float64) (uint64, error) {
	var sizeRow []byte
	best := int64(-1)
	for _, row := range s.design.Tables["FishingFishSizePoolTable"] {
		v := int64(num(row, 1))
		if float64(v) <= luck && v > best {
			sizeRow = row
			best = v
		}
	}
	lo, e := ints(sizeRow, 4)
	if e != nil {
		return 0, e
	}
	hi, e := ints(sizeRow, 3)
	if e != nil {
		return 0, e
	}
	ratios, e := ints(sizeRow, 5)
	if e != nil {
		return 0, e
	}
	indices := make([]uint64, len(lo))
	for i := range indices {
		indices[i] = uint64(i)
	}
	idx, e := s.weighted(indices, ratios)
	if e != nil || len(lo) != len(hi) {
		return 0, fmt.Errorf("fishing: invalid size pool")
	}
	if hi[idx] < lo[idx] {
		return 0, fmt.Errorf("fishing: invalid size interval")
	}
	r, e := s.sample(hi[idx] - lo[idx] + 1)
	if e != nil {
		return 0, e
	}
	return uint64(math.Round(dbl(fish, 15) + (dbl(fish, 14)-dbl(fish, 15))*float64(lo[idx]+r)/1000)), nil
}
