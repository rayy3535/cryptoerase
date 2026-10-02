// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rayy3535/cryptoerase"
	"github.com/rayy3535/cryptoerase/ata"
	"github.com/rayy3535/cryptoerase/perc"
)

// fakeRun replaces the library for one test and records the Options it got.
func fakeRun(t *testing.T, rep *cryptoerase.Report, err error) *cryptoerase.Options {
	t.Helper()
	var got cryptoerase.Options
	orig := runErase
	runErase = func(_ context.Context, o cryptoerase.Options) (*cryptoerase.Report, error) {
		got = o
		return rep, err
	}
	t.Cleanup(func() { runErase = orig })
	return &got
}

func report(result string, drives ...*cryptoerase.DriveRecord) *cryptoerase.Report {
	return &cryptoerase.Report{
		Schema:    cryptoerase.Schema,
		Mode:      "erase",
		Host:      cryptoerase.Host{Serial: "EXAMPLE01"},
		StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Result:    result,
		Drives:    drives,
	}
}

func chdir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--version"}, &out, &errb); code != 0 {
		t.Fatalf("code %d", code)
	}
	re := regexp.MustCompile(`^cryptoerase ` + regexp.QuoteMeta(cryptoerase.Version) + ` \(.*go1\.[0-9]+.*, linux/[a-z0-9]+\)\n$`)
	if !re.MatchString(out.String()) {
		t.Fatalf("version output %q", out.String())
	}
}

func TestHelp(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"-h"}, &out, &errb); code != 0 || !strings.Contains(errb.String(), "usage: cryptoerase") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
}

func TestRefusesWithoutYes(t *testing.T) {
	chdir(t)
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb); code != 1 || !strings.Contains(errb.String(), "refusing to erase without --yes") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
}

func TestBadArguments(t *testing.T) {
	chdir(t)
	bad := filepath.Join(t.TempDir(), "bad.policy")
	if err := os.WriteFile(bad, []byte("not a rule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--no-such-flag"},
		{"--inventory", "extra"},
		{"--inventory", "--fw-policy", "/nonexistent"},
		{"--inventory", "--fw-policy", bad},
		{"--inventory", "--yes"},
		{"--inventory", "--log-format", "xml"},
		{"--inventory", "--samples", "1"},
		{"--inventory", "--poll-interval", "soon"},
	} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 1 {
			t.Errorf("%v: code %d, want 1 (stderr %q)", args, code, errb.String())
		}
	}
}

