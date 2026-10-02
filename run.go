// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/rayy3535/cryptoerase/perc"
)

type runner struct {
	opts  Options
	log   *slog.Logger
	inUse map[string]bool

	percOnce   sync.Once
	percDrives []perc.Drive
	percErr    error

	raidNotes   map[string]raidNote
	raidExposed []exposedDrive

	// Controller configuration for erases done by the controller, read
	// once; percMu serializes the controller commands of those erases.
	ctrlOnce sync.Once
	ctrls    []perc.Controller
	ctrlErr  error
	percMu   sync.Mutex
}

// Run detects every drive, then inventories or erases them concurrently and
// returns the report. An error is returned only when the run could not start
// (bad options, unreadable sysfs); per-drive problems are in the report.
func Run(ctx context.Context, o Options) (*Report, error) {
	opts, err := o.withDefaults()
	if err != nil {
		return nil, err
	}
	r := &runner{opts: opts, log: opts.Logger}
	rep := &Report{
		Schema:    Schema,
		Mode:      opts.Mode.String(),
		JobID:     opts.JobID,
		StartedAt: time.Now().UTC(),
		Policy: PolicySummary{
			AllowFormat: opts.AllowFormat,
			RAIDReset:   opts.RAIDReset,
			Samples:     opts.Samples,
			Exclude:     opts.Exclude,
		},
	}
	for _, fr := range opts.FirmwarePolicy {
		rep.Policy.FirmwareRules = append(rep.Policy.FirmwareRules, fr.String())
	}
	// In-use disks are known before any RAID change, so a virtual disk that
	// backs the running OS is never deleted.
	r.inUse = r.computeInUse()
	if err := r.preflight(ctx); err != nil {
		return nil, err
	}
	if opts.RAIDReset {
		rep.RAIDReset = r.raidReset(ctx)
	}
	inv, err := r.discover()
	if err != nil {
		return nil, fmt.Errorf("discover drives: %w", err)
	}
	r.log.Info("discovered", "mode", opts.Mode.String(), "nvme_controllers", len(inv.nvme),
		"scsi_disks", len(inv.scsi), "other", len(inv.other), "in_use", strings.Join(keys(r.inUse), ","))

	type job struct {
		dev string
		fn  func(context.Context) *DriveRecord
	}
	var jobs []job
	for _, c := range inv.nvme {
		jobs = append(jobs, job{r.devPath(c.name), func(ctx context.Context) *DriveRecord { return r.nvmeDrive(ctx, c) }})
	}
	for _, s := range inv.scsi {
		jobs = append(jobs, job{r.devPath(s), func(ctx context.Context) *DriveRecord { return r.scsiDrive(ctx, s) }})
	}
	for _, s := range inv.other {
		jobs = append(jobs, job{r.devPath(s), func(ctx context.Context) *DriveRecord { return r.otherDrive(s) }})
	}

	results := make([]*DriveRecord, len(jobs))
	par := opts.Parallel
	if par <= 0 || par > len(jobs) {
		par = len(jobs)
	}
	sem := make(chan struct{}, max(par, 1))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			defer func() {
				if p := recover(); p != nil {
					rec := r.newRecord(j.dev)
					results[i] = r.done(rec, Fail, fmt.Sprintf("internal error: %v", p))
					r.log.Error("panic", "device", j.dev, "panic", p, "stack", string(debug.Stack()))
				}
			}()
			results[i] = j.fn(ctx)
		})
	}
	wg.Wait()

	rep.Drives = results
	if len(r.raidExposed) > 0 {
		r.reconcileRAID(rep)
	}
	rep.Host = r.host()
	rep.Tool = Tool{
		Name:          "cryptoerase",
		Version:       Version,
		GoVersion:     runtime.Version(),
		NVMeTransport: "Linux NVME_IOCTL_ADMIN_CMD",
	}
	if len(inv.scsi) > 0 {
		rep.Tool.ATABackend = opts.ATA.Version(ctx)
	}
	rep.FinishedAt = time.Now().UTC()
	rep.finalize()
	r.log.Info("finished", "result", rep.Result, "counts", rep.Counts)
	return rep, nil
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sortNatural(out)
	return out
}

func (r *runner) devPath(name string) string { return filepath.Join(r.opts.DevRoot, name) }

func (r *runner) isExcluded(name string) bool {
	for _, e := range r.opts.Exclude {
		if filepath.Base(e) == name {
			return true
		}
	}
	return false
}

func (r *runner) newRecord(dev string) *DriveRecord {
	return &DriveRecord{Device: dev, StartedAt: time.Now().UTC()}
}

func (r *runner) done(rec *DriveRecord, res Result, reason string) *DriveRecord {
	rec.Result, rec.Reason, rec.FinishedAt = res, reason, time.Now().UTC()
	r.log.Info("drive", "device", rec.Device, "model", rec.Model, "serial", rec.Serial, "result", res, "reason", reason)
	return rec
}

func seconds(d time.Duration) float64 { return math.Round(d.Seconds()*1000) / 1000 }

// sleep waits d or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// waitNode waits for a device node to exist again after the kernel rescans.
func (r *runner) waitNode(ctx context.Context, path string) bool {
	deadline := time.Now().Add(r.opts.NodeWait)
	for {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		if time.Now().After(deadline) || sleep(ctx, 200*time.Millisecond) != nil {
			return false
		}
	}
}

func (r *runner) otherDrive(name string) *DriveRecord {
	rec := r.newRecord(r.devPath(name))
	if readTrim(filepath.Join(r.opts.SysfsRoot, "block", name, "removable")) == "1" {
		return r.done(rec, Skipped, "removable device")
	}
	if r.inUse[name] {
		return r.done(rec, Unhandled, "unsupported device type, in use by the running OS")
	}
	return r.done(rec, Unhandled, "unsupported device type")
}
