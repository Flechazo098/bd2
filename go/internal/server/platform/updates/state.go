package updates

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/i18n"
	"bd2server/internal/common/release"
)

// Files below DATA_DIR/updates. The running server owns state.json; the CLI
// only writes control.json and reads state.json back.
const (
	directoryName = "updates"
	stateFile     = "state.json"
	controlFile   = "control.json"
	pendingFile   = "pending.json"
	dockerFile    = "docker-request.json"
)

// Status is what the console, the CLI and state.json show.
type Status struct {
	CurrentVersion string          `json:"current_version"`
	Method         string          `json:"method"`
	CheckEnabled   bool            `json:"check_enabled"`
	Countdown      int             `json:"countdown_minutes"`
	ManifestURL    string          `json:"manifest_url"`
	LastCheck      time.Time       `json:"last_check,omitzero"`
	LastError      string          `json:"last_error,omitempty"`
	Available      *Available      `json:"available,omitempty"`
	Schedules      *ScheduleStatus `json:"schedules,omitempty"`
	Maintenance    *Maintenance    `json:"maintenance,omitempty"`
	LastResult     *Result         `json:"last_result,omitempty"`
	Control        *ControlAck     `json:"control,omitempty"`
	Notified       string          `json:"notified_version,omitempty"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type Available struct {
	GameDataVersion string               `json:"game_data_version,omitempty"`
	Version         string               `json:"version"`
	Date            string               `json:"date,omitempty"`
	Notes           i18n.Text            `json:"notes,omitempty"`
	PublishedAt     time.Time            `json:"published_at"`
	Commit          string               `json:"commit"`
	Image           string               `json:"image,omitempty"`
	Migration       release.Migration    `json:"migration"`
	Downloaded      bool                 `json:"downloaded"`
	DownloadError   string               `json:"download_error,omitempty"`
	Component       *release.Component   `json:"component"`
	ManifestURL     string               `json:"manifest_url"`
	Fingerprint     *release.Fingerprint `json:"-"`
}

// Maintenance phases.
const (
	PhasePreparing = "preparing"
	PhaseCountdown = "countdown"
	PhaseClosing   = "closing"
)

type Maintenance struct {
	Component   string    `json:"component,omitempty"`
	Version     string    `json:"version"`
	Phase       string    `json:"phase"`
	At          time.Time `json:"at,omitzero"`
	RequestedBy string    `json:"requested_by"`
	Delay       int       `json:"delay_minutes"`
}

// Result describes the last version switch, written by the process that
// finished it (or rolled it back).
type Result struct {
	From    string    `json:"from"`
	To      string    `json:"to"`
	Outcome string    `json:"outcome"`
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
}

// Switch outcomes.
const (
	OutcomeSucceeded     = "succeeded"
	OutcomeRolledBack    = "rolled_back"
	OutcomeNeedsOperator = "needs_operator"
	OutcomeAwaitingHost  = "awaiting_host"
	OutcomeHostTimedOut  = "host_timed_out"
	OutcomeSwitchFailed  = "switch_failed"
)

// Control is one CLI request.
type Control struct {
	Component           string    `json:"component,omitempty"`
	ID                  string    `json:"id"`
	Action              string    `json:"action"`
	DelayMinutes        int       `json:"delay_minutes"`
	ConfirmIrreversible bool      `json:"confirm_irreversible"`
	RequestedAt         time.Time `json:"requested_at"`
}

type ControlAck struct {
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// Control actions.
const (
	ActionCheck  = "check"
	ActionApply  = "apply"
	ActionCancel = "cancel"
)

func directory(dataDir string) string { return filepath.Join(dataDir, directoryName) }

func writeJSON(path string, value any) error {
	return commonconfig.Save(path, value, commonconfig.WriteOptions{DirectoryMode: 0o700})
}

func readJSON(path string, value any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, value); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// ReadStatus returns the status the running server last wrote.
func ReadStatus(dataDir string) (Status, error) {
	var status Status
	err := readJSON(filepath.Join(directory(dataDir), stateFile), &status)
	if errors.Is(err, os.ErrNotExist) {
		return status, errors.New("no update status yet; start the server at least once with this data directory")
	}
	mergeHostResult(dataDir, &status)
	mergeScheduleResult(dataDir, &status)
	return status, err
}

func mergeHostResult(dataDir string, status *Status) {
	var result hostResult
	if err := readJSON(filepath.Join(directory(dataDir), "docker-result.json"), &result); err != nil {
		return
	}
	if result.To != status.CurrentVersion && result.From != status.CurrentVersion {
		return
	}
	if status.LastResult == nil || result.At.After(status.LastResult.At) {
		status.LastResult = &result.Result
	}
}

// SubmitControl asks the running server to act and waits for its answer.
func SubmitControl(dataDir string, control Control, wait time.Duration) (ControlAck, error) {
	path := filepath.Join(directory(dataDir), controlFile)
	if err := writeJSON(path, control); err != nil {
		return ControlAck{}, err
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		status, err := ReadStatus(dataDir)
		if err == nil && status.Control != nil && status.Control.ID == control.ID {
			return *status.Control, nil
		}
	}
	_ = os.Remove(path)
	return ControlAck{}, errors.New("the server did not answer; check that it is running with this data directory")
}
