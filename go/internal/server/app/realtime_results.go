package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"bd2server/internal/server/domain/command"
	eventplay "bd2server/internal/server/domain/events/play"
	"bd2server/internal/server/domain/ranking"
	"bd2server/internal/server/runtime/room"
	"bd2server/internal/server/storage/outbox"
	"bd2server/internal/server/storage/shared"
)

func roomMemberKey(account string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(account))
}

type roomResults struct {
	teams     *ranking.Teams
	endpoints []eventplay.RelayEndpoint
	deliver   func([]room.Delivery)
	store     *shared.Store
	rooms     *room.Manager
	pending   sync.Map
	queue     chan room.FinalResult
	cancel    context.CancelFunc
	done      chan struct{}
}

func (r *roomResults) RelayEndpoints() []eventplay.RelayEndpoint {
	return append([]eventplay.RelayEndpoint(nil), r.endpoints...)
}

func openRoomResults(ctx context.Context, store *shared.Store) (*roomResults, error) {
	r := &roomResults{store: store, queue: make(chan room.FinalResult, 128), done: make(chan struct{})}
	var err error
	r.teams, err = ranking.NewTeams(ctx, rankingStore{store})
	if err != nil {
		return nil, err
	}
	err = r.recoverResults(ctx)
	return r, err
}

func (r *roomResults) publish(final room.FinalResult) bool {
	r.pending.Store(final.View.InstanceID, struct{}{})
	select {
	case r.queue <- final:
		return true
	default:
		r.pending.Delete(final.View.InstanceID)
		return false
	}
}

