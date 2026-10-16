package updates

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bd2server/internal/common/release"
	"bd2server/internal/server/domain/events/calendar"
)

const (
	ComponentSchedules    = "schedules"
	ComponentServer       = "server"
	scheduleSelectionFile = "schedules-active.json"
	schedulePendingFile   = "schedules-pending.json"
	scheduleResultFile    = "schedules-result.json"
)

type ScheduleStatus struct {
	CurrentID       string             `json:"current_id"`
	GameVersion     string             `json:"game_version"`
	GameDataVersion string             `json:"game_data_version"`
	Available       *ScheduleAvailable `json:"available,omitempty"`
	LastResult      *Result            `json:"last_result,omitempty"`
}

type ScheduleAvailable struct {
	Component      release.Schedules `json:"component"`
	PublishedAt    time.Time         `json:"published_at"`
	Downloaded     bool              `json:"downloaded"`
	DownloadError  string            `json:"download_error,omitempty"`
	Incompatible   string            `json:"incompatible,omitempty"`
	ManifestURL    string            `json:"-"`
	SignedManifest []byte            `json:"-"`
}

type scheduleSelection struct {
	ID              string `json:"id"`
	GameVersion     string `json:"game_version"`
	GameDataVersion string `json:"game_data_version"`
}

type SchedulePlan struct {
	ID              string
	GameVersion     string
	GameDataVersion string
}

type schedulePending struct {
	From     string             `json:"from"`
	To       string             `json:"to"`
	Previous *scheduleSelection `json:"previous,omitempty"`
	Attempts int                `json:"attempts"`
}

func scheduleRoot(dataDir, id string) (string, error) {
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		return "", errors.New("invalid schedules content ID")
	}
	return filepath.Join(directory(dataDir), "schedules", id), nil
}

func scheduleCompatible(s release.Schedules, game, gameData string) error {
	if s.GameVersion != game || s.GameDataVersion != gameData {
		return fmt.Errorf("schedules require game %s / GameData %s; running %s / %s", s.GameVersion, s.GameDataVersion, game, gameData)
	}
	return nil
}

func (s *Service) updateSchedulesLocked(manifest release.Manifest, signed []byte, address string) *ScheduleAvailable {
	candidate := manifest.Schedules
	if candidate == nil || candidate.ID == s.options.CurrentSchedules {
		s.status.Schedules.Available = nil
		return nil
	}
	previous := s.status.Schedules.Available
	a := &ScheduleAvailable{Component: *candidate, PublishedAt: manifest.PublishedAt, ManifestURL: address, SignedManifest: append([]byte(nil), signed...)}
	if err := scheduleCompatible(*candidate, s.options.GameVersion, s.options.GameDataVersion); err != nil {
		a.Incompatible = err.Error()
	}
	if previous != nil && previous.Component.ID == candidate.ID {
		a.Downloaded, a.DownloadError = previous.Downloaded, previous.DownloadError
	} else {
		slog.Info("schedule update available", "current", s.options.CurrentSchedules, "available", candidate.ID, "compatible", a.Incompatible == "", "apply", "bd2server update apply --component schedules")
	}
	s.status.Schedules.Available = a
	return a
}

func (s *Service) preDownloadSchedules(ctx context.Context, a *ScheduleAvailable) {
	if a == nil || a.Incompatible != "" || !s.options.Config.Download || s.options.Config.Method == MethodNone {
		return
	}
	if _, err := s.prepareSchedules(ctx, a); err != nil {
		slog.Warn("schedule pre-download failed", "id", a.Component.ID, "error", err)
	}
}

func verifyScheduleFiles(root string, s release.Schedules) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) != len(s.Files) {
		return errors.New("schedules directory differs from signed file set")
	}
	for _, f := range s.Files {
		path := filepath.Join(root, f.Name)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != f.Size {
			return fmt.Errorf("schedule %s is not a regular file of the signed size", f.Name)
		}
		sum, err := release.HashFile(path)
		if err != nil {
			return err
		}
		if sum != f.SHA256 {
			return fmt.Errorf("schedule %s differs from signed digest", f.Name)
		}
	}
	set, err := calendar.LoadDirectory(root, s.GameVersion, s.GameDataVersion)
	if err != nil {
		return err
	}
	if set.RegularService == nil || set.MonsterHunt == nil || len(set.MonsterHunt.Seasons) == 0 {
		return errors.New("schedules require regular content and monster hunt seasons")
	}
	return nil
}

