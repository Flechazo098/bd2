package player

import "errors"

// GachaBatchResponse returns the immutable result of a completed batch. The
// digest binds the full ordered request, including repeated gacha IDs.
func (s *CollectionStore) GachaBatchResponse(identity, digest string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, found := s.data.Grants[identity]
	if !found {
		return nil, false, nil
	}
	if grant.GachaRequestDigest != digest || len(grant.GachaResponse) == 0 {
		return nil, true, errors.New("player: gacha batch request identity conflict")
	}
	return append([]byte(nil), grant.GachaResponse...), true, nil
}

// RecordGachaBatch participates in the caller's account operation alongside
// every reward, daily allowance and fixed-pity update in the batch.
func (s *CollectionStore) RecordGachaBatch(identity, digest string, response []byte) error {
	if identity == "" || digest == "" || len(response) == 0 {
		return errors.New("player: invalid gacha batch result")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prior, found := s.data.Grants[identity]; found {
		if prior.GachaRequestDigest != digest {
			return errors.New("player: gacha batch request identity conflict")
		}
		return nil
	}
	next := cloneCollection(s.data)
	next.Grants[identity] = CollectionGrant{GachaRequestDigest: digest, GachaResponse: append([]byte(nil), response...)}
	return s.commit(next)
}
