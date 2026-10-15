package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"bd2server/internal/common/i18n"
	"bd2server/internal/server/platform/updates"
)

func runUpdate(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: bd2server update status|check|apply|cancel [--data-dir DIR]")
	}
	fs := flag.NewFlagSet("update "+args[0], flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "server data directory (defaults beside the executable)")
	asJSON := fs.Bool("json", false, "print the status as JSON")
	delay := fs.Duration("in", -1, "apply: start maintenance after this countdown, e.g. 10m (default from updates.json)")
	now := fs.Bool("now", false, "apply: start maintenance as soon as the release is ready")
	confirm := fs.Bool("confirm-irreversible", false, "apply: accept a release whose save migration cannot be rolled back")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *dataDir == "" {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		*dataDir = filepath.Join(filepath.Dir(executable), "data")
	}
	directory, err := filepath.Abs(*dataDir)
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		status, err := updates.ReadStatus(directory)
		if err != nil {
			return err
		}
		if *asJSON {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(status)
		}
		printStatus(status)
		return nil
	case "check", "apply", "cancel":
		control := updates.Control{ID: requestID(), Action: args[0], ConfirmIrreversible: *confirm, RequestedAt: time.Now().UTC()}
		if args[0] == updates.ActionApply {
			config, err := updates.Document.Load(updates.Document.Path(filepath.Join(directory, "config")))
			if err != nil {
				return err
			}
			control.DelayMinutes = config.CountdownMinutes
			if *delay >= 0 {
				control.DelayMinutes = int(delay.Round(time.Minute) / time.Minute)
			}
			if *now {
				control.DelayMinutes = 0
			}
		}
		ack, err := updates.SubmitControl(directory, control, 90*time.Second)
		if err != nil {
			return err
		}
		fmt.Println(ack.Message)
		if !ack.OK {
			return errors.New("the server refused the request")
		}
		return nil
	}
	return fmt.Errorf("unknown update command %q", args[0])
}

func requestID() string {
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

func printStatus(s updates.Status) {
	fmt.Printf("Current     %s (install method %s)\n", s.CurrentVersion, s.Method)
	if !s.LastCheck.IsZero() {
		fmt.Printf("Checked     %s\n", s.LastCheck.Local().Format(time.DateTime))
	}
	if s.LastError != "" {
		fmt.Printf("Check error %s\n", s.LastError)
	}
	if a := s.Available; a != nil {
		state := "not downloaded"
		if a.Downloaded {
			state = "downloaded and verified"
		} else if a.DownloadError != "" {
			state = "download failed: " + a.DownloadError
		}
		fmt.Printf("Available   %s (%s, migration %s)\n", a.Version, state, a.Migration.Kind)
		if note := a.Migration.Note.In(i18n.Fallback); note != "" {
			fmt.Printf("Migration   %s\n", note)
		}
		if notes := a.Notes.In(i18n.Fallback); notes != "" {
			fmt.Printf("\n%s\n\n", notes)
		}
	} else if s.LastError == "" {
		fmt.Println("Available   none; up to date")
	}
	if m := s.Maintenance; m != nil {
		at := "countdown starts once the release is prepared"
		if !m.At.IsZero() {
			at = m.At.Local().Format(time.DateTime)
		}
		fmt.Printf("Maintenance %s -> %s (%s, %s)\n", m.Phase, m.Version, at, m.RequestedBy)
	}
	if r := s.LastResult; r != nil {
		fmt.Printf("Last switch %s -> %s: %s %s\n", r.From, r.To, r.Outcome, r.Message)
	}
}
