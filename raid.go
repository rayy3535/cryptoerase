// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rayy3535/cryptoerase/perc"
)

// RAIDResetter changes a RAID controller's configuration. *perc.Lister
// implements it with perccli/storcli.
type RAIDResetter interface {
	Controllers(ctx context.Context) ([]perc.Controller, error)
	DeleteVD(ctx context.Context, c, vd int) (string, error)
	DeleteHotSpare(ctx context.Context, c int, slot string) (string, error)
	SetJBOD(ctx context.Context, c int, slot string) (string, error)
}

// RAIDReset records what Options.RAIDReset did, or in inventory mode would
// do, on one controller. Controller is -1 for problems that concern no
// particular controller (no CLI installed, listing failed).
type RAIDReset struct {
	Controller int    `json:"controller"`
	Tool       string `json:"tool,omitempty"`
	// Skipped says why the controller was left unchanged.
	Skipped string `json:"skipped,omitempty"`
	// DeleteVDs are removed; KeepVDs back the running OS and stay.
	DeleteVDs []perc.VirtualDisk `json:"delete_virtual_disks,omitempty"`
	KeepVDs   []perc.VirtualDisk `json:"keep_virtual_disks,omitempty"`
	// Expose are the drives set to non-RAID so the OS sees them directly.
	Expose []perc.Drive `json:"expose_drives,omitempty"`
	// Commands were run in erase mode, or would be run in inventory mode.
	Commands []string `json:"commands,omitempty"`
	Executed bool     `json:"executed"`
	Error    string   `json:"error,omitempty"`

	hotSpares []perc.Drive
}

// raidReset plans the reset of every controller and, in erase mode, applies
// it and waits for the exposed drives to appear.
func (r *runner) raidReset(ctx context.Context) []*RAIDReset {
	rs, ok := r.opts.PERC.(RAIDResetter)
	if !ok {
		return []*RAIDReset{{Controller: -1, Error: "the RAID backend cannot change controller configuration"}}
	}
	ctrls, err := rs.Controllers(ctx)
	switch {
	case err != nil:
		return []*RAIDReset{{Controller: -1, Error: fmt.Sprintf("read controller configuration: %v", err)}}
	case len(ctrls) == 0:
		return []*RAIDReset{{Controller: -1, Skipped: "no PERC/MegaRAID CLI (perccli64, storcli64) installed, or no controller found"}}
	}
	megaraidInUse := false
	for name := range r.inUse {
		if r.scsiDriver(name) == "megaraid_sas" {
			megaraidInUse = true
		}
	}
	var out []*RAIDReset
	for _, c := range ctrls {
		rr := r.planRAID(c, megaraidInUse)
		if rr == nil {
			continue
		}
		out = append(out, rr)
		r.noteRAIDPlan(c, rr)
	}
	if r.opts.Mode != ModeErase || ctx.Err() != nil {
		return out
	}
	for _, rr := range out {
		if rr.Skipped != "" || rr.Controller < 0 {
			continue
		}
		exposed := r.applyRAID(ctx, rs, rr)
		for _, d := range rr.Expose {
			e := exposedDrive{ctrl: rr.Controller, drive: d}
			if !exposed[d.Slot] {
				e.err = rr.Error
			}
			r.raidExposed = append(r.raidExposed, e)
		}
	}
	if len(r.raidExposed) > 0 {
		r.waitExposed(ctx)
	}
	return out
}

type exposedDrive struct {
	ctrl  int
	drive perc.Drive
	err   string // why the drive was not exposed
}

