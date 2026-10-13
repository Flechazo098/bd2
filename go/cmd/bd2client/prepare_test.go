package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExclusivePreparationStagesCannotBeCombined(t *testing.T) {
	err := prepareClient([]string{"--download", "--base"})
	if err == nil || !strings.Contains(err.Error(), "separate stages") {
		t.Fatalf("conflicting stages could modify the installation: %v", err)
	}
}

func TestPreparePlanDoesNotReadPackageOrWriteInstallation(t *testing.T) {
	for _, stage := range []string{"", "--download", "--base"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			packageDir := filepath.Join(root, "missing-package")
			args := []string{"--plan", "--package-dir", packageDir, "--manifest", filepath.Join(root, "missing-manifest.json")}
			if stage != "" {
				args = append(args, stage)
			}
			if err := prepareClient(args); err != nil {
				t.Fatal("planning required live package inputs", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("planning changed installation: %v, %v", entries, err)
			}
		})
	}
}

func TestPrepareHelpNeedsNoInstalledGame(t *testing.T) {
	if err := prepareClient([]string{"--help"}); err != nil {
		t.Fatal(err)
	}
}
