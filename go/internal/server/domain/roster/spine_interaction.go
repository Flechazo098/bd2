package roster

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

type InteractionService struct {
	Store     stateio.Store
	Design    *gamedata.SpineInteractionDesign
	Authorize func(command.Context, uint64) (bool, error)
	Grant     func(command.Context, string, []gamedata.Reward) ([]byte, error)
}

type interactionProgress struct {
	Points map[string][]uint64 `json:"points"`
	Claims map[string]bool     `json:"claims"`
}

func interactionKey(k gamedata.InteractionPointKey) string {
	return fmt.Sprintf("%d:%d:%d", k[0], k[1], k[2])
}

func interactionIdentity(raw []byte) (gamedata.InteractionPointKey, error) {
	var key gamedata.InteractionPointKey
	for i := range key {
		n, found, err := wire.Varint(raw, i+1)
		if err != nil || !found || n == 0 || n > 1<<31-1 {
			return key, fmt.Errorf("interaction: invalid point identity")
		}
		key[i] = n
	}
	return key, nil
}

func (s InteractionService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	codes := map[string]int{"/SpineInteractionAchievementInfo": 532, "/SpineInteractionAchievementSave": 533, "/SpineInteractionRewardInfo": 548, "/SpineInteractionReward": 549}
	code, ok := codes[path]
	if !ok {
		return 0, nil, false, nil
	}
	fail := func(err error) (int, []byte, bool, error) { return code, nil, true, err }
	if seq, found, err := wire.Varint(request, 1); err != nil || !found || seq == 0 {
		return fail(fmt.Errorf("interaction: invalid request sequence"))
	}
	if s.Store == nil || s.Design == nil || s.Authorize == nil || s.Grant == nil {
		return fail(fmt.Errorf("interaction: missing dependencies"))
	}
	st := interactionProgress{Points: map[string][]uint64{}, Claims: map[string]bool{}}
	raw, err := s.Store.Load(ctx.State, "spine_interaction")
	if err != nil {
		return fail(err)
	}
	if raw != nil {
		if err := stateio.RequireExactJSONObject(raw, "points", "claims"); err != nil {
			return fail(err)
		}
		if err := json.Unmarshal(raw, &st); err != nil || st.Points == nil || st.Claims == nil {
			return fail(fmt.Errorf("interaction: invalid saved progress"))
		}
	}
	var out []byte
	switch path {
	case "/SpineInteractionAchievementInfo":
		out, err = interactionInfo(st.Points)
	case "/SpineInteractionRewardInfo":
		keys := make([]string, 0, len(st.Claims))
		for key := range st.Claims {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			k, parseErr := parseInteractionKey(key)
			if parseErr != nil {
				return fail(parseErr)
			}
			out = wire.AppendBytes(out, 1, interactionPointWire(k))
		}
	case "/SpineInteractionAchievementSave":
		err = s.savePoints(ctx, request, st.Points)
	case "/SpineInteractionReward":
		row, found, parseErr := wire.Bytes(request, 2)
		if parseErr != nil || !found {
			return fail(fmt.Errorf("interaction: missing reward point"))
		}
		key, parseErr := interactionIdentity(row)
		if parseErr != nil {
			return fail(parseErr)
		}
		point, known := s.Design.Points[key]
		if !known || len(s.Design.Rewards[point.RewardGroup]) == 0 {
			return fail(fmt.Errorf("interaction: point has no reward"))
		}
		if allowed, accessErr := s.Authorize(ctx, key[0]); accessErr != nil || !allowed {
			return fail(fmt.Errorf("interaction: group unavailable: %v", accessErr))
		}
		if st.Claims[interactionKey(key)] {
			return code, wire.AppendBytes(nil, 1, nil), true, nil
		}
		bundle, grantErr := s.Grant(ctx, "interaction:"+interactionKey(key), s.Design.Rewards[point.RewardGroup])
		if grantErr != nil {
			return fail(grantErr)
		}
		st.Claims[interactionKey(key)] = true
		out = wire.AppendBytes(nil, 1, bundle)
	}
	if err != nil {
		return fail(err)
	}
	if path == "/SpineInteractionAchievementSave" || path == "/SpineInteractionReward" {
		raw, err = json.Marshal(st)
		if err == nil {
			err = s.Store.Save(ctx.State, "spine_interaction", raw)
		}
		if err != nil {
			return fail(err)
		}
	}
	return code, out, true, nil
}