func loadScheduleSet(dataDir, id, game, gameData string) (string, error) {
	root, err := scheduleRoot(dataDir, id)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("schedules root must be a directory")
	}
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return "", err
	}
	manifest, err := release.Verify(raw, release.BuiltinChannel().TrustedKeys)
	if err != nil {
		return "", err
	}
	if manifest.Channel != release.BuiltinChannel().Name || manifest.Schedules == nil || manifest.Schedules.ID != id {
		return "", errors.New("cached schedules do not match signed manifest")
	}
	if err := scheduleCompatible(*manifest.Schedules, game, gameData); err != nil {
		return "", err
	}
	files := filepath.Join(root, "files")
	info, err = os.Lstat(files)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("schedules files must be a directory")
	}
	return files, verifyScheduleFiles(files, *manifest.Schedules)
}

// ScheduleDirectory selects an installed signed set for this exact data version.
// A program/GameData upgrade uses its bundled schedules until a compatible set
// is explicitly applied; it never tries to reinterpret an older set.
func ScheduleDirectory(dataDir, bundled, game, gameData string) (string, string, error) {
	var selected scheduleSelection
	if err := readJSON(filepath.Join(directory(dataDir), scheduleSelectionFile), &selected); errors.Is(err, os.ErrNotExist) {
		return bundled, "", nil
	} else if err != nil {
		return "", "", err
	}
	if selected.GameVersion != game || selected.GameDataVersion != gameData {
		return bundled, "", nil
	}
	path, err := loadScheduleSet(dataDir, selected.ID, game, gameData)
	return path, selected.ID, err
}

func (s *Service) prepareSchedules(ctx context.Context, a *ScheduleAvailable) (string, error) {
	if a == nil {
		return "", errors.New("no schedule update available")
	}
	s.stageMu.Lock()
	defer s.stageMu.Unlock()
	root, err := s.stageSchedules(ctx, a)
	s.mu.Lock()
	if current := s.status.Schedules.Available; current != nil && current.Component.ID == a.Component.ID {
		current.Downloaded = err == nil
		current.DownloadError = ""
		if err != nil {
			current.DownloadError = err.Error()
		}
		s.persistLocked()
	}
	s.mu.Unlock()
	return root, err
}

