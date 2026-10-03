// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rayy3535/cryptoerase/perc"
)

// PERCEraser has a PERC/MegaRAID controller crypto-erase one of its drives.
// *perc.Lister implements it with perccli/storcli.
type PERCEraser interface {
	Controllers(ctx context.Context) ([]perc.Controller, error)
	SetGood(ctx context.Context, c int, slot string) (string, error)
	StartCryptoErase(ctx context.Context, c int, slot string) (string, error)
	EraseStatus(ctx context.Context, c int, slot string) (perc.EraseProgress, error)
	Events(ctx context.Context, c, n int) ([]perc.Event, error)
	SetJBOD(ctx context.Context, c int, slot string) (string, error)
}

// maxEventWindow bounds how many of the latest controller events are read
// when looking for the erase result. A PERC H730P logs an "Unexpected sense"
// event for every TEST UNIT READY to a drive that is not ready, so the
// erase result can be many events back.
const maxEventWindow = 4096

// udevSettle is how long udev gets to re-read a disk's partition table after
// the markers' write handle is closed, when the disk could not be removed
// from the kernel first.
var udevSettle = 2 * time.Second

// percTarget is a SATA drive its PERC/MegaRAID controller can crypto-erase.
type percTarget struct {
	pe PERCEraser
	c  perc.Controller
	d  perc.Drive
}

// percEraseTarget checks whether the controller in front of a megaraid_sas
// SATA drive can crypto-erase it. The error says why not.
func (r *runner) percEraseTarget(ctx context.Context, rec *DriveRecord, name string) (*percTarget, error) {
	pe, ok := r.opts.PERC.(PERCEraser)
	if !ok {
		return nil, errors.New("the RAID backend cannot erase through the controller")
	}
	c, d, err := r.percDriveFor(ctx, pe, rec, name)
	if err != nil {
		return nil, err
	}
	slot := perc.Path(c.Index, d.Slot)
	switch {
	case !strings.EqualFold(d.State, "JBOD"):
		return nil, fmt.Errorf("controller drive %s is in state %q, not JBOD (non-RAID)", slot, d.State)
	case !d.CryptoErase:
		return nil, fmt.Errorf("the controller does not report drive %s as cryptographic-erase capable", slot)
	case d.DID == nil:
		return nil, fmt.Errorf("the controller reports no device ID for drive %s", slot)
	}
	return &percTarget{pe: pe, c: c, d: d}, nil
}

