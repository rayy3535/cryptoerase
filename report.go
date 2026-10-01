// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"time"

	"github.com/rayy3535/cryptoerase/ata"
	"github.com/rayy3535/cryptoerase/nvme"
	"github.com/rayy3535/cryptoerase/perc"
	"github.com/rayy3535/cryptoerase/tcg"
)

// Schema identifies the report format.
const Schema = "cryptoerase.report/v1"

// Result is the outcome for one drive.
type Result string

// Drive results.
const (
	Pass      Result = "PASS"      // crypto-erased and verified
	Fail      Result = "FAIL"      // cannot be, or was not, crypto-erased
	Unhandled Result = "UNHANDLED" // not addressable from this host; needs another method
	Skipped   Result = "SKIPPED"   // not server storage (removable, no medium)
	Planned   Result = "PLANNED"   // inventory: a crypto-erase method is available
)

// Report is the JSON document written for every run.
type Report struct {
	Schema     string         `json:"schema"`
	Mode       string         `json:"mode"`
	JobID      string         `json:"job_id,omitempty"`
	Host       Host           `json:"host"`
	Tool       Tool           `json:"tool"`
	Policy     PolicySummary  `json:"policy"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt time.Time      `json:"finished_at"`
	Result     string         `json:"result"` // PASS, FAIL or INCOMPLETE
	Counts     map[Result]int `json:"counts"`
	Drives     []*DriveRecord `json:"drives"`
}

// ExitCode maps the result to the CLI convention: 0 PASS, 1 FAIL,
// 2 INCOMPLETE (no failures, but some drives need another method).
func (r *Report) ExitCode() int {
	switch r.Result {
	case "PASS":
		return 0
	case "INCOMPLETE":
		return 2
	}
	return 1
}

func (r *Report) finalize() {
	r.Counts = map[Result]int{}
	for _, d := range r.Drives {
		r.Counts[d.Result]++
	}
	switch {
	case r.Counts[Fail] > 0:
		r.Result = "FAIL"
	case r.Counts[Pass]+r.Counts[Planned]+r.Counts[Unhandled] == 0:
		r.Result = "FAIL" // no drive found
	case r.Counts[Unhandled] > 0:
		r.Result = "INCOMPLETE"
	default:
		r.Result = "PASS"
	}
}

// Host identifies the server.
type Host struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Product      string `json:"product,omitempty"`
	Serial       string `json:"serial,omitempty"`
	Hostname     string `json:"hostname,omitempty"`
	Kernel       string `json:"kernel,omitempty"`
}

// Tool identifies the software that produced the report.
type Tool struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	GoVersion     string `json:"go_version"`
	NVMeTransport string `json:"nvme_transport"`
	ATABackend    string `json:"ata_backend,omitempty"`
}

// PolicySummary records the policy the run applied.
type PolicySummary struct {
	AllowFormat   bool     `json:"allow_format"`
	Samples       int      `json:"samples"`
	FirmwareRules []string `json:"firmware_rules"`
	Exclude       []string `json:"exclude,omitempty"`
}

// DriveRecord is the evidence for one drive. Fields follow
// NIST SP 800-88r2 sec. 4.6 where applicable.
type DriveRecord struct {
	Device        string   `json:"device"`
	Interface     string   `json:"interface,omitempty"`
	MediaType     string   `json:"media_type,omitempty"`
	Namespaces    []string `json:"namespaces,omitempty"`
	Model         string   `json:"model,omitempty"`
	Serial        string   `json:"serial,omitempty"`
	Firmware      string   `json:"firmware,omitempty"`
	CapacityBytes uint64   `json:"capacity_bytes,omitempty"`

	Attach *Attach        `json:"attach,omitempty"`
	NVMe   *NVMeInfo      `json:"nvme,omitempty"`
	ATA    *ATAInfo       `json:"ata,omitempty"`
	TCG    *TCGInfo       `json:"tcg,omitempty"`
	Health *nvme.SmartLog `json:"health,omitempty"`
	PERC   *PERCInfo      `json:"perc,omitempty"`

	FirmwarePolicy *FirmwareCheck `json:"firmware_policy,omitempty"`

	NISTMethod      string `json:"nist_method,omitempty"`
	Technique       string `json:"technique,omitempty"`
	TechniqueDetail string `json:"technique_detail,omitempty"`
	Scope           string `json:"scope,omitempty"`
	Command         string `json:"command,omitempty"`
	Planned         string `json:"planned,omitempty"`

	DeviceStatus     *DeviceStatus `json:"device_status,omitempty"`
	EraseDurationSec float64       `json:"erase_duration_s,omitempty"`
	Verification     *Verification `json:"verification,omitempty"`

	Result     Result    `json:"result"`
	Reason     string    `json:"reason,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// Attach describes how a SCSI-class disk is connected.
type Attach struct {
	Driver     string `json:"driver,omitempty"`
	SCSIVendor string `json:"scsi_vendor,omitempty"`
	SCSIModel  string `json:"scsi_model,omitempty"`
}

// NVMeInfo is the controller's erase-relevant identification.
type NVMeInfo struct {
	Version                           string       `json:"version"`
	OACS                              uint16       `json:"oacs"`
	SANICAP                           uint32       `json:"sanicap"`
	FNA                               uint8        `json:"fna"`
	TNVMCAP                           nvme.Uint128 `json:"tnvmcap"`
	UNVMCAP                           nvme.Uint128 `json:"unvmcap"`
	SanitizeCryptoErase               bool         `json:"sanitize_crypto_erase"`
	SanitizeBlockErase                bool         `json:"sanitize_block_erase"`
	SanitizeOverwrite                 bool         `json:"sanitize_overwrite"`
	FormatCryptoErase                 bool         `json:"format_crypto_erase"`
	FormatAppliesToAllNamespaces      bool         `json:"format_applies_to_all_namespaces"`
	SecureEraseAppliesToAllNamespaces bool         `json:"secure_erase_applies_to_all_namespaces"`
}

// ATAInfo is the drive's erase-relevant identification.
type ATAInfo struct {
	SanitizeFeatureSet bool         `json:"sanitize_feature_set"`
	CryptoScramble     bool         `json:"sanitize_crypto_scramble"`
	BlockErase         bool         `json:"sanitize_block_erase"`
	Overwrite          bool         `json:"sanitize_overwrite"`
	NonRotating        bool         `json:"non_rotating"`
	Security           ata.Security `json:"security"`
}

// TCGInfo is the Level 0 Discovery result.
type TCGInfo struct {
	*tcg.Level0
	Error string `json:"error,omitempty"`
}

// PERCInfo lists the physical drives behind a RAID virtual disk.
type PERCInfo struct {
	PhysicalDrives []perc.Drive `json:"physical_drives,omitempty"`
	Error          string       `json:"error,omitempty"`
}

// FirmwareCheck is the outcome of the firmware floor.
type FirmwareCheck struct {
	Status string `json:"status"` // pass, fail, no_rule
	Detail string `json:"detail,omitempty"`
}

// DeviceStatus is what the device itself reported about the erase.
type DeviceStatus struct {
	SSTAT                    string              `json:"sstat,omitempty"`
	SPROG                    *uint16             `json:"sprog,omitempty"`
	GlobalDataErased         *bool               `json:"global_data_erased,omitempty"`
	EstimatedCryptoEraseSecs *uint32             `json:"estimated_crypto_erase_s,omitempty"`
	Format                   []FormatResult      `json:"format,omitempty"`
	ATASanitize              *ata.SanitizeStatus `json:"ata_sanitize_status,omitempty"`
	RescanError              string              `json:"rescan_error,omitempty"`
}

// FormatResult records one Format NVM command.
type FormatResult struct {
	Namespace string `json:"namespace"`
	NSID      uint32 `json:"nsid"`
	LBAF      uint8  `json:"lbaf"`
	CDW10     string `json:"cdw10"`
}

// Verification records the marker check.
type Verification struct {
	Method         string `json:"method"`
	Samples        int    `json:"samples"`
	Changed        int    `json:"changed"`
	ZeroAfterErase int    `json:"zero_after_erase"`
	Unreadable     int    `json:"unreadable"`
}
