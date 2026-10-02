// SPDX-License-Identifier: Apache-2.0

package ata

import (
	"context"
	"encoding/binary"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseIdentifyBytes(t *testing.T) {
	w := identifyWords("EXAMPLE SSD", "SN42", "FW9", 0x3000, 0x0001, true)
	b := make([]byte, 512)
	for i, v := range w {
		binary.LittleEndian.PutUint16(b[2*i:], v)
	}
	id, err := ParseIdentify(b)
	if err != nil {
		t.Fatal(err)
	}
	if id.Model() != "EXAMPLE SSD" || id.Serial() != "SN42" || id.Firmware() != "FW9" || id.Sectors() != 1875385008 {
		t.Fatalf("%q %q %q %d", id.Model(), id.Serial(), id.Firmware(), id.Sectors())
	}
	if _, err := ParseIdentify(b[:510]); err == nil {
		t.Fatal("short buffer accepted")
	}
	if _, err := FromWords(w[:255]); err == nil {
		t.Fatal("short word slice accepted")
	}
}

func TestNonRotating(t *testing.T) {
	ssd, _ := FromWords(identifyWords("M", "S", "F", 0, 0, true))
	hdd, _ := FromWords(identifyWords("M", "S", "F", 0, 0, false))
	if !ssd.NonRotating() || hdd.NonRotating() {
		t.Fatal("word 217")
	}
}

func TestSectors28Bit(t *testing.T) {
	w := make([]uint16, 256)
	w[60], w[61] = 0x5678, 0x0fff // no LBA48 (word 83 bit 10 clear)
	id, _ := FromWords(w)
	if id.Sectors() != 0x0fff5678 {
		t.Fatalf("%#x", id.Sectors())
	}
}

func TestHdparmErrors(t *testing.T) {
	ctx := context.Background()
	// Not installed: the error must unwrap to exec.ErrNotFound, which the
	// erase workflow reports as a missing backend rather than a drive issue.
	missing := &Hdparm{Path: "cryptoerase-test-no-such-hdparm"}
	_, err := missing.Identify(ctx, "/dev/sda")
	var he *HdparmError
	if !errors.Is(err, exec.ErrNotFound) || !errors.As(err, &he) || !strings.Contains(err.Error(), "--Istdout /dev/sda") {
		t.Fatalf("identify: %v", err)
	}
	if _, err := missing.SanitizeStatus(ctx, "/dev/sda"); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("status: %v", err)
	}
	if err := missing.SanitizeCryptoScramble(ctx, "/dev/sda"); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("scramble: %v", err)
	}
	if v := missing.Version(ctx); v != "" {
		t.Fatalf("version %q", v)
	}

	odd := fakeHdparm(t, `
case "$1" in
  --Istdout) echo "not identify data" ;;
  --sanitize-status) echo "nothing useful" ;;
  --yes-i-know-what-i-am-doing) echo "something else happened" ;;
esac
`)
	if _, err := odd.Identify(ctx, "/dev/sda"); err == nil || !strings.Contains(err.Error(), "got 0 identify words") {
		t.Fatalf("identify parse: %v", err)
	}
	if _, err := odd.SanitizeStatus(ctx, "/dev/sda"); err == nil {
		t.Fatal("status parse accepted")
	}
	if err := odd.SanitizeCryptoScramble(ctx, "/dev/sda"); !errors.As(err, &he) || !strings.Contains(err.Error(), "something else happened") {
		t.Fatalf("scramble output: %v", err)
	}
}

func TestHdparmTimeout(t *testing.T) {
	slow := fakeHdparm(t, "exec sleep 30\n")
	slow.Timeout = 100 * time.Millisecond
	start := time.Now()
	_, err := slow.SanitizeStatus(context.Background(), "/dev/sda")
	var he *HdparmError
	if !errors.As(err, &he) {
		t.Fatalf("%v", err)
	}
	if he.Stderr != "" && !strings.Contains(err.Error(), he.Stderr[:1]) {
		t.Fatalf("message %q", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("timeout not enforced: %s", d)
	}
}

func TestHdparmErrorMessage(t *testing.T) {
	e := &HdparmError{Args: []string{"-V"}, Err: errors.New("exit status 1")}
	if e.Error() != "hdparm -V: exit status 1" || !errors.Is(e, e.Err) {
		t.Fatal(e.Error())
	}
	e.Stderr = "  SG_IO: bad sense data\n"
	if e.Error() != "hdparm -V: SG_IO: bad sense data" {
		t.Fatal(e.Error())
	}
}

func TestParseIstdoutSkipsMalformedRows(t *testing.T) {
	good := istdout(identifyWords("M", "S", "F", 0, 0, true))
	lines := strings.Split(good, "\n")
	lines[3] = "zzzz 0000 0000 0000 0000 0000 0000 0000"
	lines[4] = "00000 000 0000 0000 0000 0000 0000 0000"
	if _, err := ParseIstdout(strings.Join(lines, "\n")); err == nil || !strings.Contains(err.Error(), "got 240") {
		t.Fatalf("%v", err)
	}
	// Banner lines around the hex dump are ignored.
	if _, err := ParseIstdout("\n/dev/sda:\n" + good + "\n"); err != nil {
		t.Fatal(err)
	}
}

func TestHdparmCheck(t *testing.T) {
	ctx := context.Background()
	if err := (&Hdparm{Path: "cryptoerase-test-no-such-hdparm"}).Check(ctx); err == nil || !strings.Contains(err.Error(), "hdparm not found (cryptoerase-test-no-such-hdparm)") {
		t.Fatalf("missing: %v", err)
	}
	if err := fakeHdparm(t, "echo 'hdparm v9.65'\n").Check(ctx); err != nil {
		t.Fatalf("working: %v", err)
	}
	if err := fakeHdparm(t, "exit 1\n").Check(ctx); err == nil || !strings.Contains(err.Error(), "does not run") {
		t.Fatalf("broken: %v", err)
	}
}
