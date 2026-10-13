package sourcetools

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type repeatedFlag []string

func (s *repeatedFlag) String() string     { return strings.Join(*s, ",") }
func (s *repeatedFlag) Set(v string) error { *s = append(*s, v); return nil }

type diffReport struct {
	Tool           string         `json:"tool"`
	Output         string         `json:"output"`
	Schema         int            `json:"schema"`
	OldRoot        string         `json:"old_root"`
	NewRoot        string         `json:"new_root"`
	Workers        int            `json:"workers"`
	Strict         bool           `json:"strict"`
	ElapsedSeconds float64        `json:"elapsed_seconds"`
	Counts         map[string]int `json:"counts"`
	Warnings       []string       `json:"warnings"`
	RenamedPaths   int            `json:"renamed_paths"`
	Files          []fileResult   `json:"files"`
}

const diffTool = "bd2.cleandiff_client_source"

// RunDiff parses source comparison arguments and writes a six-file report bundle.
func RunDiff(args []string, out io.Writer) error {
	f := flag.NewFlagSet("diff", flag.ContinueOnError)
	f.SetOutput(out)
	aFlag := f.String("a", "", "old C# source tree")
	bFlag := f.String("b", "", "new C# source tree")
	outputFlag := f.String("out", "cleandiff-report.txt", "summary path; also writes .html/.json/.diff/.noise.diff/.review.diff")
	workersFlag := f.Int("workers", 0, "workers, 0=automatic; 1=serial")
	contextFlag := f.Int("context", 3, "diff context lines")
	strictFlag := f.Bool("strict", false, "include local names, parentheses, inferred generics and generated numbering in main differences")
	var includes, excludes repeatedFlag
	f.Var(&includes, "include", "logical relative path glob, repeatable (e.g. Proto/Net/*)")
	f.Var(&excludes, "exclude", "exclude path glob, repeatable")
	if e := f.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if f.NArg() != 0 {
		return errors.New("diff does not accept positional arguments")
	}
	if *aFlag == "" || *bFlag == "" {
		return errors.New("--a and --b are required")
	}
	if *workersFlag < 0 || *contextFlag < 0 {
		return errors.New("--workers and --context must not be negative")
	}
	if !strings.EqualFold(filepath.Ext(*outputFlag), ".txt") {
		return errors.New("--out must end in .txt to avoid overlapping report files")
	}
	a, e := sourceRoot(*aFlag)
	if e != nil {
		return e
	}
	b, e := sourceRoot(*bFlag)
	if e != nil {
		return e
	}
	output, e := canonicalPath(*outputFlag)
	if e != nil {
		return e
	}
	if within(output, a) || within(output, b) {
		return errors.New("reports must be outside both input source directories")
	}
	for _, path := range reportPaths(output) {
		resolved, e := canonicalPath(path)
		if e != nil {
			return e
		}
		if resolved != path {
			return fmt.Errorf("report path resolves through a symbolic link: %s", path)
		}
		if within(resolved, a) || within(resolved, b) {
			return errors.New("reports must be outside both input source directories")
		}
	}
	if e = checkReportOutput(output); e != nil {
		return e
	}
	start := time.Now()
	ia, e := inventory(a, includes, excludes)
	if e != nil {
		return e
	}
	ib, e := inventory(b, includes, excludes)
	if e != nil {
		return e
	}
	tasks, symbolsA, symbolsB, warnings := pairTrees(a, b, ia, ib)
	workers := *workersFlag
	if workers == 0 {
		workers = min(8, runtime.NumCPU())
	}
	if _, e = fmt.Fprintf(out, "旧版 %d / 新版 %d C# 文件；%d workers\n", len(ia), len(ib), workers); e != nil {
		return e
	}
	results := make([]fileResult, len(tasks))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(workers, max(1, len(tasks))); w++ {
		wg.Go(func() {
			for i := range jobs {
				results[i] = compareFile(a, b, tasks[i], *contextFlag, *strictFlag, symbolsA, symbolsB)
			}
		})
	}
	for i := range tasks {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	counts := map[string]int{}
	for _, status := range []string{"modified", "added", "removed", "review", "noise", "unchanged", "error"} {
		counts[status] = 0
	}
	renamed := 0
	for _, r := range results {
		counts[r.Status]++
		if r.OldPath != nil && r.NewPath != nil && *r.OldPath != *r.NewPath {
			renamed++
		}
	}
	report := diffReport{Tool: diffTool, Output: output, Schema: 1, OldRoot: a, NewRoot: b, Workers: workers, Strict: *strictFlag, ElapsedSeconds: time.Since(start).Seconds(), Counts: counts, Warnings: warnings, RenamedPaths: renamed, Files: results}
	if e = writeReports(output, report); e != nil {
		return e
	}
	countJSON, _ := json.Marshal(counts)
	if _, e = fmt.Fprintf(out, "%s\n匹配混淆后缀路径 %d；比较 %.2fs；总计 %.2fs\n摘要: %s\n可视化: %s\n", countJSON, renamed, report.ElapsedSeconds, time.Since(start).Seconds(), output, strings.TrimSuffix(output, filepath.Ext(output))+".html"); e != nil {
		return e
	}
	if counts["error"] > 0 {
		return fmt.Errorf("source comparison reported %d file errors; see %s", counts["error"], output)
	}
	return nil
}
func reportPaths(output string) []string {
	stem := strings.TrimSuffix(output, filepath.Ext(output))
	return []string{output, stem + ".json", stem + ".html", stem + ".diff", stem + ".noise.diff", stem + ".review.diff"}
}
func checkReportOutput(output string) error {
	paths := reportPaths(output)
	managed := false
	b, e := os.ReadFile(paths[1])
	if e == nil {
		var owner struct {
			Tool   string `json:"tool"`
			Output string `json:"output"`
		}
		managed = json.Unmarshal(b, &owner) == nil && owner.Tool == diffTool && owner.Output == output
	} else if !os.IsNotExist(e) {
		return e
	}
	for _, p := range paths {
		stat, e := os.Lstat(p)
		if e == nil {
			if !managed || !stat.Mode().IsRegular() {
				return fmt.Errorf("refusing to overwrite non-managed report: %s", p)
			}
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	return nil
}
func writeReports(output string, report diffReport) (retErr error) {
	if e := checkReportOutput(output); e != nil {
		return e
	}
	if e := os.MkdirAll(filepath.Dir(output), 0755); e != nil {
		return e
	}
	stage, e := os.MkdirTemp(filepath.Dir(output), ".bd2-diff-stage-")
	if e != nil {
		return e
	}
	cleanup := true
	defer func() {
		if cleanup {
			retErr = errors.Join(retErr, os.RemoveAll(stage))
		}
	}()
	paths := reportPaths(output)
	summary, patch, noise, review := strings.Builder{}, strings.Builder{}, strings.Builder{}, strings.Builder{}
	fmt.Fprintf(&summary, "# %s\n# → %s\n\n", report.OldRoot, report.NewRoot)
	counts, _ := json.Marshal(report.Counts)
	fmt.Fprintf(&summary, "%s\n耗时 %.2fs / %d workers\n行号为归一化成员行号；疑似噪声不等于已证明语义相同。\n\n", counts, report.ElapsedSeconds, report.Workers)
	for _, w := range report.Warnings {
		fmt.Fprintf(&summary, "警告: %s\n", w)
	}
	for _, r := range report.Files {
		if r.Status == "unchanged" {
			continue
		}
		fmt.Fprintf(&summary, "[%s] %s\n", r.Status, r.Path)
		if r.Error != "" {
			fmt.Fprintf(&summary, "  %s\n", r.Error)
		}
		for i, group := range [][]diffEntry{r.Members, r.Noise, r.Review} {
			dest := &patch
			switch i {
			case 1:
				dest = &noise
			case 2:
				dest = &review
			}
			for _, m := range group {
				if i != 1 {
					fmt.Fprintf(&summary, "  %s: %s\n", m.Kind, m.Name)
				}
				fmt.Fprintf(dest, "\n# %s :: %s\n%s", r.Path, m.Name, m.Diff)
			}
		}
	}
	fmt.Fprintf(&summary, "\n主差异: %s\n需复核结构: %s\n疑似噪声: %s\n可视化: %s\n结构化报告: %s\n", paths[3], paths[5], paths[4], paths[2], paths[1])
	jsonData, e := json.MarshalIndent(report, "", "  ")
	if e != nil {
		return e
	}
	htmlData, e := renderHTML(report)
	if e != nil {
		return e
	}
	data := [][]byte{[]byte(summary.String()), append(jsonData, '\n'), htmlData, []byte(patch.String()), []byte(noise.String()), []byte(review.String())}
	for i, p := range paths {
		if e = os.WriteFile(filepath.Join(stage, filepath.Base(p)), data[i], 0644); e != nil {
			return e
		}
	}
	// Back up the complete existing bundle before replacing any of its files.
	// On a publication error, restore all previous reports.
	if e = checkReportOutput(output); e != nil {
		return e
	}
	backups := map[string]string{}
	published := []string{}
	rollback := func(cause error) error {
		for _, p := range published {
			cause = errors.Join(cause, os.Remove(p))
		}
		for p, backup := range backups {
			if e := os.Rename(backup, p); e != nil {
				cleanup = false
				cause = errors.Join(cause, fmt.Errorf("restore %s failed; backup retained at %s: %w", p, backup, e))
			}
		}
		return cause
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			backup := filepath.Join(stage, filepath.Base(p)+".backup")
			if err = os.Rename(p, backup); err != nil {
				return rollback(err)
			}
			backups[p] = backup
		} else if !os.IsNotExist(err) {
			return rollback(err)
		}
	}
	for _, p := range paths {
		if e = os.Rename(filepath.Join(stage, filepath.Base(p)), p); e != nil {
			return rollback(e)
		}
		published = append(published, p)
	}
	return nil
}
