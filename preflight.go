// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// preflight checks, before anything is changed, that the tools the run will
// need are available: hdparm for SATA drives, the PERC/MegaRAID CLI when such
// a controller is present (drives behind it may not be visible to the OS) or
// for RAIDReset. A missing tool stops the run with
// ErrToolMissing rather than failing drive after drive.
//
// Only disks the run would work on count: removable, USB, empty, excluded
// and in-use disks need no tool. With RAIDReset both tools are needed, since
// the drives it exposes are not visible yet.
func (r *runner) preflight(ctx context.Context) error {
	var ataWhy, percWhy string
	if r.opts.RAIDReset {
		ataWhy, percWhy = "RAID reset requested", "RAID reset requested"
	}
	blocks, _ := os.ReadDir(filepath.Join(r.opts.SysfsRoot, "block"))
	for _, b := range blocks {
		name := b.Name()
		if !strings.HasPrefix(name, "sd") || r.isExcluded(name) || r.inUse[name] {
			continue
		}
		p := filepath.Join(r.opts.SysfsRoot, "block", name)
		resolved, _ := filepath.EvalSymlinks(p)
		if readTrim(filepath.Join(p, "removable")) == "1" || strings.Contains(resolved, "/usb") || readTrim(filepath.Join(p, "size")) == "0" {
			continue
		}
		vendor := readTrim(filepath.Join(p, "device", "vendor"))
		model := readTrim(filepath.Join(p, "device", "model"))
		driver := r.scsiDriver(name)
		if first, _, _ := strings.Cut(vendor, " "); first == "ATA" {
			ataWhy = "SATA drive " + name
		}
		if driver == "megaraid_sas" || strings.Contains(model, "PERC") {
			percWhy = "PERC/MegaRAID disk " + name
		}
	}
	// Drives in state Ready or hot spares are not visible to the OS at all;
	// only the controller CLI can list them.
	if host := r.megaraidHost(); host != "" && percWhy == "" {
		percWhy = "PERC/MegaRAID controller " + host
	}
	var errs []error
	check := func(why string, backend any) {
		c, ok := backend.(Checker)
		if why == "" || !ok {
			return
		}
		if err := c.Check(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", why, err))
		}
	}
	check(ataWhy, r.opts.ATA)
	check(percWhy, r.opts.PERC)
	if len(errs) > 0 {
		return fmt.Errorf("%w; nothing was changed: %w", ErrToolMissing, errors.Join(errs...))
	}
	return nil
}
