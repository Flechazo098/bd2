package app

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/social"
	"bd2server/internal/server/runtime/player"
	accountstate "bd2server/internal/server/storage/account"
	"bd2server/internal/server/storage/outbox"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"
)

type commandReceipt struct {
	Digest    string            `json:"digest"`
	Responses []player.Response `json:"responses"`
}

func (p *playerInstance) Execute(ctx context.Context, request player.Command) (reply player.Reply, result error) {
	if request.Identity.AccountID != p.accountID || p.assembly == nil {
		return reply, player.Failure{Cause: player.ErrUnavailable, RecoveryRequired: true}
	}
	if request.System != nil && request.System.Kind == player.ReconcileSocial {
		return p.reconcileSocial(ctx, request)
	}
	friendStarted := time.Now()
	friendResponses, err := p.prepareFriends(ctx, request)
	reply.Timing.Execute += time.Since(friendStarted)
	if err != nil {
		return reply, err
	}
	friendProgress := len(request.Requests) == 1 && len(friendResponses) == 1 && social.FriendMutates(request.Requests[0].Path)
	if len(request.Requests) == 1 && len(friendResponses) == 1 && !friendProgress {
		reply.Responses = []player.Response{friendResponses[0]}
		return reply, nil
	}
	if request.System == nil && hasPlayerText(request.Requests) {
		var receipt commandReceipt
		var found bool
		err := p.readLocal(ctx, request.Identity, func(c command.Context) error {
			raw, exists, err := c.State.LoadEntry("missions", "command_receipts", request.Identity.SessionID+"/"+request.Identity.RequestID)
			if err != nil || !exists {
				return err
			}
			found = true
			return json.Unmarshal(raw, &receipt)
		})
		if err != nil {
			return reply, err
		}
		if found {
			if receipt.Digest != hex.EncodeToString(request.Digest[:]) {
				return reply, player.ErrIdentityConflict
			}
			reply.Responses = receipt.Responses
			return reply, nil
		}
	}
	approvals, err := p.prepareText(ctx, request)
	if err != nil {
		return reply, err
	}
	if approvals != nil {
		ctx = context.WithValue(ctx, textApprovalKey{}, approvals)
		cancellation := request.Cancellation
		if cancellation == nil {
			cancellation = ctx
		}
		request.Cancellation = context.WithValue(cancellation, textApprovalKey{}, approvals)
	}
	restoreRooms, err := p.prepareRoomResults(ctx, request.Requests)
	if err != nil {
		return reply, err
	}
	defer restoreRooms()
	socialService, socialPlans, err := p.prepareSocial(ctx, request)
	if err != nil {
		return reply, err
	}
	defer func() {
		if result != nil || socialService == nil {
			return
		}
		for _, plan := range socialPlans {
			if err := socialService.Confirm(ctx, plan); err != nil {
				result = errors.Join(result, err)
			}
		}
	}()
	started := time.Now()
	tx, err := p.repository.BeginCommand(ctx)
	reply.Timing.Begin = time.Since(started)
	if err != nil {
		return reply, player.Failure{Cause: err, RecoveryRequired: true}
	}
	finished := false
	defer func() {
		if !finished {
			started := time.Now()
			result = errors.Join(result, tx.Rollback())
			reply.Timing.Rollback += time.Since(started)
			p.assembly = nil
			if result != nil {
				result = player.Failure{Cause: result, RecoveryRequired: true}
			}
		}
	}()

	identity := command.Context{Identity: request.Identity, Cancellation: request.Cancellation, State: tx}
	published := false
	var chatPolicyPayload []byte
	identity.Emit = func(event command.Event) error {
		if err := outbox.Publish(tx, p.accountID, event); err != nil {
			return err
		}
		published = true
		if event.Topic == "chat.policy" {
			chatPolicyPayload = append([]byte(nil), event.Payload...)
		}
		return nil
	}
	isLogin := len(request.Requests) == 1 && request.Requests[0].Path == "/LoginUser"
	isSystem := request.System != nil
	receiptKey := request.Identity.SessionID + "/" + request.Identity.RequestID
	if !isLogin && !isSystem {
		raw, found, err := tx.LoadEntry("missions", "command_receipts", receiptKey)
		if err != nil {
			return reply, err
		}
		if found {
			var receipt commandReceipt
			if err := json.Unmarshal(raw, &receipt); err != nil {
				return reply, player.Failure{Cause: fmt.Errorf("invalid command receipt: %w", err), RecoveryRequired: true}
			}
			if receipt.Digest != hex.EncodeToString(request.Digest[:]) {
				finished = true
				if err := tx.Rollback(); err != nil {
					p.assembly = nil
					return reply, player.Failure{Cause: errors.Join(player.ErrIdentityConflict, err), RecoveryRequired: true}
				}
				return reply, player.ErrIdentityConflict
			}
			reply.Responses = receipt.Responses
			finished = true
			if err := tx.Rollback(); err != nil {
				p.assembly = nil
				return reply, player.Failure{Cause: err, RecoveryRequired: true}
			}
			return reply, nil
		}
	}
	assembly := p.assembly
	if friendProgress {
		assembly.achievementObserver.BeginLogin(identity)
	}
	transientBefore := assembly.transientVersion()
	failCommand := func(cause error) error {
		changed := tx.Dirty() || assembly.transientVersion() != transientBefore
		started := time.Now()
		rollbackErr := tx.Rollback()
		reply.Timing.Rollback += time.Since(started)
		finished = true
		if rollbackErr != nil {
			p.assembly = nil
			return player.Failure{Cause: errors.Join(cause, rollbackErr), RecoveryRequired: true}
		}
		if !changed {
			return cause
		}
		p.assembly = nil
		if recoveryErr := p.Recover(context.WithoutCancel(ctx)); recoveryErr != nil {
			return player.Failure{Cause: errors.Join(cause, recoveryErr), RecoveryRequired: true}
		}
		return player.Failure{Cause: cause, RecoveryRequired: true, AlreadyRecovered: true}
	}
	if isSystem {
		body, err := p.executeSystem(identity, tx, request.System)
		if err != nil {
			return reply, failCommand(err)
		}
		reply.Responses = []player.Response{{Body: body}}
	}
	for index, item := range request.Requests {
		var response player.Response
		if isLogin {
			started := time.Now()
			err = assembly.beginLogin(identity)
			if err == nil {
				response.Body, err = assembly.login.Login(identity, item.Body, request.LoginSessionKey)
				response.PacketCode = 3
			}
			reply.Timing.Execute += time.Since(started)
		} else {
			started := time.Now()
			if item.Path == "/AchievementInfo" || item.Path == "/AchievementClear" {
				assembly.achievementObserver.BeginLogin(identity)
			}
			for _, observer := range assembly.observers {
				if err = observer.BeforeDispatch(identity, item.Path, item.Body); err != nil {
					break
				}
			}
			reply.Timing.Observer += time.Since(started)
			if err == nil {
				started = time.Now()
				if friendResponse, found := friendResponses[index]; found {
					response = friendResponse
				} else if plan := socialPlans[index]; plan != nil {
					response.PacketCode, response.Body, err = socialService.Apply(identity, plan)
					if err == nil && plan.Reservation != nil && plan.Reservation.Kind == social.SupporterClaim {
						_, err = assembly.achievementCounters.RecordEvent(identity, "supporter-claim:"+plan.ID, 101, 0, uint64(len(plan.Reservation.UsageIDs)))
					}
				} else if item.Path == "/TotalRanking" {
					response.PacketCode, response.Body, err = p.totalRanking(identity, item.Body)
				} else {
					response.PacketCode, response.Body, err = assembly.dispatch(identity, item.Path, item.Body)
				}
				reply.Timing.Execute += time.Since(started)
			}
			if err == nil {
				started = time.Now()
				for _, observer := range assembly.observers {
					var notify []byte

					notify, err = observer.AfterDispatch(identity, item.Path, item.Body, response.Body)
					if err != nil {
						break
					}
					response.Notification = append(response.Notification, notify...)
				}
				reply.Timing.Observer += time.Since(started)
			}
		}
		if err != nil {
			return reply, failCommand(err)
		}

		reply.Responses = append(reply.Responses, response)
	}
	if !isSystem && (tx.Dirty() || isLogin) {
		if isLogin || arcadeRankingChanged(request.Requests) {
			if err := assembly.publishArcadeRanking(identity); err != nil {
				return reply, failCommand(err)
			}
		}
		if isLogin || huntRankingChanged(request.Requests) {
			if err := assembly.publishHuntRanking(identity); err != nil {
				return reply, failCommand(err)
			}
		}
		if isLogin || friendProfileChanged(request.Requests) {
			if err := assembly.publishFriendProfile(identity, isLogin); err != nil {
				return reply, failCommand(err)
			}
		}
		if isLogin {
			if err := assembly.chatService.PublishPolicy(identity); err != nil {
				return reply, err
			}
		}
		if err := assembly.publishDirectory(identity); err != nil {
			return reply, failCommand(err)
		}
	}
	if tx.Dirty() && !isLogin && !isSystem {
		raw, err := json.Marshal(commandReceipt{Digest: hex.EncodeToString(request.Digest[:]), Responses: reply.Responses})
		if err != nil {
			return reply, err
		}
		if err := tx.PutEntry("missions", "command_receipts", receiptKey, raw); err != nil {
			return reply, err
		}
	}
	started = time.Now()
	err = tx.Commit()
	reply.Timing.Commit += time.Since(started)
	finished = true
	if err != nil {
		p.assembly = nil
		return reply, player.Failure{Cause: err, RecoveryRequired: true}
	}
	if isLogin && p.factory.profiles != nil {
		if err := p.factory.profiles.RecordGameLogin(ctx, p.accountID, time.Now()); err != nil {
			slog.Warn("game login timestamp synchronization failed", "account_id", p.accountID, "error", err)
		}
	}
	if published && p.factory.wakeOutbox != nil {
		p.factory.wakeOutbox(p.accountID)
	}
	if len(chatPolicyPayload) > 0 && p.factory.shared != nil {
		if err := p.factory.shared.consumeChatPolicy(ctx, p.accountID, chatPolicyPayload); err != nil {
			slog.Warn("chat policy publication deferred", "account_id", p.accountID, "error", err)
		}
	}
	return reply, nil
}

