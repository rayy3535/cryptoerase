// SPDX-License-Identifier: Apache-2.0

package scsi

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// controlPageData is MODE SENSE(10) data for the Control page, with an
// optional 8-byte block descriptor (some translation layers ignore DBD).
func controlPageData(dsense bool, blockDesc bool) []byte {
	page := []byte{0x8a, 0x0a, 0x02, 0x00, 0x00, 0x00, 0xff, 0xff, 0x00, 0x00, 0x00, 0x1e} // PS set, as returned
	if dsense {
		page[2] |= 0x04
	}
	hdr := make([]byte, 8)
	hdr[2] = 0x00 // medium type
	var bd []byte
	if blockDesc {
		bd = []byte{0, 0, 0, 0, 0, 0, 0x02, 0}
		hdr[7] = 8
	}
	out := append(append(hdr, bd...), page...)
	out[1] = byte(len(out) - 2)
	return out
}

type fakeTarget struct {
	sense   []byte // MODE SENSE reply
	senseCC []byte // MODE SENSE fails with this sense data
	selCC   []byte // MODE SELECT fails with this sense data
	cdbs    [][]byte
	sel     []byte // MODE SELECT parameter data received
}

func (f *fakeTarget) exec(cdb []byte, dir int, data []byte) error {
	f.cdbs = append(f.cdbs, append([]byte(nil), cdb...))
	switch cdb[0] {
	case opModeSense10:
		if dir != DirFromDevice {
			return errors.New("MODE SENSE direction")
		}
		if f.senseCC != nil {
			return &CheckCondition{Sense: f.senseCC}
		}
		copy(data, f.sense)
		return nil
	case opModeSelect10:
		if dir != DirToDevice {
			return errors.New("MODE SELECT direction")
		}
		if f.selCC != nil {
			return &CheckCondition{Sense: f.selCC}
		}
		f.sel = append([]byte(nil), data...)
		return nil
	}
	return errors.New("unexpected opcode")
}

// Fixed-format ILLEGAL REQUEST, INVALID FIELD IN CDB.
var illegalRequest = []byte{0x70, 0, 0x05, 0, 0, 0, 0, 0x0a, 0, 0, 0, 0, 0x24, 0x00}

func TestSetDescriptorSense(t *testing.T) {
	for _, bd := range []bool{false, true} {
		f := &fakeTarget{sense: controlPageData(false, bd)}
		changed, err := SetDescriptorSense(f.exec)
		if err != nil || !changed {
			t.Fatalf("block descriptor %v: %v %v", bd, changed, err)
		}
		if want := []byte{0x5a, 0x08, 0x0a, 0, 0, 0, 0, 0, 64, 0}; !bytes.Equal(f.cdbs[0], want) {
			t.Errorf("MODE SENSE CDB % x", f.cdbs[0])
		}
		if want := []byte{0x55, 0x10, 0, 0, 0, 0, 0, 0, 20, 0}; !bytes.Equal(f.cdbs[1], want) {
			t.Errorf("MODE SELECT CDB % x", f.cdbs[1])
		}
		// Header all zero (no block descriptors), PS cleared, only D_SENSE
		// changed.
		want := append(make([]byte, 8), 0x0a, 0x0a, 0x06, 0x00, 0x00, 0x00, 0xff, 0xff, 0x00, 0x00, 0x00, 0x1e)
		if !bytes.Equal(f.sel, want) {
			t.Errorf("MODE SELECT data\n got % x\nwant % x", f.sel, want)
		}
	}
}

func TestSetDescriptorSenseAlreadySet(t *testing.T) {
	f := &fakeTarget{sense: controlPageData(true, false)}
	changed, err := SetDescriptorSense(f.exec)
	if err != nil || changed || len(f.cdbs) != 1 {
		t.Fatalf("%v %v %d commands", changed, err, len(f.cdbs))
	}
}

func TestSetDescriptorSenseErrors(t *testing.T) {
	short := controlPageData(false, false)[:12]
	short[1] = 10
	wrongPage := controlPageData(false, false)
	wrongPage[8] = 0x08
	for name, tc := range map[string]struct {
		f    *fakeTarget
		want string
	}{
		"sense rejected":  {&fakeTarget{senseCC: illegalRequest}, "MODE SENSE(10), Control page: check condition: sense key 0x5, ASC/ASCQ 24/00"},
		"select rejected": {&fakeTarget{sense: controlPageData(false, false), selCC: []byte{0x72, 0x05, 0x26, 0x00}}, "MODE SELECT(10), D_SENSE=1: check condition: sense key 0x5, ASC/ASCQ 26/00"},
		"empty reply":     {&fakeTarget{sense: nil}, "no mode page"},
		"wrong page":      {&fakeTarget{sense: wrongPage}, "got page 0x8"},
		"truncated":       {&fakeTarget{sense: short}, "truncated"},
	} {
		_, err := SetDescriptorSense(tc.f.exec)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := controlPage([]byte{0, 1}); err == nil {
		t.Error("short header accepted")
	}
}

func TestSenseKey(t *testing.T) {
	for _, tc := range []struct {
		in             []byte
		key, asc, ascq byte
	}{
		{illegalRequest, 5, 0x24, 0},
		{[]byte{0x72, 0x02, 0x04, 0x01}, 2, 4, 1},
		{[]byte{0x70, 0, 5}, 0, 0, 0}, // too short for ASC
		{[]byte{0x7f}, 0, 0, 0},
		{nil, 0, 0, 0},
	} {
		if k, a, q := SenseKey(tc.in); k != tc.key || a != tc.asc || q != tc.ascq {
			t.Errorf("% x: %x %x %x", tc.in, k, a, q)
		}
	}
}

func FuzzControlPage(f *testing.F) {
	f.Add(controlPageData(false, false))
	f.Add(controlPageData(true, true))
	f.Fuzz(func(t *testing.T, b []byte) {
		page, err := controlPage(b)
		if err == nil && (len(page) < 3 || page[0]&0x7f != pageControl) {
			t.Fatalf("bad page accepted: % x", page)
		}
	})
}
