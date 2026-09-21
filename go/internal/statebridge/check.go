package statebridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	controlv1 "bd2server/gen/state/control/v1"
	"google.golang.org/protobuf/proto"
)

const (
	BridgeAPIVersion = 1
	MaxFrameBytes    = 16 << 20
)

type CheckResult struct {
	SourceSHA256 [32]byte
	Violations   []*controlv1.Violation
}

type boundedOutput struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		b.overflow = true
		return 0, errors.New("statebridge: tool output exceeded limit")
	}
	return b.Buffer.Write(data)
}

// Check loads the complete state snapshot and invokes the pure Haskell
// validator. The tool receives no state path and therefore cannot modify it.
func Check(ctx context.Context, stateDir, toolPath string) (CheckResult, error) {
	snapshot, sourceHash, err := LoadSnapshot(stateDir)
	if err != nil {
		return CheckResult{}, err
	}
	request := &controlv1.CheckRequest{BridgeApiVersion: BridgeAPIVersion, SourceSha256: sourceHash[:], Snapshot: snapshot}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	if err != nil {
		return CheckResult{}, fmt.Errorf("statebridge: encode request: %w", err)
	}
	responsePayload, err := runTool(ctx, toolPath, nil, payload)
	if err != nil {
		return CheckResult{}, err
	}
	var response controlv1.CheckResponse
	if err := proto.Unmarshal(responsePayload, &response); err != nil {
		return CheckResult{}, fmt.Errorf("statebridge: decode response: %w", err)
	}
	if response.BridgeApiVersion != BridgeAPIVersion {
		return CheckResult{}, fmt.Errorf("statebridge: validator API version %d", response.BridgeApiVersion)
	}
	if !bytes.Equal(response.SourceSha256, sourceHash[:]) {
		return CheckResult{}, errors.New("statebridge: validator returned the wrong source hash")
	}
	if err := validateViolations(response.Violations); err != nil {
		return CheckResult{}, err
	}
	return CheckResult{SourceSHA256: sourceHash, Violations: response.Violations}, nil
}

func runTool(ctx context.Context, toolPath string, args []string, payload []byte) ([]byte, error) {
	frame, err := encodeFrame(payload)
	if err != nil {
		return nil, err
	}
	if toolPath == "" {
		return nil, errors.New("statebridge: empty validator tool path")
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	command := exec.CommandContext(ctx, toolPath, args...)
	command.Stdin = bytes.NewReader(frame)
	stdout := &boundedOutput{limit: MaxFrameBytes + binary.MaxVarintLen64}
	stderr := &boundedOutput{limit: 64 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	runErr := command.Run()
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("statebridge: validator output exceeded limit")
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("statebridge: validator timeout: %w", ctx.Err())
		}
		return nil, fmt.Errorf("statebridge: validator failed: %w: %s", runErr, stderr.String())
	}
	responsePayload, err := decodeFrame(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("statebridge: decode response frame: %w", err)
	}
	return responsePayload, nil
}

func validateViolations(violations []*controlv1.Violation) error {
	for _, problem := range violations {
		if problem == nil || problem.Code == "" || (problem.Severity != controlv1.Severity_SEVERITY_WARNING && problem.Severity != controlv1.Severity_SEVERITY_ERROR) {
			return errors.New("statebridge: validator returned an invalid violation")
		}
	}
	return nil
}

func encodeFrame(payload []byte) ([]byte, error) {
	if len(payload) > MaxFrameBytes {
		return nil, fmt.Errorf("statebridge: frame length %d exceeds %d", len(payload), MaxFrameBytes)
	}
	frame := binary.AppendUvarint(nil, uint64(len(payload)))
	return append(frame, payload...), nil
}

func decodeFrame(frame []byte) ([]byte, error) {
	reader := bufio.NewReader(bytes.NewReader(frame))
	size, err := binary.ReadUvarint(reader)
	if err != nil {
		return nil, err
	}
	if size > MaxFrameBytes {
		return nil, fmt.Errorf("frame length %d exceeds %d", size, MaxFrameBytes)
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	if extra, err := reader.ReadByte(); err == nil {
		return nil, fmt.Errorf("trailing response byte 0x%02x", extra)
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	return payload, nil
}
