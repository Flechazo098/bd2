package monsterhunt

import "strconv"

type publicRank struct {
	Rank              uint64
	Score, TopPercent float64
}

func (s *Service) ranking(u user) (publicRank, error) {
	score, err := s.score(u)
	if err != nil {
		return publicRank{}, err
	}
	return publicRank{Rank: 1, Score: score, TopPercent: 100}, nil
}

func (s *Service) ProfileRank() (uint64, float64, error) {
	season := s.current()
	u, ok := s.state.Users[strconv.FormatUint(season.ID, 10)]
	if !ok || !u.Played {
		return 0, 0, nil
	}
	rank, err := s.ranking(u)
	return rank.Rank, rank.TopPercent, err
}