func (p *playerAssembly) dispatch(ctx command.Context, path string, request []byte) (int, []byte, error) {
	for _, handler := range p.handlers {
		code, body, handled, err := handler.Handle(ctx, path, request)
		if handled || err != nil {
			return code, body, err
		}
	}
	return 0, nil, fmt.Errorf("packet not implemented: %s", path)
}

func (p *playerAssembly) beginLogin(ctx command.Context) error {
	for _, observer := range p.observers {
		if hook, ok := observer.(interface{ BeginLogin(command.Context) }); ok {
			hook.BeginLogin(ctx)
		}
	}
	for _, handler := range p.handlers {
		if hook, ok := handler.(interface{ BeginLogin(command.Context) }); ok {
			hook.BeginLogin(ctx)
		}
	}
	return p.missionService.RecordLogin(ctx, p.worldService.MissionsUnlocked)
}

func (p *playerInstance) Recover(ctx context.Context) error {
	if err := p.repository.Check(); err != nil {
		if errors.Is(err, accountstate.ErrFenced) {
			return err
		}
		if closeErr := p.repository.Close(); closeErr != nil {
			return errors.Join(err, closeErr)
		}
		path := filepath.Join(p.factory.options.stateDirectory, "accounts", accountDirectoryName(p.accountID), "state.db")
		repository, openErr := accountstate.Open(path)
		if openErr != nil {
			return errors.Join(err, openErr)
		}
		p.repository = repository
	}
	assembly, err := p.factory.assemble(ctx, p.accountID, p.repository)
	if err != nil {
		return err
	}
	p.assembly = assembly
	p.sharedSocial = nil
	if err := p.recoverSharedBattle(ctx); err != nil {
		return err
	}
	slog.Warn("player state recovered from committed snapshot", "account_id", p.accountID)
	return nil
}

func (p *playerInstance) Close() error {
	p.assembly = nil
	p.sharedSocial = nil
	return p.repository.Close()
}

func (p *playerAssembly) transientVersion() [4]uint64 {
	return [4]uint64{p.battleService.TransientVersion(), p.monsterHuntService.TransientVersion(), p.gachaService.TransientVersion(), p.worldService.TransientVersion()}
}