// percErase has the controller crypto-erase a drive behind it. For SATA
// drives ATA pass-through is not a dependable alternative on PERC: one PERC
// H355 returned no ATA registers (the result cannot be read), and a PERC
// H730P rejected SANITIZE and then reported the drive not ready.
//
// Erase mode: write the markers through the OS; remove the disk from the
// kernel, so nothing reads it while the controller hides it; set it
// unconfigured good; "start erase crypto"; wait for "Erase completed" in the
// controller event log; set it back to JBOD; wait for the disk to reappear;
// verify the markers.
func (r *runner) percErase(ctx context.Context, rec *DriveRecord, name, driver string, locked bool, t *percTarget) *DriveRecord {
	pe, c, d := t.pe, t.c, t.d
	slot := perc.Path(c.Index, d.Slot)
	if rec.Attach == nil {
		rec.Attach = &Attach{}
	}
	rec.Attach.RAIDSlot = slot
	pr := &PERCErase{Controller: c.Index, Slot: slot, DID: *d.DID, Tool: c.Tool}
	rec.Technique = "Cryptographic Erase"
	rec.TechniqueDetail = "drive cryptographic erase by the PERC/MegaRAID controller (start erase crypto); result from the controller event log"
	rec.Command = fmt.Sprintf("%s %s start erase crypto", c.Tool, slot)
	rec.DeviceStatus = &DeviceStatus{PERCErase: pr}
	if r.opts.Mode == ModeInventory {
		rec.Planned = "perc-crypto-erase"
		pr.Commands = []string{
			fmt.Sprintf("%s %s set good force", c.Tool, slot),
			rec.Command,
			fmt.Sprintf("%s %s set jbod", c.Tool, slot),
		}
		return r.done(rec, Planned, fmt.Sprintf("drive behind the %s controller; the controller erases it: set unconfigured good, start erase crypto, set back to non-RAID", driver))
	}
	if locked {
		return r.done(rec, Fail, "ATA security is locked (a user password is set); unlock or PSID revert required")
	}
	if ctx.Err() != nil {
		return r.done(rec, Fail, "interrupted before erase; drive not modified")
	}

	orig := name
	host := r.scsiHost(name)
	m, err := r.writeMarkers(r.devPath(name))
	if err != nil && errors.Is(err, syscall.EIO) {
		// A PERC H730P left drives NOT READY after a SANITIZE sent through
		// ATA pass-through, failing every read and write; a controller erase
		// made them usable again. Erase once to recover, then erase again
		// with markers.
		again, rerr := r.percRecover(ctx, pe, pr, name, host, d, err)
		if rerr != nil {
			return r.done(rec, Fail, fmt.Sprintf("the drive rejects writes (%v), and a controller erase to make it usable again failed: %v; treat the drive as not erased", err, rerr))
		}
		name = again
		m, err = r.writeMarkers(r.devPath(name))
	}
	if err != nil {
		if errors.Is(err, errMarkerReadback) {
			return r.done(rec, Fail, fmt.Sprintf("marker read-back mismatch before erase: %v", err))
		}
		return r.done(rec, Fail, fmt.Sprintf("cannot write markers before erase (locked or read-only?): %v", err))
	}
	// The write handle stays open until the disk has been removed from the
	// kernel (see percCryptoErase).
	defer m.release()

	start := time.Now()
	r.percMu.Lock()
	jbodErr, eraseErr := r.percCryptoErase(ctx, pe, pr, name, host, d.Slot, m)
	r.percMu.Unlock()
	switch {
	case eraseErr != nil && jbodErr != nil:
		return r.done(rec, Fail, fmt.Sprintf("%v; setting the drive back to non-RAID also failed: %v", eraseErr, jbodErr))
	case eraseErr != nil:
		return r.done(rec, Fail, eraseErr.Error())
	case pr.Outcome != "completed":
		return r.done(rec, Fail, "controller event log: "+pr.Event.Description)
	case jbodErr != nil:
		return r.done(rec, Fail, fmt.Sprintf("the controller logged %q, but setting the drive back to non-RAID failed (%v), so the erase could not be verified", pr.Event.Description, jbodErr))
	}
	rec.EraseDurationSec = seconds(time.Since(start))

	again := r.waitPERCDrive(ctx, d, host)
	if again == "" {
		return r.done(rec, Fail, fmt.Sprintf("the controller logged %q, but the drive did not come back to the OS within %s after being set to non-RAID, so the erase could not be verified", pr.Event.Description, r.opts.RAIDWait))
	}
	if again != orig {
		pr.ReattachedAs = r.devPath(again)
	}
	v, verr := r.verifyMarkers(r.devPath(again), m)
	if verr != nil {
		r.log.Warn("verify", "device", r.devPath(again), "error", verr)
	}
	rec.Verification = r.verification(v)
	res, reason := verifiedResult(v)
	return r.done(rec, res, reason)
}

// percRecover erases a drive that rejects writes through the controller,
// without markers, and returns the block device it comes back as. pr keeps
// the commands; Recovery records what happened.
func (r *runner) percRecover(ctx context.Context, pe PERCEraser, pr *PERCErase, name, host string, d perc.Drive, cause error) (string, error) {
	r.log.Warn("drive rejects writes; erasing it through the controller first", "device", r.devPath(name), "error", cause)
	r.percMu.Lock()
	jbodErr, eraseErr := r.percCryptoErase(ctx, pe, pr, name, host, d.Slot, nil)
	r.percMu.Unlock()
	switch {
	case eraseErr != nil:
		return "", eraseErr
	case pr.Outcome != "completed":
		return "", errors.New("controller event log: " + pr.Event.Description)
	case jbodErr != nil:
		return "", fmt.Errorf("setting the drive back to non-RAID failed: %w", jbodErr)
	}
	again := r.waitPERCDrive(ctx, d, host)
	if again == "" {
		return "", fmt.Errorf("the drive did not come back to the OS within %s", r.opts.RAIDWait)
	}
	pr.Recovery = fmt.Sprintf("the drive rejected writes (%v); a first controller erase (%q) made it writable again, then it was erased with markers", cause, pr.Event.Description)
	pr.Outcome, pr.Event = "", nil
	return again, nil
}

