package app

import (
	"context"

	"bd2server/internal/server/domain/command"
	eventplay "bd2server/internal/server/domain/events/play"
	"bd2server/internal/server/domain/ranking"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/room"
)

func (r *roomResults) recordTeam(ctx context.Context, final room.FinalResult) error {
	if final.View.Kind != room.Action {
		return nil
	}
	stage := uint64(2)
	if final.View.Selection.Solo {
		stage = 1
	}
	record := ranking.CompletedTeamRun{ResultID: "room:" + final.View.InstanceID, InstanceID: final.View.InstanceID, EventUID: uint64(final.View.Selection.EventScheduleID), Monster: uint64(final.View.Selection.MonsterID), StageType: stage, Clear: final.Clear, Milliseconds: final.ScoreMilliseconds}
	for _, member := range final.View.Members {
		name, _, err := wire.Bytes(member.Profile.UserInfo, 4)
		if err != nil {
			return err
		}
		record.Members = append(record.Members, ranking.TeamMember{AccountID: member.Principal.AccountID, OwnerIndex: uint64(member.Principal.OwnerIndex), Name: string(name), CharacterID: uint64(member.CharacterID)})
	}
	return r.teams.Record(ctx, record)
}

func (r *roomResults) NativeRankings(ctx command.Context, event, monster, stage uint64) ([]eventplay.NativeRanking, error) {
	rows, err := r.teams.Rankings(ranking.TeamQuery{OwnerID: ctx.AccountID, EventUID: event, Monster: monster, StageType: stage, Limit: 100})
	if err != nil {
		return nil, err
	}
	out := make([]eventplay.NativeRanking, 0, len(rows))
	for _, row := range rows {
		result := eventplay.NativeRanking{InstanceID: row.InstanceID, EventUID: row.EventUID, Monster: row.Monster, StageType: row.StageType, Rank: row.Rank, Score: float64(row.Milliseconds), TopPercent: row.TopPercent, Own: row.Own}
		for _, member := range row.Members {
			result.Members = append(result.Members, eventplay.NativeRankMember{OwnerIndex: member.OwnerIndex, UserID: member.Name, CharacterID: member.CharacterID})
		}
		out = append(out, result)
	}
	return out, nil
}
