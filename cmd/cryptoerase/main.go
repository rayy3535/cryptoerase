// SPDX-License-Identifier: Apache-2.0

// Command cryptoerase cryptographically erases the NVMe and SATA SSDs of a
// server and writes a JSON evidence report. It is a thin wrapper around
// package github.com/rayy3535/cryptoerase.
//
//	cryptoerase --inventory [flags]   detect and plan, no writes
//	cryptoerase --yes [flags]         erase
//
// Exit codes: 0 every in-scope drive erased and verified (inventory: every
// in-scope drive has a method); 1 a drive failed, no drive found, or bad
// arguments; 2 no failures but some drives are UNHANDLED and need another
// method before the server is released.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/rayy3535/cryptoerase"
	"github.com/rayy3535/cryptoerase/ata"
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cryptoerase", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		inventory   = fs.Bool("inventory", false, "detect drives and plan the erase; write nothing")
		yes         = fs.Bool("yes", false, "confirm the erase (required unless --inventory)")
		report      = fs.String("report", "", "JSON report path (default ./cryptoerase-<serial>-<UTC>.json)")
		jobID       = fs.String("job-id", "", "job / ticket ID, copied into the report")
		allowFormat = fs.Bool("allow-format", false, "NVMe: accept Format NVM SES=010b when Sanitize Crypto Erase is not supported")
		fwPolicy    = fs.String("fw-policy", "", "file replacing the built-in firmware floor table")
		samples     = fs.Int("samples", 16, "markers per namespace / disk (>= 2)")
		parallel    = fs.Int("parallel", 0, "drives processed at once (0 = all)")
		poll        = fs.Duration("poll-interval", 10*time.Second, "sanitize status polling interval")
		noProgress  = fs.Duration("no-progress-timeout", 20*time.Minute, "fail a sanitize whose progress does not change for this long")
		formatTO    = fs.Duration("format-timeout", 10*time.Minute, "timeout per Format NVM command")
		hdparm      = fs.String("hdparm", "hdparm", "hdparm binary used for SATA")
		logFormat   = fs.String("log-format", "text", "stderr log format: text or json")
		showVersion = fs.Bool("version", false, "print version and exit")
	)
	var exclude stringList
	fs.Var(&exclude, "exclude", "device never to touch (sda, /dev/sda, nvme0); repeatable, reported UNHANDLED")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: cryptoerase (--inventory | --yes) [flags]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if *showVersion {
		fmt.Fprintln(stdout, "cryptoerase", cryptoerase.Version)
		return 0
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		return 1
	}

	var handler slog.Handler = slog.NewTextHandler(stderr, nil)
	if *logFormat == "json" {
		handler = slog.NewJSONHandler(stderr, nil)
	}
	logger := slog.New(handler)

	opts := cryptoerase.Options{
		Mode:              cryptoerase.ModeErase,
		Confirm:           *yes,
		AllowFormat:       *allowFormat,
		Samples:           *samples,
		Parallel:          *parallel,
		Exclude:           exclude,
		JobID:             *jobID,
		PollInterval:      *poll,
		NoProgressTimeout: *noProgress,
		FormatTimeout:     *formatTO,
		Logger:            logger,
		ATA:               &ata.Hdparm{Path: *hdparm},
	}
	if *inventory {
		opts.Mode = cryptoerase.ModeInventory
	}
	if *fwPolicy != "" {
		b, err := os.ReadFile(*fwPolicy)
		if err != nil {
			logger.Error("read firmware policy", "error", err)
			return 1
		}
		rules, err := cryptoerase.ParseFirmwarePolicy(string(b))
		if err != nil {
			logger.Error("parse firmware policy", "error", err)
			return 1
		}
		opts.FirmwarePolicy = rules
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rep, err := cryptoerase.Run(ctx, opts)
	if err != nil {
		if errors.Is(err, cryptoerase.ErrNotConfirmed) {
			logger.Error("refusing to erase without --yes (use --inventory to only detect)")
		} else {
			logger.Error("run", "error", err)
		}
		return 1
	}

	path := *report
	if path == "" {
		serial := rep.Host.Serial
		if serial == "" {
			serial = "unknown"
		}
		path = fmt.Sprintf("cryptoerase-%s-%s.json", serial, rep.StartedAt.Format("20060102T150405Z"))
	}
	if err := writeReport(path, rep); err != nil {
		logger.Error("write report", "path", path, "error", err)
		return 1
	}
	summary(stderr, rep, path)
	return rep.ExitCode()
}

// writeReport writes atomically: temp file in the same directory, then rename.
func writeReport(path string, rep *cryptoerase.Report) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cryptoerase-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func summary(w io.Writer, rep *cryptoerase.Report, path string) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DEVICE\tMODEL\tSERIAL\tTECHNIQUE\tRESULT\tREASON")
	for _, d := range rep.Drives {
		model := d.Model
		if model == "" && d.Attach != nil {
			model = d.Attach.SCSIModel
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", d.Device, dash(model), dash(d.Serial), dash(d.TechniqueDetail), d.Result, d.Reason)
	}
	tw.Flush()
	fmt.Fprintf(w, "result=%s exit=%d report=%s\n", rep.Result, rep.ExitCode(), path)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