// percDriveFor finds the controller drive behind block device name: by the
// record's serial number, or as blockMatchesDrive does.
func (r *runner) percDriveFor(ctx context.Context, pe PERCEraser, rec *DriveRecord, name string) (perc.Controller, perc.Drive, error) {
	r.ctrlOnce.Do(func() { r.ctrls, r.ctrlErr = pe.Controllers(ctx) })
	switch {
	case r.ctrlErr != nil:
		return perc.Controller{}, perc.Drive{}, fmt.Errorf("controller configuration unreadable: %w", r.ctrlErr)
	case len(r.ctrls) == 0:
		return perc.Controller{}, perc.Drive{}, errors.New("no PERC/MegaRAID CLI (perccli64, storcli64) to erase through the controller")
	}
	var hits []int
	var found []perc.Drive
	for i, c := range r.ctrls {
		for _, d := range c.Drives {
			if (d.Serial != "" && strings.EqualFold(strings.TrimSpace(rec.Serial), d.Serial)) || r.blockMatchesDrive(name, d) {
				hits, found = append(hits, i), append(found, d)
			}
		}
	}
	switch len(found) {
	case 0:
		return perc.Controller{}, perc.Drive{}, fmt.Errorf("serial %q (and %s) not found among the controller's drives", rec.Serial, name)
	case 1:
		return r.ctrls[hits[0]], found[0], nil
	}
	return perc.Controller{}, perc.Drive{}, fmt.Errorf("serial %q (and %s) matches %d controller drives", rec.Serial, name, len(found))
}

// percCryptoErase runs the controller commands. It records each command and
// the outcome in pr. eraseErr is set when the erase did not run to a logged
// result; jbodErr when the drive could not be set back to JBOD.
func (r *runner) percCryptoErase(ctx context.Context, pe PERCEraser, pr *PERCErase, name, host, slot string, m *markers) (jbodErr, eraseErr error) {
	c := pr.Controller
	evs, err := pe.Events(ctx, c, 1)
	if err != nil {
		return nil, fmt.Errorf("cannot read the controller event log, which holds the erase result: %w; drive not modified", err)
	}
	base := perc.LatestSeq(evs)

	// While unconfigured good the controller hides the drive, and any read
	// of the kernel's disk fails with I/O errors in the kernel log. So the
	// disk is removed from the kernel first, while the markers' write handle
	// is still open: closing that handle makes udev re-read the partition
	// table, and a re-read racing with the removal fails the same way. Once
	// the disk is gone, closing the handle triggers nothing.
	if err := r.detachSCSI(name); err != nil {
		r.log.Warn("remove disk from kernel", "device", name, "error", err)
		m.release()
		// Let udev finish its re-read before the controller hides the drive.
		if err := sleep(ctx, udevSettle); err != nil {
			return nil, fmt.Errorf("interrupted before erase: %w; drive not modified", err)
		}
	}
	m.release()
	cmd, err := pe.SetGood(ctx, c, slot)
	pr.Commands = append(pr.Commands, cmd)
	if err != nil {
		r.rescanSCSI(host)
		return nil, fmt.Errorf("could not set the drive unconfigured good: %w; drive not modified", err)
	}
	cmd, err = pe.StartCryptoErase(ctx, c, slot)
	pr.Commands = append(pr.Commands, cmd)
	if err != nil {
		eraseErr = fmt.Errorf("the controller did not start the crypto erase: %w; not erased", err)
	} else {
		pr.Outcome, pr.Event, eraseErr = r.waitPERCErase(ctx, pe, c, slot, pr.DID, base)
	}
	// Always put the drive back the way it was found.
	cmd, jbodErr = pe.SetJBOD(context.WithoutCancel(ctx), c, slot)
	pr.Commands = append(pr.Commands, cmd)
	return jbodErr, eraseErr
}