func (s *Service) stageSchedules(ctx context.Context, a *ScheduleAvailable) (string, error) {
	if a == nil {
		return "", errors.New("no schedule update available")
	}
	manifest, err := release.Verify(a.SignedManifest, s.keys)
	if err != nil {
		return "", err
	}
	if manifest.Schedules == nil || manifest.Schedules.ID != a.Component.ID || manifest.Channel != release.BuiltinChannel().Name {
		return "", errors.New("schedule candidate does not match signed manifest")
	}
	candidate := *manifest.Schedules
	if err := scheduleCompatible(candidate, s.options.GameVersion, s.options.GameDataVersion); err != nil {
		return "", err
	}
	root, err := scheduleRoot(s.options.DataDir, candidate.ID)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(root); err == nil {
		files, err := loadScheduleSet(s.options.DataDir, candidate.ID, s.options.GameVersion, s.options.GameDataVersion)
		if err == nil && s.options.ValidateSchedules != nil {
			err = s.options.ValidateSchedules(files)
		}
		return files, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		return "", err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(root), ".staging-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	files := filepath.Join(temporary, "files")
	for _, file := range candidate.Files {
		if _, err := release.Download(ctx, s.options.Client, a.ManifestURL, file, files, nil); err != nil {
			return "", err
		}
	}
	if err := verifyScheduleFiles(files, candidate); err != nil {
		return "", err
	}
	if s.options.ValidateSchedules != nil {
		if err := s.options.ValidateSchedules(files); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(temporary, "manifest.json"), a.SignedManifest, 0o600); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(temporary, root); err != nil {
		return "", err
	}
	return filepath.Join(root, "files"), nil
}

func (p *Plan) switchSchedules(now time.Time) error {
	s := p.Schedules
	if _, err := loadScheduleSet(p.DataDir, s.ID, s.GameVersion, s.GameDataVersion); err != nil {
		return err
	}
	var previous scheduleSelection
	err := readJSON(filepath.Join(directory(p.DataDir), scheduleSelectionFile), &previous)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	record := schedulePending{From: p.From, To: s.ID}
	if err == nil {
		record.Previous = &previous
	}
	if err := writeJSON(filepath.Join(directory(p.DataDir), schedulePendingFile), record); err != nil {
		return err
	}
	selected := scheduleSelection{ID: s.ID, GameVersion: s.GameVersion, GameDataVersion: s.GameDataVersion}
	if err := writeJSON(filepath.Join(directory(p.DataDir), scheduleSelectionFile), selected); err != nil {
		_ = os.Remove(filepath.Join(directory(p.DataDir), schedulePendingFile))
		return err
	}
	recordScheduleResult(p.DataDir, Result{From: p.From, To: p.To, Outcome: "restarting", At: now.UTC()})
	return nil
}

func scheduleStartup(dataDir string) (*Confirmation, int, error) {
	path := filepath.Join(directory(dataDir), schedulePendingFile)
	var record schedulePending
	if err := readJSON(path, &record); errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	} else if err != nil {
		return nil, ExitNeedsOperator, err
	}
	var selected scheduleSelection
	if err := readJSON(filepath.Join(directory(dataDir), scheduleSelectionFile), &selected); err != nil {
		if errors.Is(err, os.ErrNotExist) && record.Previous == nil {
			return nil, 0, os.Remove(path)
		}
		return nil, ExitNeedsOperator, err
	}
	if selected.ID != record.To {
		if record.Previous != nil && selected == *record.Previous {
			return nil, 0, os.Remove(path)
		}
		return nil, ExitNeedsOperator, errors.New("pending schedules differ from active selection")
	}
	record.Attempts++
	if record.Attempts > maxStartAttempts {
		code, err := rollbackSchedules(dataDir, record, "repeated startup failures")
		return nil, code, err
	}
	if err := writeJSON(path, record); err != nil {
		return nil, ExitNeedsOperator, err
	}
	return &Confirmation{dataDir: dataDir, schedules: &record}, 0, nil
}

func rollbackSchedules(dataDir string, record schedulePending, reason string) (int, error) {
	path := filepath.Join(directory(dataDir), scheduleSelectionFile)
	var err error
	if record.Previous == nil {
		err = os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else {
		err = writeJSON(path, record.Previous)
	}
	if err != nil {
		return ExitNeedsOperator, fmt.Errorf("restore previous schedules: %w", err)
	}
	if err := os.Remove(filepath.Join(directory(dataDir), schedulePendingFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ExitNeedsOperator, err
	}
	message := "previous schedule selection restored: " + reason
	recordScheduleResult(dataDir, Result{From: record.From, To: record.To, Outcome: OutcomeRolledBack, Message: message, At: time.Now().UTC()})
	return ExitRestart, errors.New(message)
}

func recordScheduleResult(dataDir string, result Result) {
	if err := writeJSON(filepath.Join(directory(dataDir), scheduleResultFile), result); err != nil {
		slog.Warn("schedule result could not be saved", "error", err)
	}
}

func mergeScheduleResult(dataDir string, status *Status) {
	var result Result
	if err := readJSON(filepath.Join(directory(dataDir), scheduleResultFile), &result); err != nil {
		return
	}
	if status.Schedules == nil {
		status.Schedules = &ScheduleStatus{}
	}
	if previous := status.Schedules.LastResult; previous == nil || result.At.After(previous.At) {
		status.Schedules.LastResult = &result
	}
}
