package social

import (
	"context"
	"encoding/json"
	"fmt"
)

func projectGuild(tx Tx, g Guild, a Account, op Operation, r Reservation) (Guild, error) {
	raw, err := json.Marshal(g)
	if err != nil {
		return Guild{}, err
	}
	var view Guild
	if err = json.Unmarshal(raw, &view); err != nil {
		return Guild{}, err
	}
	view.PendingOperation = ""
	view.PendingManagement = ""
	switch op.Kind {
	case Create, Join, Accept:
		if !r.Application {
			profile := op.Actor
			role := Member
			if op.Kind == Create {
				role = Admin
			}
			if op.Kind == Accept {
				var app Application
				found, e := load(tx, applicationBucket, applicationKey(g.ID, r.SubjectAccount), &app)
				if e != nil {
					return Guild{}, e
				}
				if !found {
					return Guild{}, fmt.Errorf("%w: no application from %s to guild %d", ErrNotFound, r.SubjectAccount, g.ID)
				}
				profile = app.Applicant
			}
			view.Members[r.SubjectAccount] = Membership{PublicMember: profile, Role: role, Epoch: a.Epoch + 1, JoinedMS: op.AtMS}
		}
	case Edit:
		view.Icon, view.IconColor, view.Message, view.JoinType = op.Create.Icon, op.Create.IconColor, op.Create.Message, op.Create.JoinType
	case Rename:
		view.Name = op.Text
	case Notice:
		view.Notice, view.NoticeUpdatedMS = op.Text, op.AtMS
	case ChangeRole:
		m := view.Members[r.SubjectAccount]
		m.Role = op.Role
		view.Members[r.SubjectAccount] = m
		if op.Role == Admin {
			m = view.Members[op.Actor.AccountID]
			m.Role = Member
			view.Members[op.Actor.AccountID] = m
		}
	}
	if err := projectMemberProfiles(tx, &view); err != nil {
		return Guild{}, err
	}
	return view, nil
}

// RecoverReservation is called only after resolving the player's durable intent.
// A nil proof means the player transaction is known not to have committed.
func (o *Owner) RecoverReservation(ctx context.Context, id string, proof *CommitProof) (Reservation, error) {
	if proof != nil {
		if proof.OperationID != id {
			return Reservation{}, fmt.Errorf("%w: proof names operation %q, not %q", ErrConflict, proof.OperationID, id)
		}
		return o.Confirm(ctx, *proof)
	}
	r, err := o.Reservation(ctx, id)
	if err != nil {
		return Reservation{}, err
	}
	return o.Cancel(ctx, CommitProof{OperationID: r.ID, ActorAccount: r.ActorAccount, Digest: r.Digest})
}