func (s InteractionService) savePoints(ctx command.Context, request []byte, points map[string][]uint64) error {
	if len(request) > 256<<10 {
		return fmt.Errorf("interaction: save too large")
	}
	authorized := map[uint64]bool{}
	return wire.Walk(request, func(row wire.Field) error {
		if row.Number != 2 {
			return nil
		}
		if row.Type != 2 {
			return fmt.Errorf("interaction: invalid achievement row")
		}
		group, _, err := wire.Varint(row.Value, 1)
		if err != nil || group == 0 || group > 1<<31-1 {
			return fmt.Errorf("interaction: invalid group")
		}
		section, _, err := wire.Varint(row.Value, 2)
		if err != nil || section == 0 || section > 1<<31-1 {
			return fmt.Errorf("interaction: invalid section")
		}
		if !authorized[group] {
			allowed, err := s.Authorize(ctx, group)
			if err != nil || !allowed {
				return fmt.Errorf("interaction: group unavailable: %v", err)
			}
			authorized[group] = true
		}
		return wire.Walk(row.Value, func(f wire.Field) error {
			if f.Number != 3 {
				return nil
			}
			if f.Type != 2 {
				return fmt.Errorf("interaction: invalid point")
			}
			id, _, err := wire.Varint(f.Value, 1)
			key := gamedata.InteractionPointKey{group, section, id}
			point, known := s.Design.Points[key]
			if err != nil || !known || !point.Achievement {
				return fmt.Errorf("interaction: point not an achievement")
			}
			motions := append([]uint64{}, points[interactionKey(key)]...)
			err = wire.Walk(f.Value, func(m wire.Field) error {
				if m.Number != 2 {
					return nil
				}
				if m.Type != 0 && m.Type != 2 {
					return fmt.Errorf("interaction: invalid motion encoding")
				}
				for remaining := m.Value; len(remaining) > 0; {
					n, count := binary.Uvarint(remaining)
					if count <= 0 || !point.Motions[n] {
						return fmt.Errorf("interaction: motion does not belong to point")
					}
					if !slices.Contains(motions, n) {
						motions = append(motions, n)
					}
					remaining = remaining[count:]
				}
				return nil
			})
			if err != nil {
				return err
			}
			if len(point.Motions) != 0 && len(motions) == 0 {
				return fmt.Errorf("interaction: required motions missing")
			}
			slices.Sort(motions)
			points[interactionKey(key)] = motions
			return nil
		})
	})
}

func parseInteractionKey(text string) (gamedata.InteractionPointKey, error) {
	var key gamedata.InteractionPointKey
	parts := strings.Split(text, ":")
	if len(parts) != len(key) {
		return key, fmt.Errorf("interaction: malformed saved point")
	}
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 31)
		if err != nil || n == 0 {
			return key, fmt.Errorf("interaction: malformed saved point")
		}
		key[i] = n
	}
	return key, nil
}

func interactionPointWire(key gamedata.InteractionPointKey) []byte {
	row := wire.AppendVarint(nil, 1, key[0])
	row = wire.AppendVarint(row, 2, key[1])
	return wire.AppendVarint(row, 3, key[2])
}

func interactionInfo(points map[string][]uint64) ([]byte, error) {
	keys := make([]gamedata.InteractionPointKey, 0, len(points))
	for text := range points {
		key, err := parseInteractionKey(text)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b gamedata.InteractionPointKey) int { return slices.Compare(a[:], b[:]) })
	var out, group []byte
	var previous gamedata.InteractionPointKey
	for _, key := range keys {
		if key[0] != previous[0] || key[1] != previous[1] {
			if group != nil {
				out = wire.AppendBytes(out, 1, group)
			}
			group = wire.AppendVarint(nil, 1, key[0])
			group = wire.AppendVarint(group, 2, key[1])
		}
		row := wire.AppendVarint(nil, 1, key[2])
		for _, motion := range points[interactionKey(key)] {
			row = wire.AppendVarint(row, 2, motion)
		}
		group = wire.AppendBytes(group, 3, row)
		previous = key
	}
	if group != nil {
		out = wire.AppendBytes(out, 1, group)
	}
	return out, nil
}
