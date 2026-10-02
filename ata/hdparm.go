// SPDX-License-Identifier: Apache-2.0

package ata

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Hdparm implements Backend by running hdparm(8) (version 9.56 or later for
// --Istdout in host byte order).
type Hdparm struct {
	// Path to the hdparm binary; "hdparm" (looked up in PATH) if empty.
	Path string
	// Timeout per invocation; 60 s if zero.
	Timeout time.Duration
}

// HdparmError carries hdparm's exit status and stderr.
type HdparmError struct {
	Args   []string
	Err    error
	Stderr string
}

func (e *HdparmError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("hdparm %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *HdparmError) Unwrap() error { return e.Err }

// run returns hdparm's stdout and stderr.
func (h *Hdparm) run(ctx context.Context, args ...string) (string, string, error) {
	path := h.Path
	if path == "" {
		path = "hdparm"
	}
	timeout := h.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	// G204: the hdparm path is operator configuration; no shell is involved.
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // G204, see above
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// If hdparm is killed on timeout but something still holds its output
	// pipes, stop waiting for them shortly after.
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return stdout.String(), stderr.String(), &HdparmError{Args: args, Err: err, Stderr: stderr.String() + stdout.String()}
	}
	return stdout.String(), stderr.String(), nil
}

// noRegisters reports whether hdparm got no ATA registers back for a
// command it sent with SG_IO. hdparm then prints the SCSI sense buffer it
// could not decode and carries on with zeroed registers.
func noRegisters(out string) bool { return strings.Contains(out, "bad/missing sense data") }

// Identify runs `hdparm --Istdout DEV` and decodes the 256 hex words.
func (h *Hdparm) Identify(ctx context.Context, dev string) (*Identify, error) {
	out, _, err := h.run(ctx, "--Istdout", dev)
	if err != nil {
		return nil, err
	}
	return ParseIstdout(out)
}

// ParseIstdout decodes `hdparm --Istdout` output: 32 lines of eight 4-digit
// hex words in host byte order.
func ParseIstdout(out string) (*Identify, error) {
	words := make([]uint16, 0, 256)
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 8 {
			continue
		}
		row := make([]uint16, 0, 8)
		for _, s := range f {
			if len(s) != 4 {
				row = nil
				break
			}
			v, err := strconv.ParseUint(s, 16, 16)
			if err != nil {
				row = nil
				break
			}
			row = append(row, uint16(v))
		}
		words = append(words, row...)
	}
	if len(words) != 256 {
		return nil, fmt.Errorf("hdparm --Istdout: got %d identify words, want 256", len(words))
	}
	return FromWords(words)
}

var (
	reState    = regexp.MustCompile(`State:\s+(SD\d)`)
	reProgress = regexp.MustCompile(`Progress:\s+0x([0-9a-fA-F]+)`)
)

// SanitizeStatus runs `hdparm --sanitize-status DEV`. It returns an error
// wrapping ErrNoRegisters when the status could not be read back.
func (h *Hdparm) SanitizeStatus(ctx context.Context, dev string) (*SanitizeStatus, error) {
	args := []string{"--sanitize-status", dev}
	out, errout, err := h.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	if noRegisters(errout + out) {
		return nil, &HdparmError{Args: args, Err: ErrNoRegisters, Stderr: errout}
	}
	return ParseSanitizeStatus(out)
}

// ParseSanitizeStatus decodes hdparm's SANITIZE STATUS EXT report.
func ParseSanitizeStatus(out string) (*SanitizeStatus, error) {
	m := reState.FindStringSubmatch(out)
	if m == nil {
		return nil, fmt.Errorf("hdparm --sanitize-status: no state in output %q", strings.TrimSpace(out))
	}
	s := &SanitizeStatus{
		State:                 m[1],
		InProgress:            m[1] == "SD2",
		Frozen:                m[1] == "SD1",
		CompletedWithoutError: strings.Contains(out, "Last Sanitize Operation Completed Without Error"),
		Antifreeze:            strings.Contains(out, "Antifreeze bit set"),
		Raw:                   strings.TrimSpace(out),
	}
	if p := reProgress.FindStringSubmatch(out); p != nil {
		v, _ := strconv.ParseUint(p[1], 16, 16)
		s.Progress = uint16(v)
	}
	return s, nil
}

// SanitizeCryptoScramble runs
// `hdparm --yes-i-know-what-i-am-doing --sanitize-crypto-scramble DEV`.
func (h *Hdparm) SanitizeCryptoScramble(ctx context.Context, dev string) error {
	out, errout, err := h.run(ctx, "--yes-i-know-what-i-am-doing", "--sanitize-crypto-scramble", dev)
	if err != nil {
		return err
	}
	if noRegisters(errout + out) {
		return &HdparmError{Args: []string{"--sanitize-crypto-scramble", dev}, Err: ErrNoRegisters, Stderr: errout}
	}
	if !strings.Contains(out, "Operation started in background") {
		return &HdparmError{Args: []string{"--sanitize-crypto-scramble", dev}, Err: errors.New("unexpected output"), Stderr: out}
	}
	return nil
}

// Version returns the first line of `hdparm -V`.
func (h *Hdparm) Version(ctx context.Context) string {
	out, _, err := h.run(ctx, "-V")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
}

var _ Backend = (*Hdparm)(nil)