func (r *roomResults) start() {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() {
		defer close(r.done)
		for {
			select {
			case <-ctx.Done():
				return
			case final, ok := <-r.queue:
				if !ok {
					return
				}
				for {
					err := r.persist(ctx, final)
					if err == nil {
						break
					}
					if ctx.Err() != nil {
						return
					}
					slog.Warn("room result persistence deferred", "instance_id", final.View.InstanceID, "error", err)
					timer := time.NewTimer(time.Second)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
			}
		}
	}()
}

func (r *roomResults) persist(ctx context.Context, final room.FinalResult) error {
	raw, err := json.Marshal(final)
	if err != nil {
		return err
	}
	err = r.store.Update(ctx, func(tx *shared.Tx) error {
		retired, err := retiredRoomResult(tx, final)
		if err != nil || retired {
			return err
		}
		old, found, err := tx.Get("room.results", final.View.InstanceID)
		if err != nil {
			return err
		}
		if found {
			var saved room.FinalResult
			if err := json.Unmarshal(old, &saved); err != nil {
				return err
			}
			if saved.ScoreMilliseconds != final.ScoreMilliseconds || saved.Clear != final.Clear || saved.View.Kind != final.View.Kind {
				return fmt.Errorf("%w: room %q result was saved with different values", outbox.ErrConflict, final.View.InstanceID)
			}
			final = saved
			return nil
		}
		return tx.Put("room.results", final.View.InstanceID, raw)
	})
	if err != nil {
		return err
	}
	if err := r.recordTeam(ctx, final); err != nil {
		return err
	}
	if err := r.retireClaimedResult(ctx, final); err != nil {
		return err
	}
	r.pending.Delete(final.View.InstanceID)
	if r.rooms == nil {
		return nil
	}
	err = r.rooms.AcknowledgeResult(ctx, final.View.InstanceID)
	if errors.Is(err, room.ErrNotFound) || errors.Is(err, room.ErrClosed) {
		return nil
	}
	return err
}

func (r *roomResults) StartRun(ctx command.Context, family string) (eventplay.NativeRun, error) {
	if r.rooms == nil {
		return eventplay.NativeRun{}, fmt.Errorf("%w: realtime rooms are not running", eventplay.ErrNoRoom)
	}
	kind := room.Kind(strings.ToLower(family))
	view, err := r.rooms.SnapshotForSession(ctx.AccountID, ctx.SessionID, kind)
	if err != nil {
		if errors.Is(err, room.ErrNotFound) {
			return eventplay.NativeRun{}, fmt.Errorf("%w: %v", eventplay.ErrNoRoom, err)
		}
		return eventplay.NativeRun{}, err
	}
	if view.RoomState != 2 && (kind != room.Fishing || view.RoomState != 1) {
		return eventplay.NativeRun{}, fmt.Errorf("%w: %s room %q is in state %d", room.ErrNotReady, kind, view.InstanceID, view.RoomState)
	}
	for _, member := range view.Members {
		if member.Principal.AccountID != ctx.AccountID || member.Principal.SessionID != ctx.SessionID {
			continue
		}
		if (!member.Ready && kind != room.Fishing) || member.NativeGUID == "" {
			return eventplay.NativeRun{}, fmt.Errorf("%w: %s is not ready in %s room %q", room.ErrNotReady, ctx.AccountID, kind, view.InstanceID)
		}
		started := int64(0)
		if !view.StartTime.IsZero() {
			started = view.StartTime.UnixMilli()
		}
		return eventplay.NativeRun{InstanceID: view.InstanceID, GUID: member.NativeGUID, Family: family, SessionID: ctx.SessionID, UID: uint64(view.Selection.EventScheduleID), Group: uint64(view.Selection.GroupID), Stage: uint64(view.Selection.StageID), Monster: uint64(view.Selection.MonsterID), Started: started, MapID: uint64(view.Selection.MapID), BoatLevel: uint64(view.Selection.BoatLevel), Host: member.Principal.OwnerIndex == view.HostOwnerIndex, Members: uint64(len(view.Members))}, nil
	}
	return eventplay.NativeRun{}, fmt.Errorf("%w: session %s of %s is not in %s room %q", room.ErrUnauthorized, ctx.SessionID, ctx.AccountID, kind, view.InstanceID)
}

func (r *roomResults) Result(ctx command.Context, run eventplay.NativeRun) (eventplay.NativeResult, error) {
	if _, pending := r.pending.Load(run.InstanceID); pending {
		return eventplay.NativeResult{}, fmt.Errorf("%w: room %q result is pending", eventplay.ErrRoomNotFinished, run.InstanceID)
	}
	if r.rooms != nil {
		view, err := r.rooms.SnapshotForSession(ctx.AccountID, run.SessionID, room.Kind(strings.ToLower(run.Family)))
		if err == nil && view.InstanceID == run.InstanceID {
			return eventplay.NativeResult{}, fmt.Errorf("%w: room %q is still open", eventplay.ErrRoomNotFinished, run.InstanceID)
		}
	}
	return eventplay.NativeResult{}, fmt.Errorf("%w: room %q has no result", eventplay.ErrRoomAbandoned, run.InstanceID)
}

func (r *roomResults) Consume(ctx context.Context, event outbox.Pending) error {
	var run eventplay.NativeRun
	if err := json.Unmarshal(event.Envelope.Event.Payload, &run); err != nil {
		return err
	}
	if run.InstanceID == "" || run.GUID == "" {
		return errors.New("invalid committed room run")
	}
	if event.Envelope.Event.Topic == "room.run.claimed" {
		return r.claim(ctx, event, run)
	}
	if event.Envelope.Event.Topic != "room.run.accepted" {
		return errors.New("unsupported room event")
	}
	if err := r.store.Consume(ctx, event, func(tx *shared.Tx, envelope outbox.Envelope) error {
		return tx.Put("room.runs", run.InstanceID+":"+roomMemberKey(envelope.Source), envelope.Event.Payload)
	}); err != nil {
		return err
	}
	kind := room.Kind(strings.ToLower(run.Family))
	if r.rooms == nil {
		return nil
	}
	view, err := r.rooms.SnapshotForSession(event.Envelope.Source, run.SessionID, kind)
	if err != nil {
		return nil
	}
	for _, member := range view.Members {
		if member.Principal.AccountID == event.Envelope.Source && member.NativeGUID == run.GUID {
			return r.rooms.AcceptRun(ctx, member.Principal, kind, run.InstanceID, run.GUID)
		}
	}
	return nil
}

func (r *roomResults) claim(ctx context.Context, event outbox.Pending, run eventplay.NativeRun) error {
	err := r.store.Consume(ctx, event, func(tx *shared.Tx, envelope outbox.Envelope) error {
		raw, found, err := tx.Get("room.results", run.InstanceID)
		if err != nil {
			return err
		}
		if !found {
			if run.Family != "Fishing" {
				return errors.New("room claim has no durable result")
			}
			saved, accepted, err := tx.Get("room.runs", run.InstanceID+":"+roomMemberKey(envelope.Source))
			if err != nil {
				return err
			}
			var own eventplay.NativeRun
			if !accepted || json.Unmarshal(saved, &own) != nil || own != run {
				return fmt.Errorf("%w: fishing room %q has no accepted run of %s matching the claim", room.ErrUnauthorized, run.InstanceID, envelope.Source)
			}
			key := run.InstanceID + ":" + roomMemberKey(envelope.Source)
			if err := tx.Put("room.claims", key, []byte(run.GUID)); err != nil {
				return err
			}
			_, err = tx.Delete("room.runs", key)
			return err
		}
		var final room.FinalResult
		if err := json.Unmarshal(raw, &final); err != nil {
			return err
		}
		memberFound := false
		for _, member := range final.View.Members {
			memberFound = memberFound || member.Principal.AccountID == envelope.Source && member.NativeGUID == run.GUID
		}
		if !memberFound {
			return fmt.Errorf("%w: room %q result has no member %s with guid %q", room.ErrUnauthorized, run.InstanceID, envelope.Source, run.GUID)
		}
		if err := tx.Put("room.claims", run.InstanceID+":"+roomMemberKey(envelope.Source), []byte(run.GUID)); err != nil {
			return err
		}
		return retireClaimedRoom(tx, final)
	})
	return err
}

func (r *roomResults) stop() {
	if r.cancel == nil {
		return
	}
	close(r.queue)
	timer := time.AfterFunc(30*time.Second, r.cancel)
	<-r.done
	timer.Stop()
	r.cancel()
}