// waitPERCErase waits for the controller to log the result of the erase of
// drive did. "show erase" only says whether an erase is running: a crypto
// erase takes no time, so it reads "Not in progress" before and after.
func (r *runner) waitPERCErase(ctx context.Context, pe PERCEraser, c int, slot string, did int, base uint32) (string, *perc.Event, error) {
	every := min(r.opts.PollInterval, time.Second)
	lastPct, lastChange := -2, time.Now()
	idle := 0
	var lastErr error
	for {
		if err := sleep(ctx, every); err != nil {
			return "", nil, fmt.Errorf("interrupted while the controller was erasing; rerun to verify: %w", err)
		}
		evs, err := r.eventsSince(ctx, pe, c, base)
		if err == nil {
			if o, ev := perc.EraseOutcome(evs, base, did); o != "" {
				return o, ev, nil
			}
		} else {
			lastErr = err
		}
		p, err := pe.EraseStatus(ctx, c, slot)
		switch {
		case err != nil:
			lastErr = err
			idle++
		case p.InProgress():
			idle = 0
			if p.Percent != lastPct {
				lastPct, lastChange = p.Percent, time.Now()
			} else if time.Since(lastChange) >= r.opts.NoProgressTimeout {
				return "", nil, fmt.Errorf("%w at %d%% for %s; treat the drive as not erased", errStalled, lastPct, r.opts.NoProgressTimeout)
			}
		default:
			idle++
		}
		if idle >= 10 {
			msg := fmt.Sprintf("the controller shows no erase in progress and logged no result for device ID %d", did)
			if lastErr != nil {
				msg += fmt.Sprintf(" (last error: %v)", lastErr)
			}
			return "", nil, errors.New(msg + "; treat the drive as not erased")
		}
	}
}

// eventsSince reads the controller events newer than base: the newest
// sequence number first, then that many events (at most maxEventWindow).
func (r *runner) eventsSince(ctx context.Context, pe PERCEraser, c int, base uint32) ([]perc.Event, error) {
	last, err := pe.Events(ctx, c, 1)
	if err != nil {
		return nil, err
	}
	newest := perc.LatestSeq(last)
	if newest <= base {
		return nil, nil
	}
	n := min(uint64(newest-base), maxEventWindow)
	return pe.Events(ctx, c, int(n))
}

// waitPERCDrive waits for a drive set back to JBOD to reappear as a disk,
// asking the SCSI host to rescan halfway through the wait.
func (r *runner) waitPERCDrive(ctx context.Context, d perc.Drive, host string) string {
	start := time.Now()
	scanned := false
	for {
		if name := r.findExposed(d); name != "" {
			return name
		}
		if !scanned && time.Since(start) >= r.opts.RAIDWait/2 {
			r.rescanSCSI(host)
			scanned = true
		}
		if time.Since(start) >= r.opts.RAIDWait || sleep(ctx, min(time.Second, r.opts.RAIDWait/10+time.Millisecond)) != nil {
			return ""
		}
	}
}

// detachSCSI removes a disk from the kernel (/sys/block/NAME/device/delete).
func (r *runner) detachSCSI(name string) error {
	return os.WriteFile(filepath.Join(r.opts.SysfsRoot, "block", name, "device", "delete"), []byte("1"), 0o600)
}

// rescanSCSI asks a SCSI host to scan for disks.
func (r *runner) rescanSCSI(host string) {
	if host == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(r.opts.SysfsRoot, "class", "scsi_host", host, "scan"), []byte("- - -"), 0o600); err != nil {
		r.log.Warn("rescan SCSI host", "host", host, "error", err)
	}
}
