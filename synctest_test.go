// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rayy3535/cryptoerase/ata"
	"github.com/rayy3535/cryptoerase/nvme"
)

// These tests run the polling loops with the production defaults (10 s poll
// interval, 20 min no-progress timeout) on a fake clock, so the timing rules
// are checked exactly and instantly.

// sprogScript is an nvme.Device whose Sanitize Status log follows a function
// of the elapsed (fake) time.
type sprogScript struct {
	nvme.Device // nil: only SanitizeLog is used
	start       time.Time
	at          func(elapsed time.Duration) (*nvme.SanitizeLog, error)
}

func (s *sprogScript) SanitizeLog() (*nvme.SanitizeLog, error) { return s.at(time.Since(s.start)) }

func defaultRunner(t *testing.T) *runner {
	t.Helper()
	o, err := (&Options{Mode: ModeInventory, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	return &runner{opts: o, log: o.Logger}
}

func inProgress(sprog uint16) *nvme.SanitizeLog {
	return &nvme.SanitizeLog{SSTAT: nvme.SanitizeInProgress, SPROG: sprog}
}

func TestSynctestNVMeStallAfterExactly20Minutes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := defaultRunner(t)
		start := time.Now()
		dev := &sprogScript{start: start, at: func(time.Duration) (*nvme.SanitizeLog, error) { return inProgress(0x1000), nil }}
		l, err := r.waitNVMeSanitize(context.Background(), dev)
		if !errors.Is(err, errStalled) || l == nil || l.SPROG != 0x1000 {
			t.Fatalf("%v %+v", err, l)
		}
		if got := time.Since(start); got != 20*time.Minute {
			t.Fatalf("stalled after %s, want 20m0s", got)
		}
	})
}

func TestSynctestNVMeSlowProgressIsNotAStall(t *testing.T) {
	// SPROG moves every 19 minutes: slow, but never stalled. A 6-hour
	// sanitize must be followed to the end.
	synctest.Test(t, func(t *testing.T) {
		r := defaultRunner(t)
		start := time.Now()
		dev := &sprogScript{start: start, at: func(e time.Duration) (*nvme.SanitizeLog, error) {
			if e >= 6*time.Hour {
				return &nvme.SanitizeLog{SSTAT: nvme.SanitizeSucceeded | 0x100, SPROG: 0xffff}, nil
			}
			return inProgress(uint16(e / (19 * time.Minute))), nil
		}}
		l, err := r.waitNVMeSanitize(context.Background(), dev)
		if err != nil || l.Status() != nvme.SanitizeSucceeded {
			t.Fatalf("%v %+v", err, l)
		}
		if got := time.Since(start); got != 6*time.Hour {
			t.Fatalf("finished after %s", got)
		}
	})
}

func TestSynctestNVMeTransientLogErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := defaultRunner(t)
		boom := errors.New("boom")
		// Three consecutive errors are tolerated, the fourth is not.
		polls := 0
		dev := &sprogScript{start: time.Now(), at: func(time.Duration) (*nvme.SanitizeLog, error) {
			polls++
			switch {
			case polls <= 3:
				return nil, boom
			case polls == 4:
				return inProgress(1), nil
			case polls <= 8:
				return nil, boom
			}
			return &nvme.SanitizeLog{SSTAT: nvme.SanitizeSucceeded}, nil
		}}
		start := time.Now()
		l, err := r.waitNVMeSanitize(context.Background(), dev)
		if !errors.Is(err, boom) || l == nil || l.SPROG != 1 || polls != 8 {
			t.Fatalf("err %v log %+v polls %d", err, l, polls)
		}
		if got := time.Since(start); got != 70*time.Second {
			t.Fatalf("gave up after %s, want 70s (7 poll intervals)", got)
		}
	})
}

func TestSynctestNVMeCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := defaultRunner(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		dev := &sprogScript{start: time.Now(), at: func(e time.Duration) (*nvme.SanitizeLog, error) {
			return inProgress(uint16(e / time.Second)), nil
		}}
		_, err := r.waitNVMeSanitize(ctx, dev)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	})
}

// ataScript is an ata.Backend whose status follows the fake clock.
type ataScript struct {
	ata.Backend
	start time.Time
	at    func(elapsed time.Duration) (*ata.SanitizeStatus, error)
}

func (a *ataScript) SanitizeStatus(context.Context, string) (*ata.SanitizeStatus, error) {
	return a.at(time.Since(a.start))
}

func TestSynctestATAStallAndSlowProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := defaultRunner(t)
		start := time.Now()
		r.opts.ATA = &ataScript{start: start, at: func(time.Duration) (*ata.SanitizeStatus, error) {
			return &ata.SanitizeStatus{InProgress: true, Progress: 0x8000}, nil
		}}
		if _, err := r.waitATASanitize(context.Background(), "/dev/sda"); !errors.Is(err, errStalled) {
			t.Fatal(err)
		}
		if got := time.Since(start); got != 20*time.Minute {
			t.Fatalf("stalled after %s", got)
		}

		start = time.Now()
		r.opts.ATA = &ataScript{start: start, at: func(e time.Duration) (*ata.SanitizeStatus, error) {
			if e >= 3*time.Hour {
				return &ata.SanitizeStatus{State: "SD0", CompletedWithoutError: true}, nil
			}
			return &ata.SanitizeStatus{InProgress: true, Progress: uint16(e / (15 * time.Minute))}, nil
		}}
		st, err := r.waitATASanitize(context.Background(), "/dev/sda")
		if err != nil || !st.CompletedWithoutError || time.Since(start) != 3*time.Hour {
			t.Fatalf("%v %+v after %s", err, st, time.Since(start))
		}
	})
}

func TestSynctestWaitNode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := defaultRunner(t)
		start := time.Now()
		if r.waitNode(context.Background(), "/nonexistent/cryptoerase-test") {
			t.Fatal("node found")
		}
		if got := time.Since(start); got < 10*time.Second || got > 10*time.Second+200*time.Millisecond {
			t.Fatalf("waited %s, want ~NodeWait (10s)", got)
		}
	})
}
