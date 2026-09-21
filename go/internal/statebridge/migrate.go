package statebridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"

	controlv1 "bd2server/gen/state/control/v1"
	controlv2 "bd2server/gen/state/control/v2"
	statev1 "bd2server/gen/state/v1"
	statev2 "bd2server/gen/state/v2"
	"google.golang.org/protobuf/proto"
)

const MigrationBridgeAPIVersion = 2

type MigrationResult struct {
	SourceSHA256 [32]byte
	TargetSHA256 [32]byte
	Target       *statev2.Snapshot
	Violations   []*controlv1.Violation
}

// MigrateV1ToV2 performs an in-memory, read-only migration. The Haskell tool
// receives only protobuf bytes and cannot discover or write the state path.
func MigrateV1ToV2(ctx context.Context, stateDir, toolPath string) (MigrationResult, error) {
	source, sourceHash, err := LoadSnapshot(stateDir)
	if err != nil {
		return MigrationResult{}, err
	}
	request := &controlv2.MigrateV1ToV2Request{
		BridgeApiVersion: MigrationBridgeAPIVersion,
		SourceSha256:     sourceHash[:],
		Source:           source,
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("statebridge: encode migration request: %w", err)
	}
	responsePayload, err := runTool(ctx, toolPath, []string{"migrate-v1-v2"}, payload)
	if err != nil {
		return MigrationResult{}, err
	}
	var response controlv2.MigrateV1ToV2Response
	if err := proto.Unmarshal(responsePayload, &response); err != nil {
		return MigrationResult{}, fmt.Errorf("statebridge: decode migration response: %w", err)
	}
	if response.BridgeApiVersion != MigrationBridgeAPIVersion {
		return MigrationResult{}, fmt.Errorf("statebridge: migrator API version %d", response.BridgeApiVersion)
	}
	if !bytes.Equal(response.SourceSha256, sourceHash[:]) {
		return MigrationResult{}, errors.New("statebridge: migrator returned the wrong source hash")
	}
	if err := validateViolations(response.Violations); err != nil {
		return MigrationResult{}, err
	}
	result := MigrationResult{SourceSHA256: sourceHash, Violations: response.Violations}
	if !response.Migrated {
		if response.Target != nil {
			return MigrationResult{}, errors.New("statebridge: rejected migration returned a target")
		}
		if !hasErrorViolation(response.Violations) {
			return MigrationResult{}, errors.New("statebridge: rejected migration omitted an error violation")
		}
		return result, nil
	}
	if response.Target == nil {
		return MigrationResult{}, errors.New("statebridge: accepted migration omitted target")
	}
	if hasErrorViolation(response.Violations) {
		return MigrationResult{}, errors.New("statebridge: accepted migration returned errors")
	}
	if err := verifyV2(source, response.Target); err != nil {
		return MigrationResult{}, err
	}
	targetBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(response.Target)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("statebridge: encode migrated target: %w", err)
	}
	result.Target = response.Target
	result.TargetSHA256 = sha256.Sum256(targetBytes)
	return result, nil
}

func hasErrorViolation(violations []*controlv1.Violation) bool {
	for _, violation := range violations {
		if violation.Severity == controlv1.Severity_SEVERITY_ERROR {
			return true
		}
	}
	return false
}

func verifyV2(source *statev1.Snapshot, target *statev2.Snapshot) error {
	if target.FormatVersion != 2 || target.ClientVersion != source.ClientVersion || target.GameDataVersion != source.GameDataVersion {
		return errors.New("statebridge: migrated target metadata mismatch")
	}
	if target.Roster == nil || target.Collection == nil || source.Characters == nil || source.Collection == nil {
		return errors.New("statebridge: migrated target omitted roster or collection")
	}
	unchanged := [][2]proto.Message{
		{source.Progress, target.Progress}, {source.Deck, target.Deck}, {source.Inventory, target.Inventory},
		{source.Equipment, target.Equipment}, {source.Wallet, target.Wallet}, {source.Mail, target.Mail},
		{source.Missions, target.Missions},
	}
	for _, pair := range unchanged {
		if !proto.Equal(pair[0], pair[1]) {
			return errors.New("statebridge: migration changed an unchanged V1 domain")
		}
	}
	baseCount := len(source.Characters.Characters)
	acquiredCount := len(source.Collection.Characters)
	if len(target.Roster.Characters) != baseCount+acquiredCount {
		return errors.New("statebridge: migration changed character count")
	}
	for i, record := range target.Roster.Characters {
		if record == nil || record.Character == nil {
			return errors.New("statebridge: migration emitted an empty character record")
		}
		if i < baseCount {
			if record.Origin != statev2.CharacterOrigin_CHARACTER_ORIGIN_BASE || !proto.Equal(record.Character, source.Characters.Characters[i]) {
				return errors.New("statebridge: migration changed a base character")
			}
		} else if record.Origin != statev2.CharacterOrigin_CHARACTER_ORIGIN_ACQUIRED || !proto.Equal(record.Character, source.Collection.Characters[i-baseCount]) {
			return errors.New("statebridge: migration changed an acquired character")
		}
	}
	if target.Roster.NextAcquiredCharacterIndex != source.Collection.NextCharacterIndex ||
		target.Roster.NextCostumeIndex != source.Collection.NextCostumeIndex ||
		len(target.Roster.Costumes) != len(source.Collection.Costumes) {
		return errors.New("statebridge: migration changed roster allocation state")
	}
	for i := range target.Roster.Costumes {
		if !proto.Equal(target.Roster.Costumes[i], source.Collection.Costumes[i]) {
			return errors.New("statebridge: migration changed a costume")
		}
	}
	if !collectionLedgerEqual(source.Collection, target.Collection) {
		return errors.New("statebridge: migration changed collection ledger state")
	}
	return nil
}

func collectionLedgerEqual(source *statev1.Collection, target *statev2.CollectionLedger) bool {
	return equalU64(source.LatestPreview, target.LatestPreview) && source.PreviewEventIndex == target.PreviewEventIndex &&
		source.PreviewLocked == target.PreviewLocked && protoSlicesEqual(source.BaseCostumeLevels, target.BaseCostumeLevels) &&
		protoSlicesEqual(source.GachaSelections, target.GachaSelections) && protoSlicesEqual(source.StepUpProgress, target.StepUpProgress) &&
		protoSlicesEqual(source.GachaUsers, target.GachaUsers) && protoSlicesEqual(source.GachaFixed, target.GachaFixed) &&
		equalStrings(source.GachaApplied, target.GachaApplied) && protoSlicesEqual(source.GachaPointExchanges, target.GachaPointExchanges) &&
		source.GachaCountCorrected == target.GachaCountCorrected && protoSlicesEqual(source.Grants, target.Grants)
}

func protoSlicesEqual[T proto.Message](left, right []T) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !proto.Equal(left[i], right[i]) {
			return false
		}
	}
	return true
}

func equalU64(left, right []uint64) bool {
	return slices.Equal(left, right)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
