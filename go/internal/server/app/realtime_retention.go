package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"

	"bd2server/internal/server/domain/command"
	eventplay "bd2server/internal/server/domain/events/play"
	"bd2server/internal/server/runtime/room"
	"bd2server/internal/server/storage/outbox"
	"bd2server/internal/server/storage/shared"
)

type preparedRoomResult struct {
	result eventplay.NativeResult
	err    error
}

type preparedRoomResults struct {
	*roomResults
	account string
	results map[eventplay.NativeRun]preparedRoomResult
}

// PrepareResults reads durable entitlements before the caller opens its player transaction.
func (r *roomResults) PrepareResults(ctx context.Context, account string, runs []eventplay.NativeRun) (eventplay.RoomRuntime, error) {
	if account == "" {
		return nil, room.ErrUnauthorized
	}
	prepared := &preparedRoomResults{roomResults: r, account: account, results: make(map[eventplay.NativeRun]preparedRoomResult, len(runs))}
	err := r.store.View(ctx, func(tx *shared.Tx) error {
		for _, run := range runs {
			if _, loaded := prepared.results[run]; loaded {
				continue
			}
			raw, found, err := tx.Get("room.results", run.InstanceID)
			if err != nil {
				return err
			}
			var result eventplay.NativeResult
			if found {
				_, ready, readyErr := tx.Get("room.ready", run.InstanceID)
				if readyErr != nil {
					return readyErr
				}
				if !ready {
					prepared.results[run] = preparedRoomResult{err: eventplay.ErrRoomNotFinished}
					continue
				}
				var final room.FinalResult
				if err := json.Unmarshal(raw, &final); err != nil {
					return err
				}
				if final.View.InstanceID != run.InstanceID {
					return errors.New("invalid durable room result")
				}
				result, err = projectRoomResult(account, run, final)
			} else {
				result, err = r.Result(command.Context{Identity: command.Identity{AccountID: account}}, run)
			}
			prepared.results[run] = preparedRoomResult{result: result, err: err}
		}
		return nil
	})
	return prepared, err
}

func (r *preparedRoomResults) Result(ctx command.Context, run eventplay.NativeRun) (eventplay.NativeResult, error) {
	if ctx.AccountID != r.account {
		return eventplay.NativeResult{}, room.ErrUnauthorized
	}
	prepared, found := r.results[run]
	if !found {
		// A run created in this command is eligible for prefetch on the next command.
		return eventplay.NativeResult{}, eventplay.ErrRoomNotFinished
	}
	result := prepared.result
	result.Evidence = append([]byte(nil), result.Evidence...)
	return result, prepared.err
}

func projectRoomResult(account string, run eventplay.NativeRun, final room.FinalResult) (eventplay.NativeResult, error) {
	if string(final.View.Kind) != strings.ToLower(run.Family) {
		return eventplay.NativeResult{}, room.ErrUnauthorized
	}
	for _, member := range final.View.Members {
		if member.Principal.AccountID != account || member.NativeGUID != run.GUID {
			continue
		}
		score := final.ScoreMilliseconds
		if final.View.Kind == room.Defense {
			score = member.Wave
		}
		if score < 0 {
			return eventplay.NativeResult{}, errors.New("invalid negative room score")
		}
		return eventplay.NativeResult{Clear: final.Clear, Score: uint64(score), Evidence: append([]byte(nil), final.Evidence...)}, nil
	}
	return eventplay.NativeResult{}, room.ErrUnauthorized
}

func (r *roomResults) recoverResults(ctx context.Context) error {
	for cursor := ""; ; {
		var rows []shared.Entry
		if err := r.store.View(ctx, func(tx *shared.Tx) error {
			var err error
			rows, err = tx.Scan("room.results", "", cursor, 128)
			return err
		}); err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			var final room.FinalResult
			if err := json.Unmarshal(row.Value, &final); err != nil {
				return err
			}
			if row.Key != final.View.InstanceID {
				return errors.New("invalid durable room result")
			}
			if err := r.recordTeam(ctx, final); err != nil {
				return err
			}
			if err := r.retireClaimedResult(ctx, final); err != nil {
				return err
			}
			cursor = row.Key
		}
	}
}

func roomResultDigest(final room.FinalResult) ([]byte, error) {
	raw, err := json.Marshal(final)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func retiredRoomResult(tx *shared.Tx, final room.FinalResult) (bool, error) {
	prior, found, err := tx.Get("room.retired", final.View.InstanceID)
	if err != nil || !found {
		return false, err
	}
	digest, err := roomResultDigest(final)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(prior, digest) {
		return false, outbox.ErrConflict
	}
	return true, nil
}

func (r *roomResults) retireClaimedResult(ctx context.Context, final room.FinalResult) error {
	return r.store.Update(ctx, func(tx *shared.Tx) error {
		retired, err := retiredRoomResult(tx, final)
		if err != nil || retired {
			return err
		}
		if err := tx.Put("room.ready", final.View.InstanceID, []byte{1}); err != nil {
			return err
		}
		return retireClaimedRoom(tx, final)
	})
}

func retireClaimedRoom(tx *shared.Tx, final room.FinalResult) error {
	_, ready, err := tx.Get("room.ready", final.View.InstanceID)
	if err != nil || !ready {
		return err
	}
	if len(final.View.Members) == 0 {
		return nil
	}
	for _, member := range final.View.Members {
		_, claimed, err := tx.Get("room.claims", final.View.InstanceID+":"+roomMemberKey(member.Principal.AccountID))
		if err != nil || !claimed {
			return err
		}
	}
	digest, err := roomResultDigest(final)
	if err != nil {
		return err
	}
	// Keep the compact identity receipt so a delayed persistence retry cannot resurrect the payload.
	if err := tx.Put("room.retired", final.View.InstanceID, digest); err != nil {
		return err
	}
	if _, err := tx.Delete("room.results", final.View.InstanceID); err != nil {
		return err
	}
	if _, err := tx.Delete("room.ready", final.View.InstanceID); err != nil {
		return err
	}
	for _, bucket := range []string{"room.runs", "room.claims"} {
		prefix := final.View.InstanceID + ":"
		for cursor := ""; ; {
			rows, err := tx.Scan(bucket, prefix, cursor, 128)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				if _, err := tx.Delete(bucket, row.Key); err != nil {
					return err
				}
				cursor = row.Key
			}
		}
	}
	return nil
}
