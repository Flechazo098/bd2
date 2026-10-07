package app

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/runtime/player"
	accountstate "bd2server/internal/server/storage/account"
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
	isLogin := len(request.Requests) == 1 && request.Requests[0].Path == "/LoginUser"
	receiptKey := request.Identity.SessionID + "/" + request.Identity.RequestID
	if !isLogin {
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
	transientBefore := assembly.transientVersion()
	for _, item := range request.Requests {
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
			for _, observer := range assembly.observers {
				if err = observer.BeforeDispatch(identity, item.Path, item.Body); err != nil {
					break
				}
			}
			reply.Timing.Observer += time.Since(started)
			if err == nil {
				started = time.Now()
				response.PacketCode, response.Body, err = assembly.dispatch(identity, item.Path, item.Body)
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
			changed := tx.Dirty() || assembly.transientVersion() != transientBefore
			started := time.Now()
			rollbackErr := tx.Rollback()
			reply.Timing.Rollback += time.Since(started)
			finished = true
			if rollbackErr != nil {
				p.assembly = nil
				return reply, player.Failure{Cause: errors.Join(err, rollbackErr), RecoveryRequired: true}
			}
			if !changed {
				return reply, err
			}
			p.assembly = nil
			if recoveryErr := p.Recover(context.WithoutCancel(ctx)); recoveryErr != nil {
				return reply, player.Failure{Cause: errors.Join(err, recoveryErr), RecoveryRequired: true}
			}
			return reply, player.Failure{Cause: err, RecoveryRequired: true, AlreadyRecovered: true}
		}

		reply.Responses = append(reply.Responses, response)
	}
	if tx.Dirty() && !isLogin {
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
	slog.Warn("player state recovered from committed snapshot", "account_id", p.accountID)
	return nil
}

func (p *playerInstance) Close() error {
	p.assembly = nil
	return p.repository.Close()
}

func (p *playerAssembly) transientVersion() [4]uint64 {
	return [4]uint64{p.battleService.TransientVersion(), p.monsterHuntService.TransientVersion(), p.gachaService.TransientVersion(), p.worldService.TransientVersion()}
}
