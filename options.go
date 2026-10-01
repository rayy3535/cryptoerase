// SPDX-License-Identifier: Apache-2.0

// Package cryptoerase performs and records cryptographic erase of the NVMe
// and SATA SSDs in a server, e.g. before the server is handed to its next
// user.
//
// Policy:
//
//   - Cryptographic Erase only. NVMe: Sanitize with the Crypto Erase action;
//     Format NVM with SES=010b only when Options.AllowFormat is set and the
//     controller reports no unallocated NVM capacity. SATA: ATA SANITIZE
//     CRYPTO SCRAMBLE EXT.
//   - Fail closed. There is no fallback to block erase, overwrite, ATA
//     Security Erase or zero-fill; a drive without a cryptographic erase
//     method is reported FAIL.
//   - Firmware floor. Drives matching a FirmwareRule below its minimum are
//     reported FAIL.
//   - Drives the host cannot address directly (RAID virtual disks, SAS, HDDs,
//     disks in use by the running OS, exclusions) are reported UNHANDLED,
//     never skipped silently.
//   - Verification: the device's completion status, plus markers. Random
//     1 MiB markers are written at evenly spaced offsets of every namespace or
//     disk and read back before the erase; after the erase none may read back
//     unchanged.
//
// Drives are processed concurrently (Options.Parallel). Run returns a Report
// whose ExitCode follows the CLI convention: 0 pass, 1 fail, 2 incomplete.
package cryptoerase

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/rayy3535/cryptoerase/ata"
	"github.com/rayy3535/cryptoerase/blockdev"
	"github.com/rayy3535/cryptoerase/nvme"
	"github.com/rayy3535/cryptoerase/perc"
)

// Version of the library and CLI.
const Version = "0.1.0"

// Mode selects what Run does.
type Mode int

const (
	// ModeInventory detects drives and capabilities and plans the erase
	// without writing anything.
	ModeInventory Mode = iota
	// ModeErase erases. Requires Options.Confirm.
	ModeErase
)

func (m Mode) String() string {
	if m == ModeErase {
		return "erase"
	}
	return "inventory"
}

// PERCLister lists physical drives behind a RAID controller.
type PERCLister interface {
	List(ctx context.Context) ([]perc.Drive, error)
}

// Options configures Run. Zero values select defaults and the real system.
type Options struct {
	Mode Mode
	// Confirm must be true for ModeErase.
	Confirm bool
	// AllowFormat accepts NVMe Format NVM with SES=010b (Cryptographic Erase)
	// for controllers without Sanitize Crypto Erase (e.g. Samsung PM983,
	// Intel P4610).
	AllowFormat bool
	// Samples is the number of markers per namespace or disk (default 16,
	// minimum 2).
	Samples int
	// Parallel bounds how many drives are processed at once; 0 means all.
	Parallel int
	// Exclude lists devices never to touch (sda, /dev/sda, nvme0, nvme0n1).
	Exclude []string
	// FirmwarePolicy replaces DefaultFirmwarePolicy when non-nil.
	FirmwarePolicy []FirmwareRule
	// JobID is copied into the report.
	JobID string

	PollInterval      time.Duration // sanitize status polling (default 10 s)
	NoProgressTimeout time.Duration // give up when progress stalls (default 20 min)
	FormatTimeout     time.Duration // per Format NVM command (default 10 min)
	NodeWait          time.Duration // wait for namespace nodes after erase (default 10 s)

	Logger *slog.Logger

	// Environment. Empty roots mean /sys, /proc and /dev.
	SysfsRoot string
	ProcRoot  string
	DevRoot   string
	// DisableDirectIO turns off O_DIRECT for markers (tests on tmpfs).
	DisableDirectIO bool

	// Backends. Nil selects the Linux implementations.
	OpenNVMe    func(path string) (nvme.Device, error)
	NamespaceID func(blockDev string) (uint32, error)
	OpenBlock   func(path string, direct bool) (blockdev.Device, error)
	ATA         ata.Backend
	PERC        PERCLister
}

// ErrNotConfirmed is returned when ModeErase is requested without Confirm.
var ErrNotConfirmed = errors.New("cryptoerase: erase requested without confirmation")

func (o *Options) withDefaults() (Options, error) {
	v := *o
	if v.Mode == ModeErase && !v.Confirm {
		return v, ErrNotConfirmed
	}
	if v.Samples == 0 {
		v.Samples = 16
	}
	if v.Samples < 2 {
		return v, errors.New("cryptoerase: Samples must be >= 2")
	}
	if v.FirmwarePolicy == nil {
		v.FirmwarePolicy = DefaultFirmwarePolicy()
	}
	if v.PollInterval == 0 {
		v.PollInterval = 10 * time.Second
	}
	if v.NoProgressTimeout == 0 {
		v.NoProgressTimeout = 20 * time.Minute
	}
	if v.FormatTimeout == 0 {
		v.FormatTimeout = 10 * time.Minute
	}
	if v.NodeWait == 0 {
		v.NodeWait = 10 * time.Second
	}
	if v.Logger == nil {
		v.Logger = slog.Default()
	}
	if v.SysfsRoot == "" {
		v.SysfsRoot = "/sys"
	}
	if v.ProcRoot == "" {
		v.ProcRoot = "/proc"
	}
	if v.DevRoot == "" {
		v.DevRoot = "/dev"
	}
	if v.OpenNVMe == nil {
		v.OpenNVMe = func(p string) (nvme.Device, error) { return nvme.OpenController(p) }
	}
	if v.NamespaceID == nil {
		v.NamespaceID = nvme.NamespaceID
	}
	if v.OpenBlock == nil {
		v.OpenBlock = func(p string, direct bool) (blockdev.Device, error) { return blockdev.Open(p, direct) }
	}
	if v.ATA == nil {
		v.ATA = &ata.Hdparm{}
	}
	if v.PERC == nil {
		v.PERC = &perc.Lister{}
	}
	return v, nil
}
