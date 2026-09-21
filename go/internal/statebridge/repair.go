package statebridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	controlv1 "bd2server/gen/state/control/v1"
	controlv2 "bd2server/gen/state/control/v2"
	statev2 "bd2server/gen/state/v2"
	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"google.golang.org/protobuf/proto"
)

type RepairResult struct {
	SourceSHA256 [32]byte
	TargetSHA256 [32]byte
	Target       *statev2.Snapshot
	Changed      bool
	Violations   []*controlv1.Violation
}

func BuildValidationContext(gameDataVersion string, catalog *gamedata.RegularGachaCatalog, starter *player.Starter, stepGroupID uint64, stepGachaIDs []uint64) (*controlv2.ValidationContext, error) {
	if gameDataVersion == "" || catalog == nil || starter == nil || stepGroupID == 0 || len(stepGachaIDs) == 0 {
		return nil, errors.New("statebridge: incomplete validation context")
	}
	gachaFacts, costumeFacts := catalog.MigrationFacts()
	context := &controlv2.ValidationContext{GameDataVersion: gameDataVersion}
	for _, fact := range gachaFacts {
		context.GachaGroups = append(context.GachaGroups, &controlv2.GachaGroupFact{
			GachaId: fact.GachaID, GroupId: fact.GroupID, FixedId: fact.FixedID, PointCount: fact.PointCount,
		})
	}
	for _, fact := range costumeFacts {
		context.Costumes = append(context.Costumes, &controlv2.CostumeFact{
			CostumeId: fact.CostumeID, Grade: fact.Grade, MaxLevel: fact.MaxLevel,
			OverflowItemType: fact.OverflowItemType, OverflowItemId: fact.OverflowItemID, OverflowItemCount: fact.OverflowCount,
		})
	}
	for _, costume := range starter.Costumes {
		context.BaseCostumes = append(context.BaseCostumes, &controlv2.BaseCostumeFact{
			InventoryIndex: costume.InvenIndex, CostumeId: costume.ID, Level: costume.Level,
		})
	}
	context.StepUps = []*controlv2.StepUpFact{{GroupId: stepGroupID, OrderedGachaIds: append([]uint64(nil), stepGachaIDs...)}}
	return context, nil
}

func Repair(ctx context.Context, stateDir, toolPath string, validation *controlv2.ValidationContext) (RepairResult, error) {
	source, sourceHash, err := LoadSnapshot(stateDir)
	if err != nil {
		return RepairResult{}, err
	}
	if validation == nil || validation.GameDataVersion != source.GameDataVersion {
		return RepairResult{}, errors.New("statebridge: validation context version mismatch")
	}
	request := &controlv2.RepairRequest{BridgeApiVersion: MigrationBridgeAPIVersion, SourceSha256: sourceHash[:], Source: source, Context: validation}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	if err != nil {
		return RepairResult{}, fmt.Errorf("statebridge: encode repair request: %w", err)
	}
	responsePayload, err := runTool(ctx, toolPath, []string{"repair"}, payload)
	if err != nil {
		return RepairResult{}, err
	}
	var response controlv2.RepairResponse
	if err := proto.Unmarshal(responsePayload, &response); err != nil {
		return RepairResult{}, fmt.Errorf("statebridge: decode repair response: %w", err)
	}
	if response.BridgeApiVersion != MigrationBridgeAPIVersion || !bytes.Equal(response.SourceSha256, sourceHash[:]) {
		return RepairResult{}, errors.New("statebridge: repair response identity mismatch")
	}
	if err := validateViolations(response.Violations); err != nil {
		return RepairResult{}, err
	}
	result := RepairResult{SourceSHA256: sourceHash, Changed: response.Changed, Violations: response.Violations}
	if !response.Repaired {
		if response.Target != nil || !hasErrorViolation(response.Violations) {
			return RepairResult{}, errors.New("statebridge: invalid rejected repair response")
		}
		return result, nil
	}
	if response.Target == nil || hasErrorViolation(response.Violations) {
		return RepairResult{}, errors.New("statebridge: invalid accepted repair response")
	}
	if response.Target.FormatVersion != 2 || response.Target.ClientVersion != source.ClientVersion || response.Target.GameDataVersion != source.GameDataVersion {
		return RepairResult{}, errors.New("statebridge: repaired target metadata mismatch")
	}
	if !response.Changed {
		if err := verifyV2(source, response.Target); err != nil {
			return RepairResult{}, fmt.Errorf("statebridge: unchanged repair altered source semantics: %w", err)
		}
	}
	targetBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(response.Target)
	if err != nil {
		return RepairResult{}, err
	}
	result.Target = response.Target
	result.TargetSHA256 = sha256.Sum256(targetBytes)
	return result, nil
}
