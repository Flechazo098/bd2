package gamedata

import "fmt"

type FriendRules struct{ Friends, Sent, Received, Recommended, ExpireDays uint64 }

func LoadFriendRules(root, version string) (FriendRules, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return FriendRules{}, err
	}
	defer done()
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CommunityDefineTable WHERE id=0").Scan(&raw); err != nil {
		return FriendRules{}, err
	}
	v, err := cashScalars(raw, 3, 6, 5, 4, 1)
	if err != nil {
		return FriendRules{}, err
	}
	for _, n := range v {
		if n == 0 || n > 256 {
			return FriendRules{}, fmt.Errorf("gamedata: invalid friend limits")
		}
	}
	return FriendRules{Friends: v[0], Sent: v[1], Received: v[2], Recommended: v[3], ExpireDays: v[4]}, nil
}
