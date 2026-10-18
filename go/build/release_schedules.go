package main

import (
	"bd2server/internal/common/util"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"

	releasepkg "bd2server/internal/common/release"
	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/domain/events/calendar"
)

func (t task) publishSchedules(opts publishOptions) error {
	commit, err := t.releaseCommit(opts.allowDirty)
	if err != nil {
		return err
	}
	output := opts.output
	if output == "" {
		output = filepath.Join(t.root, ".build", "release-site")
	}
	versions, err := versionconfig.Load(filepath.Join(t.root, "versions.json"))
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(output, "manifest.json"))
	if err != nil {
		return fmt.Errorf("load existing release materials before replacing schedules: %w", err)
	}
	var manifest releasepkg.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	if manifest.Channel != releasepkg.BuiltinChannel().Name {
		return fmt.Errorf("release materials belong to a different channel")
	}
	schedules, err := t.stageSchedules(output, versions)
	if err != nil {
		return err
	}
	if manifest.Schedules != nil {
		schedules.Notes = manifest.Schedules.Notes
	}
	manifest.Schedules, manifest.Commit, manifest.PublishedAt = schedules, commit, time.Now().UTC().Truncate(time.Second)
	if err := manifest.Validate(); err != nil {
		return err
	}
	raw, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, "manifest.json"), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Println("Unsigned release materials:", output)
	fmt.Println("Schedule content ID:", schedules.ID)
	fmt.Println("Sign independently with tools/release/sign.py; publish files before the signed manifest.")
	return nil
}

func (t task) stageSchedules(output string, versions versionconfig.Config) (*releasepkg.Schedules, error) {
	source := filepath.Join(t.root, "schedules")
	set, err := calendar.LoadDirectory(source, versions.GameVersion, versions.GameDataVersion)
	if err != nil {
		return nil, err
	}
	if set.RegularService == nil || set.MonsterHunt == nil || len(set.MonsterHunt.Seasons) == 0 {
		return nil, fmt.Errorf("schedules require regular content and monster hunt seasons")
	}
	schedules := &releasepkg.Schedules{GameVersion: versions.GameVersion, GameDataVersion: versions.GameDataVersion}
	entries, err := os.ReadDir(source)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		file := filepath.Join(source, entry.Name())
		sum, err := util.SHA256File(file)
		if err != nil {
			return nil, err
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		relative := path.Join("files", sum, entry.Name())
		schedules.Files = append(schedules.Files, releasepkg.File{Name: entry.Name(), Size: info.Size(), SHA256: sum, URL: "../" + relative})
	}
	schedules.ID = schedules.ContentID()
	for _, f := range schedules.Files {
		target := filepath.Join(output, "files", f.SHA256, f.Name)
		if err := copyFile(filepath.Join(source, f.Name), target); err != nil {
			return nil, err
		}
		if !releasepkg.Matches(target, f) {
			return nil, fmt.Errorf("schedule %s changed while preparing materials", f.Name)
		}
	}
	return schedules, nil
}
