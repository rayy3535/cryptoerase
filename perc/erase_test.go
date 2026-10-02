// SPDX-License-Identifier: Apache-2.0

package perc

import (
	"context"
	"os"
	"strings"
	"testing"
)

// events_latest.txt follows "perccli64 /c0 show events type=latest=N" from a
// PERC H355 (perccli 007.1623): the event records come first, the CLI's
// status block after them. The codes of the erase events are not from the
// capture.
func TestParseEvents(t *testing.T) {
	b, err := os.ReadFile("testdata/events_latest.txt")
	if err != nil {
		t.Fatal(err)
	}
	evs, err := ParseEvents(string(b))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 5 || LatestSeq(evs) != 0xbcc {
		t.Fatalf("%d events, latest %#x", len(evs), LatestSeq(evs))
	}
	e := evs[2]
	if e.Seq != 0xbc8 || e.Description != "Erase completed on PD 26(e0x44/s0)" || e.DeviceID == nil || *e.DeviceID != 38 ||
		e.Time != "Fri Oct  2 05:41:23 2026" || e.Code != "0x00000062" {
		t.Fatalf("%+v", e)
	}
	if evs[4].DeviceID != nil || evs[4].Description != "Controller requests a host bus rescan" {
		t.Fatalf("rescan event %+v", evs[4])
	}

	for _, tc := range []struct {
		seq      uint32
		did      int
		want     string
		wantSeq  uint32
		describe string
	}{
		{0xbc5, 38, "completed", 0xbc8, "after the baseline"},
		{0xbc8, 38, "", 0, "nothing newer than the baseline"},
		{0xbc5, 37, "", 0, "another drive"},
	} {
		o, ev := EraseOutcome(evs, tc.seq, tc.did)
		if o != tc.want || (ev == nil) != (tc.want == "") || (ev != nil && ev.Seq != tc.wantSeq) {
			t.Errorf("%s: %q %+v", tc.describe, o, ev)
		}
	}
}

func TestEraseOutcomeFromDescription(t *testing.T) {
	// Without Event Data the PD number (hex) in the description decides;
	// the newest result wins.
	evs := []Event{
		{Seq: 10, Description: "Erase failed on PD 25(e0x44/s1) (Error 02)"},
		{Seq: 11, Description: "Erase completed on PD 26(e0x44/s0)"},
		{Seq: 12, Description: "Erase aborted on PD 25(e0x44/s1)"},
	}
	if o, ev := EraseOutcome(evs, 0, 0x25); o != "aborted" || ev.Seq != 12 {
		t.Fatalf("%q %+v", o, ev)
	}
	if o, _ := EraseOutcome(evs, 0, 38); o != "completed" {
		t.Fatalf("%q", o)
	}
}

func TestParseEventsErrors(t *testing.T) {
	for name, in := range map[string]string{
		"failure status": "CLI Version = 007\nController = 0\nStatus = Failure\nDescription = Controller 0 not found\n",
		"no events":      "Status = Success\nDescription = None\n",
		"bad seq":        "seqNum: 0xfffffffff\n",
	} {
		if _, err := ParseEvents(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEraseCommands(t *testing.T) {
	var got []string
	answers := map[string]string{
		"/c0/e68/s0 set good force J":     `{"Controllers":[{"Command Status":{"Controller":0,"Status":"Success","Description":"Set Drive Good Succeeded."}}]}`,
		"/c0/e68/s0 start erase crypto J": `{"Controllers":[{"Command Status":{"Controller":0,"Status":"Success","Description":"Start Drive Erase Succeeded."}}]}`,
		"/c0/e68/s0 show erase J":         `{"Controllers":[{"Command Status":{"Controller":0,"Status":"Success","Description":"Show Drive Erase Status Succeeded."},"Response Data":[{"Drive-ID":"/c0/e68/s0","Progress%":"-","Status":"Not in progress","Estimated Time Left":"-"}]}]}`,
		"/c0/e68/s1 show erase J":         `{"Controllers":[{"Command Status":{"Controller":0,"Status":"Success","Description":"Show Drive Erase Status Succeeded."},"Response Data":[{"Drive-ID":"/c0/e68/s1","Progress%":42,"Status":"In progress","Estimated Time Left":"1 Seconds"}]}]}`,
		"/c0 show events type=latest=2":   "seqNum: 0x00000010\nEvent Description: Erase completed on PD 26(e0x44/s0)\nEvent Data:\n===========\nDevice ID: 38\nStatus = Success\n",
	}
	l := &Lister{
		LookPath: func(string) (string, error) { return "/usr/sbin/perccli64", nil },
		Exec: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			k := strings.Join(args, " ")
			got = append(got, k)
			return []byte(answers[k]), nil
		},
	}
	ctx := context.Background()
	if cmd, err := l.SetGood(ctx, 0, "68:0"); err != nil || cmd != "perccli64 /c0/e68/s0 set good force" {
		t.Fatalf("%q %v", cmd, err)
	}
	if cmd, err := l.StartCryptoErase(ctx, 0, "68:0"); err != nil || cmd != "perccli64 /c0/e68/s0 start erase crypto" {
		t.Fatalf("%q %v", cmd, err)
	}
	p, err := l.EraseStatus(ctx, 0, "68:0")
	if err != nil || p.InProgress() || p.Percent != -1 || p.Status != "Not in progress" {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = l.EraseStatus(ctx, 0, "68:1")
	if err != nil || !p.InProgress() || p.Percent != 42 {
		t.Fatalf("%+v %v", p, err)
	}
	evs, err := l.Events(ctx, 0, 2)
	if err != nil || len(evs) != 1 || *evs[0].DeviceID != 38 {
		t.Fatalf("%+v %v", evs, err)
	}
	if _, err := l.EraseStatus(ctx, 0, "68:9"); err == nil {
		t.Fatal("unreadable show erase accepted")
	}
	if _, err := l.Events(ctx, 1, 2); err == nil {
		t.Fatal("empty events accepted")
	}
	none := &Lister{LookPath: func(string) (string, error) { return "", os.ErrNotExist }}
	if _, err := none.Events(ctx, 0, 1); err == nil {
		t.Fatal("events without CLI")
	}
}

func TestParseEraseStatusNoRow(t *testing.T) {
	if _, err := parseEraseStatus(&jsonDoc{}); err == nil {
		t.Fatal("empty accepted")
	}
}
