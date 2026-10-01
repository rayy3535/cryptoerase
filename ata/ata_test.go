// SPDX-License-Identifier: Apache-2.0

package ata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ataString packs s into n words, high byte first, space padded.
func ataString(w []uint16, first, n int, s string) {
	b := []byte(s)
	for len(b) < 2*n {
		b = append(b, ' ')
	}
	for i := range n {
		w[first+i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
	}
}

// IdentifyWords builds IDENTIFY words for tests.
func identifyWords(model, serial, fw string, w59, w128 uint16, ssd bool) []uint16 {
	w := make([]uint16, 256)
	ataString(w, 10, 10, serial)
	ataString(w, 23, 4, fw)
	ataString(w, 27, 20, model)
	w[59] = w59
	w[83] = 0x0400
	w[100], w[101] = 0x1ab0, 0x6fc8 // 1875385008 sectors
	w[128] = w128
	if ssd {
		w[217] = 1
	}
	return w
}

func istdout(w []uint16) string {
	var sb strings.Builder
	for i := 0; i < 256; i += 8 {
		for j := range 8 {
			if j > 0 {
				sb.WriteByte(' ')
			}
			fmt.Fprintf(&sb, "%04x", w[i+j])
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func TestParseIstdout(t *testing.T) {
	w := identifyWords("SAMSUNG MZ7LH960HAJR-00005", "EXAMPLESN0000002", "HXT7404Q", 0xb000, 0x0029, true)
	id, err := ParseIstdout("\n/dev/sda:\n" + istdout(w))
	if err != nil {
		t.Fatal(err)
	}
	if id.Model() != "SAMSUNG MZ7LH960HAJR-00005" || id.Serial() != "EXAMPLESN0000002" || id.Firmware() != "HXT7404Q" {
		t.Fatalf("strings %q %q %q", id.Model(), id.Serial(), id.Firmware())
	}
	if !id.SanitizeSupported() || !id.CryptoScrambleSupported() || id.OverwriteSupported() || !id.BlockEraseSupported() {
		t.Errorf("word 59 decode %#x", id.Words[59])
	}
	sec := id.Security()
	if !sec.Supported || sec.Enabled || sec.Locked || !sec.Frozen || !sec.EnhancedErase {
		t.Errorf("security decode %+v", sec)
	}
	if !id.NonRotating() || id.Sectors() != 1875385008 {
		t.Errorf("ssd=%v sectors=%d", id.NonRotating(), id.Sectors())
	}
}

func TestParseIstdoutShort(t *testing.T) {
	if _, err := ParseIstdout("0000 0000\n"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseSanitizeStatus(t *testing.T) {
	inProgress := "Issuing SANITIZE_STATUS command\nSanitize status:\n    State:    SD2 Sanitize operation In Process\n    Progress: 0x8000 (50%)\n"
	s, err := ParseSanitizeStatus(inProgress)
	if err != nil || !s.InProgress || s.Progress != 0x8000 || s.CompletedWithoutError {
		t.Fatalf("%v %+v", err, s)
	}
	done := "Issuing SANITIZE_STATUS command\nSanitize status:\n    State:    SD0 Sanitize Idle\n    Last Sanitize Operation Completed Without Error\n"
	s, err = ParseSanitizeStatus(done)
	if err != nil || s.InProgress || !s.CompletedWithoutError || s.State != "SD0" {
		t.Fatalf("%v %+v", err, s)
	}
	frozen := "Issuing SANITIZE_STATUS command\nSanitize status:\n    State:    SD1 Sanitize Frozen\n"
	s, _ = ParseSanitizeStatus(frozen)
	if !s.Frozen {
		t.Fatalf("%+v", s)
	}
	if _, err := ParseSanitizeStatus("SG_IO: bad/missing sense data"); err == nil {
		t.Fatal("expected error")
	}
}

// fakeHdparm writes a shell script standing in for hdparm.
func fakeHdparm(t *testing.T, body string) *Hdparm {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hdparm")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Hdparm{Path: p}
}

func TestHdparmBackend(t *testing.T) {
	w := identifyWords("MODEL X", "SN1", "FW1", 0x3000, 0x0001, true)
	ident := filepath.Join(t.TempDir(), "ident")
	if err := os.WriteFile(ident, []byte(istdout(w)), 0o644); err != nil {
		t.Fatal(err)
	}
	h := fakeHdparm(t, `
case "$1" in
  -V) echo "hdparm v9.65" ;;
  --Istdout) cat `+ident+` ;;
  --sanitize-status) printf 'Sanitize status:\n    State:    SD0 Sanitize Idle\n    Last Sanitize Operation Completed Without Error\n' ;;
  --yes-i-know-what-i-am-doing)
    if [ "$3" = /dev/frozen ]; then
      echo "SANITIZE failed: Input/output error" >&2
      echo "SANITIZE device error reason: Device in FROZEN state" >&2
      exit 5
    fi
    printf 'Issuing SANITIZE_CRYPTO_SCRAMBLE command\nOperation started in background\n' ;;
esac
`)
	ctx := context.Background()
	if v := h.Version(ctx); v != "hdparm v9.65" {
		t.Errorf("version %q", v)
	}
	id, err := h.Identify(ctx, "/dev/sda")
	if err != nil || id.Model() != "MODEL X" {
		t.Fatalf("%v %v", err, id)
	}
	st, err := h.SanitizeStatus(ctx, "/dev/sda")
	if err != nil || !st.CompletedWithoutError {
		t.Fatalf("%v %+v", err, st)
	}
	if err := h.SanitizeCryptoScramble(ctx, "/dev/sda"); err != nil {
		t.Fatal(err)
	}
	err = h.SanitizeCryptoScramble(ctx, "/dev/frozen")
	var he *HdparmError
	if !errors.As(err, &he) || !strings.Contains(err.Error(), "Device in FROZEN state") {
		t.Fatalf("frozen error: %v", err)
	}
}
