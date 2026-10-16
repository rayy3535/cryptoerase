// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/rayy3535/cryptoerase/scsi"
)

// onSASTransport reports whether a SCSI disk is a SAS end device: a drive
// on a SAS HBA (mpt3sas, smartpqi, …), as opposed to a RAID controller's
// logical volume, or a Fibre Channel or iSCSI LUN of a shared array, which
// must never be sanitized.
func (r *runner) onSASTransport(name string) bool {
	resolved, err := filepath.EvalSymlinks(filepath.Join(r.opts.SysfsRoot, "block", name, "device"))
	return err == nil && strings.Contains(resolved, "/end_device-")
}

// sasDrive erases a SAS drive the OS sees directly, with SCSI SANITIZE,
// CRYPTOGRAPHIC ERASE (SBC-4) sent through SG_IO.
func (r *runner) sasDrive(ctx context.Context, rec *DriveRecord, name, vendor, model, driver string, rotational bool) *DriveRecord {
	p := filepath.Join(r.opts.SysfsRoot, "block", name)
	dev := r.devPath(name)
	rec.Interface = "SAS"
	rec.MediaType = "SSD (SAS)"
	if rotational {
		rec.MediaType = "HDD (SAS)"
	}
	rec.Model = strings.TrimSpace(vendor + " " + model)
	rec.Serial = r.vpdSerial(name)
	rec.Firmware = readTrim(filepath.Join(p, "device", "rev"))
	fc := CheckFirmware(r.opts.FirmwarePolicy, rec.Model, rec.Firmware)
	rec.FirmwarePolicy = &fc
	if fc.Status == "fail" {
		return r.done(rec, Fail, fc.Detail)
	}

	sup, err := r.opts.SCSI.CryptoEraseSupport(dev)
	rec.SCSI = &SCSIInfo{SanitizeCryptoErase: sup.String()}
	switch {
	case err != nil:
		return r.done(rec, Unhandled, fmt.Sprintf("cannot ask the drive which SANITIZE actions it supports (driver=%s): %v", driver, err))
	case sup == scsi.SupportUnknown:
		return r.done(rec, Unhandled, "the drive does not report its supported commands (REPORT SUPPORTED OPERATION CODES), so whether it supports SCSI SANITIZE CRYPTOGRAPHIC ERASE is unknown; not erased")
	case sup == scsi.SupportNo && rotational:
		return r.done(rec, Unhandled, "hard disk without a cryptographic erase (no SCSI SANITIZE CRYPTOGRAPHIC ERASE support, i.e. not a self-encrypting drive); overwriting is outside this tool's scope")
	case sup == scsi.SupportNo:
		return r.done(rec, Fail, "no SCSI SANITIZE CRYPTOGRAPHIC ERASE support")
	}
	rec.NISTMethod = "Purge"
	rec.Scope = "all user data on the device"
	rec.Technique = "Cryptographic Erase"
	rec.TechniqueDetail = "SCSI SANITIZE, CRYPTOGRAPHIC ERASE"
	rec.Command = "SANITIZE (48h), service action 03h (CRYPTOGRAPHIC ERASE), IMMED, on " + dev

	// REQUEST SENSE changes nothing, so it runs in inventory mode too.
	st, err := r.scsiSanitizeState(dev)
	switch {
	case err != nil:
		return r.done(rec, Fail, fmt.Sprintf("sanitize state unreadable (REQUEST SENSE): %v", err))
	case st.State == scsi.SanitizeFailed:
		rec.DeviceStatus = &DeviceStatus{SCSISanitize: st}
		return r.done(rec, Fail, "a previous sanitize failed (SANITIZE COMMAND FAILED): the drive rejects reads and writes until a sanitize completes, so markers cannot be written; run one without markers (sg_sanitize --crypto), then rerun")
	}
	rec.Planned = "scsi-sanitize-crypto-erase"
	if r.opts.Mode == ModeInventory {
		return r.done(rec, Planned, "")
	}
	rec.Planned = ""
	if st.InProgress() {
		if _, err := r.waitSCSISanitize(ctx, dev); err != nil {
			return r.done(rec, Fail, fmt.Sprintf("previous sanitize did not finish: %v", err))
		}
	}
	if ctx.Err() != nil {
		return r.done(rec, Fail, "interrupted before erase; drive not modified")
	}

	m, err := r.writeMarkers(dev)
	if err != nil {
		if errors.Is(err, errMarkerReadback) {
			return r.done(rec, Fail, fmt.Sprintf("marker read-back mismatch before erase: %v", err))
		}
		return r.done(rec, Fail, fmt.Sprintf("cannot write markers before erase (locked or read-only?): %v", err))
	}
	defer m.release()

	start := time.Now()
	if err := r.opts.SCSI.CryptoErase(dev); err != nil {
		return r.done(rec, Fail, fmt.Sprintf("SANITIZE CRYPTOGRAPHIC ERASE rejected: %v", err))
	}
	st, err = r.waitSCSISanitize(ctx, dev)
	rec.DeviceStatus = &DeviceStatus{SCSISanitize: st}
	switch {
	case err != nil && ctx.Err() != nil:
		return r.done(rec, Fail, "interrupted while sanitizing; the device continues the operation on its own, rerun to verify")
	case err != nil:
		return r.done(rec, Fail, fmt.Sprintf("sanitize state unreadable or stalled: %v", err))
	case st.State == scsi.SanitizeFailed:
		return r.done(rec, Fail, "the drive reports the sanitize failed (SANITIZE COMMAND FAILED)")
	}
	rec.EraseDurationSec = seconds(time.Since(start))
	m.release() // the erase is over: udev may probe the disk now

	v, verr := r.verifyMarkers(dev, m)
	if verr != nil {
		r.log.Warn("verify", "device", dev, "error", verr)
	}
	rec.Verification = r.verification(v)
	res, reason := verifiedResult(v)
	return r.done(rec, res, reason)
}

// scsiSanitizeState reads the sanitize state, reading again past pending
// unit attentions (REQUEST SENSE reports and clears one at a time).
func (r *runner) scsiSanitizeState(dev string) (*scsi.SanitizeStatus, error) {
	var st *scsi.SanitizeStatus
	var err error
	for range 4 {
		if st, err = r.opts.SCSI.SanitizeStatus(dev); err != nil || st.State != scsi.SanitizeOther {
			return st, err
		}
	}
	return st, fmt.Errorf("unexpected sense data %s", st.Sense)
}

// waitSCSISanitize polls REQUEST SENSE until no sanitize is in progress.
func (r *runner) waitSCSISanitize(ctx context.Context, dev string) (*scsi.SanitizeStatus, error) {
	var last *scsi.SanitizeStatus
	lastChange := time.Now()
	errs := 0
	for {
		st, err := r.scsiSanitizeState(dev)
		if err != nil {
			errs++
			if errs > 3 {
				return last, err
			}
		} else {
			errs = 0
			if !st.InProgress() {
				return st, nil
			}
			if last == nil || st.Progress != last.Progress {
				lastChange = time.Now()
			} else if time.Since(lastChange) >= r.opts.NoProgressTimeout {
				return st, fmt.Errorf("%w at progress %d/65536 for %s", errStalled, st.Progress, r.opts.NoProgressTimeout)
			}
			last = st
		}
		if err := sleep(ctx, r.opts.PollInterval); err != nil {
			return last, err
		}
	}
}
