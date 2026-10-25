package ranking

import (
	"encoding/json"
	"errors"

	"bd2server/internal/server/domain/battle/pvp"
	"bd2server/internal/server/domain/matchmaking"
)

// PVP skill ratings persist per mode across seasons: VP resets every season,
// skill does not.
type pvpRatingRow struct {
	Owner  string
	Rating matchmaking.Rating
}

func pvpRatingKey(mode pvp.Mode, owner string) string {
	return string(mode) + ":" + pvpMemberKey(owner)
}

func newPVPRater() (matchmaking.System, error) {
	return matchmaking.NewSystem(matchmaking.DefaultConfig, pvp.PlayerCovariates, pvp.ContestCovariates)
}

func (p *PVP) loadRatings(tx Tx) error {
	for _, mode := range []pvp.Mode{pvp.Mirror, pvp.Colosseum} {
		p.ratings[mode] = map[string]matchmaking.Rating{}
		p.models[mode] = p.rater.NewModel()
		raw, found, err := tx.Get("pvp.rating_models", string(mode))
		if err != nil {
			return err
		}
		if found {
			var model matchmaking.Model
			if err = json.Unmarshal(raw, &model); err != nil {
				return err
			}
			if len(model.Player) != len(p.models[mode].Player) || len(model.Contest) != len(p.models[mode].Contest) {
				return errors.New("invalid saved PVP rating model")
			}
			p.models[mode] = model
		}
		for cursor := ""; ; {
			page, err := tx.Scan("pvp.ratings", string(mode)+":", cursor, 256)
			if err != nil {
				return err
			}
			if len(page) == 0 {
				break
			}
			for _, entry := range page {
				var row pvpRatingRow
				if err := json.Unmarshal(entry.Value, &row); err != nil {
					return err
				}
				if row.Owner == "" || entry.Key != pvpRatingKey(mode, row.Owner) || row.Rating.Phi <= 0 || row.Rating.Sigma <= 0 {
					return errors.New("invalid saved PVP rating")
				}
				p.ratings[mode][row.Owner] = row.Rating
				cursor = entry.Key
			}
		}
	}
	return nil
}

func (p *PVP) rating(mode pvp.Mode, owner string) matchmaking.Rating {
	if r, ok := p.ratings[mode][owner]; ok {
		return r
	}
	return p.rater.New()
}

// settleRatings rates both sides of an accepted battle inside its transaction
// and returns the in-memory commit.
func (p *PVP) settleRatings(tx Tx, battle pvp.BattleSettled) (func(), error) {
	result, err := pvp.RatingResult(battle)
	if err != nil {
		return nil, err
	}
	attacker, defender := battle.Attacker.Profile.OwnerID, battle.Defender.Profile.OwnerID
	contest := pvp.RatingContest(battle.Attacker, battle.Defender, battle.FirstAttack)
	model, a, d, err := p.rater.Settle(p.models[battle.Mode], p.rating(battle.Mode, attacker), p.rating(battle.Mode, defender), contest, result, battle.Timestamp)
	if err != nil {
		return nil, err
	}
	for owner, r := range map[string]matchmaking.Rating{attacker: a, defender: d} {
		raw, err := json.Marshal(pvpRatingRow{Owner: owner, Rating: r})
		if err != nil {
			return nil, err
		}
		if err = tx.Put("pvp.ratings", pvpRatingKey(battle.Mode, owner), raw); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	if err = tx.Put("pvp.rating_models", string(battle.Mode), raw); err != nil {
		return nil, err
	}
	return func() {
		p.ratings[battle.Mode][attacker], p.ratings[battle.Mode][defender] = a, d
		p.models[battle.Mode] = model
	}, nil
}