func TestFlagsMapToOptions(t *testing.T) {
	dir := chdir(t)
	pol := filepath.Join(dir, "fw.policy")
	if err := os.WriteFile(pol, []byte("# custom\n^EXAMPLE MODEL$ ; . ; 2.0 ; vendor advisory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := fakeRun(t, report("PASS"), nil)
	var out, errb bytes.Buffer
	code := run([]string{
		"--yes", "--allow-format", "--raid-reset", "--job-id", "JOB-1", "--samples", "4", "--parallel", "2",
		"--poll-interval", "3s", "--no-progress-timeout", "7m", "--format-timeout", "9m",
		"--hdparm", "/opt/hdparm", "--raid-cli", "/opt/MegaRAID/perccli/perccli64", "--exclude", "sda", "--exclude", "/dev/nvme1",
		"--fw-policy", pol, "--report", "r.json",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
	if got.Mode != cryptoerase.ModeErase || !got.Confirm || !got.AllowFormat || !got.RAIDReset || got.JobID != "JOB-1" ||
		got.Samples != 4 || got.Parallel != 2 || got.PollInterval != 3*time.Second ||
		got.NoProgressTimeout != 7*time.Minute || got.FormatTimeout != 9*time.Minute {
		t.Errorf("options %+v", got)
	}
	if h, ok := got.ATA.(*ata.Hdparm); !ok || h.Path != "/opt/hdparm" {
		t.Errorf("ATA backend %#v", got.ATA)
	}
	if l, ok := got.PERC.(*perc.Lister); !ok || l.Path != "/opt/MegaRAID/perccli/perccli64" {
		t.Errorf("PERC backend %#v", got.PERC)
	}
	if strings.Join(got.Exclude, ",") != "sda,/dev/nvme1" {
		t.Errorf("exclude %v", got.Exclude)
	}
	if len(got.FirmwarePolicy) != 1 || got.FirmwarePolicy[0].Min != "2.0" || !got.FirmwarePolicy[0].Model.MatchString("EXAMPLE MODEL") {
		t.Errorf("firmware policy %+v", got.FirmwarePolicy)
	}
	if got.Logger == nil {
		t.Error("no logger")
	}
}

func TestInventoryDefaults(t *testing.T) {
	chdir(t)
	got := fakeRun(t, report("PASS"), nil)
	var out, errb bytes.Buffer
	if code := run([]string{"--inventory"}, &out, &errb); code != 0 {
		t.Fatalf("code %d", code)
	}
	if got.Mode != cryptoerase.ModeInventory || got.Confirm || got.FirmwarePolicy != nil || got.Samples != 16 {
		t.Errorf("options %+v", got)
	}
}

func TestExitCodesAndReport(t *testing.T) {
	for _, tc := range []struct {
		result string
		code   int
	}{{"PASS", 0}, {"FAIL", 1}, {"INCOMPLETE", 2}} {
		t.Run(tc.result, func(t *testing.T) {
			dir := chdir(t)
			rep := report(tc.result,
				&cryptoerase.DriveRecord{Device: "/dev/nvme0", Model: "EXAMPLE NVME", Serial: "SN1", TechniqueDetail: "NVMe Sanitize Crypto Erase", Result: cryptoerase.Pass},
				&cryptoerase.DriveRecord{Device: "/dev/sda", Attach: &cryptoerase.Attach{SCSIModel: "PERC H755"}, Result: cryptoerase.Unhandled, Reason: "RAID virtual disk"},
			)
			fakeRun(t, rep, nil)
			var out, errb bytes.Buffer
			if code := run([]string{"--yes", "--log-format", "json"}, &out, &errb); code != tc.code {
				t.Fatalf("code %d want %d", code, tc.code)
			}
			name := "cryptoerase-EXAMPLE01-20260102T030405Z.json"
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			var back cryptoerase.Report
			if err := json.Unmarshal(b, &back); err != nil || back.Result != tc.result || len(back.Drives) != 2 {
				t.Fatalf("report %v %+v", err, back)
			}
			s := errb.String()
			for _, want := range []string{
				"DEVICE", "/dev/nvme0", "EXAMPLE NVME", "NVMe Sanitize Crypto Erase",
				"/dev/sda", "PERC H755", "RAID virtual disk",
				"result=" + tc.result, "report=" + name,
			} {
				if !strings.Contains(s, want) {
					t.Errorf("summary missing %q:\n%s", want, s)
				}
			}
			// Nothing but the report may be left behind (no temp files).
			ents, _ := os.ReadDir(dir)
			if len(ents) != 1 {
				t.Errorf("directory has %d entries", len(ents))
			}
		})
	}
}

func TestUnknownSerialReportName(t *testing.T) {
	dir := chdir(t)
	rep := report("PASS")
	rep.Host.Serial = ""
	fakeRun(t, rep, nil)
	var out, errb bytes.Buffer
	if code := run([]string{"--inventory"}, &out, &errb); code != 0 {
		t.Fatalf("code %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "cryptoerase-unknown-20260102T030405Z.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRunErrorAndUnwritableReport(t *testing.T) {
	chdir(t)
	fakeRun(t, nil, errors.New("discover drives: boom"))
	var out, errb bytes.Buffer
	if code := run([]string{"--inventory"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "boom") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}

	fakeRun(t, report("PASS"), nil)
	errb.Reset()
	code := run([]string{"--inventory", "--report", filepath.Join(t.TempDir(), "missing", "r.json")}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "write report") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
}

func TestWriteReportReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r.json")
	if err := os.WriteFile(p, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeReport(p, report("PASS")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !bytes.HasPrefix(b, []byte("{\n")) || !bytes.HasSuffix(b, []byte("}\n")) {
		t.Fatalf("content %q", b)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("%d entries", len(ents))
	}
	// Renaming over a directory fails; the temp file must still be removed.
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeReport(sub, report("PASS")); err == nil {
		t.Fatal("rename over non-empty directory succeeded")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 2 {
		t.Fatalf("temp file left behind: %d entries", len(ents))
	}
}

func TestStringList(t *testing.T) {
	var s stringList
	_ = s.Set("a")
	_ = s.Set("b")
	if s.String() != "a,b" {
		t.Fatal(s.String())
	}
	if dash("") != "-" || dash("x") != "x" {
		t.Fatal("dash")
	}
}
