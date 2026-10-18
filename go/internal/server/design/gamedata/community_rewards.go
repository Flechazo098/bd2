package gamedata

import "fmt"

type CommunityRewards struct {
	Community, Survey []Reward
}

func LoadCommunityRewards(root, version string) (*CommunityRewards, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CommunityWebLinkTable WHERE id=0").Scan(&raw); err != nil {
		return nil, err
	}
	d := &CommunityRewards{}
	for _, r := range []struct {
		count, id, typ int
		out            *[]Reward
	}{{1, 2, 3, &d.Community}, {65, 66, 67, &d.Survey}} {
		rewards, err := monsterHuntRewardArrays(raw, r.typ, r.id, r.count)
		if err != nil || len(rewards) == 0 {
			return nil, fmt.Errorf("gamedata: invalid community reward arrays: %w", err)
		}
		for _, reward := range rewards {
			*r.out = append(*r.out, Reward(reward))
		}
	}
	return d, nil
}