// planRAID decides what to do on one controller. It returns nil when the
// controller has nothing to reset.
func (r *runner) planRAID(c perc.Controller, megaraidInUse bool) *RAIDReset {
	rr := &RAIDReset{Controller: c.Index, Tool: c.Tool}
	keepDG := map[int]bool{}
	deleteDG := map[int]bool{}
	for _, vd := range c.VDs {
		name := r.vdDevice(vd)
		switch {
		case name == "" && megaraidInUse:
			rr.Skipped = fmt.Sprintf("cannot tell whether virtual disk %d holds the running OS (no OS device name or NAA match), and the OS uses a disk on this controller type", vd.VD)
			return rr
		case name != "" && r.inUse[name]:
			rr.KeepVDs = append(rr.KeepVDs, vd)
			keepDG[vd.DG] = true
		default:
			rr.DeleteVDs = append(rr.DeleteVDs, vd)
			deleteDG[vd.DG] = true
		}
	}
	for _, d := range c.Drives {
		state := strings.ToUpper(strings.TrimSpace(d.State))
		switch {
		case d.Foreign():
			rr.Skipped = fmt.Sprintf("drive %s carries a foreign configuration; import or clear it first (%s %s/fall show)", d.Slot, c.Tool, fmt.Sprintf("/c%d", c.Index))
			return rr
		case state == "ONLN":
			dg := d.DriveGroup()
			if keepDG[dg] {
				continue
			}
			if !deleteDG[dg] {
				rr.Skipped = fmt.Sprintf("drive %s is online in drive group %d, which has no virtual disk", d.Slot, dg)
				return rr
			}
			rr.Expose = append(rr.Expose, d)
		case state == "UGOOD":
			rr.Expose = append(rr.Expose, d)
		case state == "DHS" || state == "GHS":
			rr.hotSpares = append(rr.hotSpares, d)
			rr.Expose = append(rr.Expose, d)
		case state == "JBOD":
			// Already visible to the OS; erased like any other disk.
		default:
			rr.Skipped = fmt.Sprintf("drive %s is in state %q; resolve it before resetting RAID", d.Slot, d.State)
			return rr
		}
	}
	if len(rr.DeleteVDs) == 0 && len(rr.Expose) == 0 {
		if len(rr.KeepVDs) == 0 {
			return nil
		}
		return rr
	}
	for _, d := range rr.hotSpares {
		rr.Commands = append(rr.Commands, fmt.Sprintf("%s %s delete hotsparedrive", c.Tool, perc.Path(c.Index, d.Slot)))
	}
	for _, vd := range rr.DeleteVDs {
		rr.Commands = append(rr.Commands, fmt.Sprintf("%s /c%d/v%d delete force", c.Tool, c.Index, vd.VD))
	}
	for _, d := range rr.Expose {
		rr.Commands = append(rr.Commands, fmt.Sprintf("%s %s set jbod", c.Tool, perc.Path(c.Index, d.Slot)))
	}
	return rr
}

// applyRAID runs the planned commands in order and stops at the first
// failure. Commands then lists what was actually run. It returns the slots
// that were set to non-RAID.
func (r *runner) applyRAID(ctx context.Context, rs RAIDResetter, rr *RAIDReset) map[string]bool {
	exposed := map[string]bool{}
	rr.Commands = nil
	rr.Executed = true
	step := func(cmd string, err error) bool {
		rr.Commands = append(rr.Commands, cmd)
		if err != nil {
			rr.Error = err.Error()
			r.log.Error("raid reset", "controller", rr.Controller, "command", cmd, "error", err)
			return false
		}
		r.log.Info("raid reset", "controller", rr.Controller, "command", cmd)
		return true
	}
	for _, d := range rr.hotSpares {
		if !step(rs.DeleteHotSpare(ctx, rr.Controller, d.Slot)) {
			return exposed
		}
	}
	for _, vd := range rr.DeleteVDs {
		if !step(rs.DeleteVD(ctx, rr.Controller, vd.VD)) {
			return exposed
		}
	}
	for _, d := range rr.Expose {
		if !step(rs.SetJBOD(ctx, rr.Controller, d.Slot)) {
			return exposed
		}
		exposed[d.Slot] = true
	}
	return exposed
}

// waitExposed waits until every exposed drive shows up as a block device,
// or RAIDWait passes. Drives that never appear are reported by
// reconcileRAID.
func (r *runner) waitExposed(ctx context.Context) {
	deadline := time.Now().Add(r.opts.RAIDWait)
	for {
		missing := 0
		for _, e := range r.raidExposed {
			if e.err == "" && r.findExposed(e.drive) == "" {
				missing++
			}
		}
		if missing == 0 {
			return
		}
		if time.Now().After(deadline) {
			r.log.Warn("raid reset: exposed drives not visible", "missing", missing, "waited", r.opts.RAIDWait)
			return
		}
		if sleep(ctx, time.Second) != nil {
			return
		}
	}
}

