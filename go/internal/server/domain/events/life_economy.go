package events

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

type LifeEconomy interface {
	CanApply(command.Context, []gamedata.Reward, []gamedata.Reward) error
	Apply(command.Context, string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}

func (e *Economy) AttachLife(life LifeEconomy) { e.life = life }

func lifeCurrency(typ uint64) bool { return typ >= 64 && typ <= 66 }

// ordinaryLifeBundle adapts a life reward body to an ordinary
// RewardDBInfoBundle. CommonPacket.AddItemInfo files every item_info entry
// under the ordinary inventory and only life coins have a currency branch
// there, so life furniture and items stay in view_item_info; LifeInfo, sent
// on entering the farm, reloads the life inventory.
func ordinaryLifeBundle(life []byte) ([]byte, error) {
	var bundle []byte
	err := wire.Walk(life, func(f wire.Field) error {
		if f.Number == 1 && f.Type == 2 {
			typ, _, err := wire.Varint(f.Value, 3)
			if err != nil {
				return err
			}
			if typ == 65 || typ == 66 {
				return nil
			}
		}
		bundle = append(bundle, life[f.Start:f.End]...)
		return nil
	})
	return bundle, err
}
