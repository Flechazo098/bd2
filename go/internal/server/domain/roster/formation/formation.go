// Package formation validates the shared battle-grid structure of parties and presets.
package formation

import (
	"fmt"
	"math"

	"bd2server/internal/server/protocol/wire"
)

const (
	StandardCapacity = 5
	StandardCells    = 12
	GuildCapacity    = 7
	GuildCells       = 25
	HuntTeams        = 3
)

type Rules struct {
	Capacity        uint64
	Cells           uint64
	AllowUnassigned bool
}

type Member struct {
	Character uint64
	Position  uint64
	Sequence  uint64
}

func DecodeDeck(raw []byte) (Member, error) {
	var member Member
	for field, target := range map[int]*uint64{1: &member.Character, 2: &member.Position, 3: &member.Sequence} {
		value, _, err := wire.Varint(raw, field)
		if err != nil {
			return Member{}, err
		}
		*target = value
	}
	if member.Character == 0 || member.Character > math.MaxInt64 {
		return Member{}, fmt.Errorf("formation: invalid character %d", member.Character)
	}
	return member, nil
}

// A zero Character omits identity checks for externally resolved supporter slots.
func (r Rules) Validate(members []Member) error {
	if r.Capacity == 0 || r.Cells == 0 || uint64(len(members)) > r.Capacity {
		return fmt.Errorf("formation: %d members exceed capacity %d", len(members), r.Capacity)
	}
	characters, positions, sequences := map[uint64]bool{}, map[uint64]bool{}, map[uint64]bool{}
	for _, member := range members {
		unassigned := r.AllowUnassigned && member.Position == math.MaxUint64
		if !unassigned && member.Position >= r.Cells {
			return fmt.Errorf("formation: position %d outside 0..%d", member.Position, r.Cells-1)
		}
		if member.Sequence == 0 || member.Sequence > r.Capacity {
			return fmt.Errorf("formation: sequence %d outside 1..%d", member.Sequence, r.Capacity)
		}
		if member.Character != 0 && characters[member.Character] || !unassigned && positions[member.Position] || sequences[member.Sequence] {
			return fmt.Errorf("formation: repeated character %d, position %d or sequence %d", member.Character, member.Position, member.Sequence)
		}
		if member.Character != 0 {
			characters[member.Character] = true
		}
		if !unassigned {
			positions[member.Position] = true
		}
		sequences[member.Sequence] = true
	}
	return nil
}

// MergeCostumeSettings follows the native character/mode/monster replacement key.
func MergeCostumeSettings(previous, requested [][]byte) ([][]byte, error) {
	out := make([][]byte, 0, len(previous)+len(requested))
	indices := map[[3]uint64]int{}
	for _, rows := range [][][]byte{previous, requested} {
		for _, raw := range rows {
			var key [3]uint64
			for index, field := range [...]int{1, 3, 4} {
				value, _, err := wire.Varint(raw, field)
				if err != nil {
					return nil, err
				}
				key[index] = value
			}
			if key[0] == 0 || key[0] > math.MaxInt64 {
				return nil, fmt.Errorf("formation: invalid costume setting character %d", key[0])
			}
			if index, exists := indices[key]; exists {
				out[index] = raw
			} else {
				indices[key] = len(out)
				out = append(out, raw)
			}
		}
	}
	return out, nil
}