// findExposed returns the block device name of a physical drive (see
// blockMatchesDrive), or "".
func (r *runner) findExposed(d perc.Drive) string {
	blocks, _ := os.ReadDir(filepath.Join(r.opts.SysfsRoot, "block"))
	for _, b := range blocks {
		if r.blockMatchesDrive(b.Name(), d) {
			return b.Name()
		}
	}
	return ""
}

func normWWN(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"naa.", "eui.", "0x"} {
		s = strings.TrimPrefix(s, p)
	}
	return s
}

// wwidMatches compares a sysfs wwid ("naa.5002538e...", or for SATA disks
// "t10.ATA <model> <serial>") with a drive's WWN or serial number.
func wwidMatches(wwid string, d perc.Drive) bool {
	if wwid == "" {
		return false
	}
	if d.WWN != "" && normWWN(wwid) == normWWN(d.WWN) {
		return true
	}
	return d.Serial != "" && strings.HasPrefix(strings.ToLower(wwid), "t10.") && slices.Contains(strings.Fields(wwid), d.Serial)
}

// vdDevice returns the block device name of a virtual disk, from the name
// the CLI reports or by matching its NAA identifier against sysfs; "" when
// it cannot be determined.
func (r *runner) vdDevice(vd perc.VirtualDisk) string {
	if vd.OSDevice != "" {
		return filepath.Base(vd.OSDevice)
	}
	if vd.NAA == "" {
		return ""
	}
	blocks, _ := os.ReadDir(filepath.Join(r.opts.SysfsRoot, "block"))
	for _, b := range blocks {
		if normWWN(readTrim(filepath.Join(r.opts.SysfsRoot, "block", b.Name(), "device", "wwid"))) == normWWN(vd.NAA) {
			return b.Name()
		}
	}
	return ""
}

// noteRAIDPlan remembers, per virtual-disk block device, what the reset
// does with it, for that device's drive record.
func (r *runner) noteRAIDPlan(c perc.Controller, rr *RAIDReset) {
	if r.raidNotes == nil {
		r.raidNotes = map[string]raidNote{}
	}
	if rr.Skipped != "" {
		for _, vd := range c.VDs {
			if name := r.vdDevice(vd); name != "" {
				r.raidNotes[name] = raidNote{skipped: rr.Skipped}
			}
		}
		return
	}
	for _, vd := range rr.DeleteVDs {
		if name := r.vdDevice(vd); name != "" {
			r.raidNotes[name] = raidNote{deleted: true, drives: len(vd.Drives)}
		}
	}
}

type raidNote struct {
	deleted bool
	drives  int
	skipped string
}

// reconcileRAID makes sure every drive exposed by the reset was erased, or
// is reported: a drive that never appeared to the OS still holds its data.
func (r *runner) reconcileRAID(rep *Report) {
	for _, e := range r.raidExposed {
		slot := perc.Path(e.ctrl, e.drive.Slot)
		if match := r.recordOf(rep, e.drive, slot); match != nil {
			if match.Attach == nil {
				match.Attach = &Attach{}
			}
			match.Attach.RAIDSlot = slot
			continue
		}
		rec := &DriveRecord{
			Device:    slot,
			Interface: e.drive.Interface,
			Model:     e.drive.Model,
			Serial:    e.drive.Serial,
			Attach:    &Attach{RAIDSlot: slot},
			StartedAt: rep.StartedAt,
		}
		reason := "set to non-RAID by the RAID reset but not visible to the OS; not erased"
		if e.err != "" {
			reason = "RAID reset failed (" + e.err + "); drive not exposed and not erased"
		}
		r.done(rec, Fail, reason)
		rep.Drives = append(rep.Drives, rec)
	}
}

func exposedMatches(rec *DriveRecord, d perc.Drive) bool {
	if d.Serial != "" && strings.EqualFold(strings.TrimSpace(rec.Serial), d.Serial) {
		return true
	}
	return rec.Attach != nil && wwidMatches(rec.Attach.WWID, d)
}
