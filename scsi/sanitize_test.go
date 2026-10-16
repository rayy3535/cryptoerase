// SPDX-License-Identifier: Apache-2.0

package scsi

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// fixedSense builds fixed-format sense data, with a progress indication
// when progress >= 0.
func fixedSense(key, asc, ascq byte, progress int) []byte {
	s := make([]byte, 18)
	s[0], s[2], s[7], s[12], s[13] = 0x70, key, 10, asc, ascq
	if progress >= 0 {
		s[15] = 0x80
		s[16], s[17] = byte(progress>>8), byte(progress)
	}
	return s
}

func TestCommandSupported(t *testing.T) {
	for _, tc := range []struct {
		support byte
		err     error
		want    Support
	}{
		{0x03, nil, SupportYes},
		{0x05, nil, SupportYes},
		{0x01, nil, SupportNo},
		{0x00, nil, SupportUnknown},
		{0, &CheckCondition{Sense: illegalRequest}, SupportUnknown}, // 24/00
		{0, &CheckCondition{Sense: []byte{0x72, 0x05, 0x20, 0x00}}, SupportUnknown}, // invalid opcode
	} {
		var cdb []byte
		got, err := CryptoEraseSupported(func(c []byte, dir int, data []byte) error {
			cdb = append([]byte(nil), c...)
			if dir != DirFromDevice {
				t.Errorf("direction %d", dir)
			}
			if tc.err != nil {
				return tc.err
			}
			data[1] = tc.support
			return nil
		})
		if err != nil || got != tc.want {
			t.Errorf("support %#x / %v: %v %v", tc.support, tc.err, got, err)
		}
		if want := []byte{0xa3, 0x0c, 0x02, 0x48, 0x00, 0x03, 0, 0, 0, 64, 0, 0}; !bytes.Equal(cdb, want) {
			t.Errorf("CDB % x", cdb)
		}
	}
	// Other errors are errors.
	_, err := CryptoEraseSupported(func([]byte, int, []byte) error {
		return &CheckCondition{Sense: fixedSense(0x02, 0x04, 0x00, -1)}
	})
	if err == nil || !strings.Contains(err.Error(), "REPORT SUPPORTED OPERATION CODES") {
		t.Fatalf("%v", err)
	}
	if SupportYes.String() != "supported" || SupportNo.String() != "not supported" || SupportUnknown.String() != "unknown" {
		t.Error("Support.String")
	}
}

func TestStartSanitize(t *testing.T) {
	var cdb []byte
	err := StartSanitize(func(c []byte, dir int, data []byte) error {
		cdb = c
		if dir != DirNone || data != nil {
			t.Errorf("transfer %d %v", dir, data)
		}
		return nil
	}, SanitizeCryptoErase)
	// IMMED set, AUSE clear, service action 03h, no parameter list.
	if err != nil || !bytes.Equal(cdb, []byte{0x48, 0x83, 0, 0, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("%v % x", err, cdb)
	}
	err = StartSanitize(func([]byte, int, []byte) error { return errors.New("SG_IO: EIO") }, SanitizeCryptoErase)
	if err == nil || !strings.HasPrefix(err.Error(), "SANITIZE: ") {
		t.Fatalf("%v", err)
	}
}

func TestParseSanitizeStatus(t *testing.T) {
	desc := []byte{0x72, 0x02, 0x04, 0x1b, 0, 0, 0, 8, 0x02, 0x06, 0, 0, 0x80, 0x40, 0x00, 0}
	for _, tc := range []struct {
		name     string
		sense    []byte
		state    string
		progress int
	}{
		{"in progress, fixed", fixedSense(0x02, 0x04, 0x1b, 0x8000), SanitizeInProgress, 0x8000},
		{"in progress, descriptor", desc, SanitizeInProgress, 0x4000},
		{"in progress, no indication", fixedSense(0x02, 0x04, 0x1b, -1), SanitizeInProgress, 0},
		{"failed", fixedSense(0x03, 0x31, 0x03, -1), SanitizeFailed, 0},
		{"idle", fixedSense(0x00, 0x00, 0x00, -1), SanitizeIdle, 0},
		{"recovered", fixedSense(0x01, 0x00, 0x00, -1), SanitizeIdle, 0},
		{"unit attention", fixedSense(0x06, 0x29, 0x00, -1), SanitizeOther, 0},
		{"not ready, other", fixedSense(0x02, 0x04, 0x02, -1), SanitizeOther, 0},
		{"zeroed buffer", make([]byte, 18), SanitizeOther, 0},
		{"empty", nil, SanitizeOther, 0},
	} {
		st := ParseSanitizeStatus(tc.sense)
		if st.State != tc.state || st.Progress != tc.progress || st.InProgress() != (tc.state == SanitizeInProgress) {
			t.Errorf("%s: %+v", tc.name, st)
		}
	}
	if st := ParseSanitizeStatus(fixedSense(0x02, 0x04, 0x1b, 1)); st.Sense != "2/04/1b" {
		t.Errorf("sense %q", st.Sense)
	}
}

func TestSanitizeProgress(t *testing.T) {
	st, err := SanitizeProgress(func(cdb []byte, dir int, data []byte) error {
		if cdb[0] != 0x03 || cdb[1] != 0 || int(cdb[4]) != len(data) {
			t.Errorf("CDB % x", cdb)
		}
		copy(data, fixedSense(0x02, 0x04, 0x1b, 0x1234))
		return nil
	})
	if err != nil || !st.InProgress() || st.Progress != 0x1234 {
		t.Fatalf("%v %+v", err, st)
	}
	if _, err := SanitizeProgress(func([]byte, int, []byte) error { return errors.New("SG_IO: EIO") }); err == nil {
		t.Fatal("error lost")
	}
}

func FuzzParseSanitizeStatus(f *testing.F) {
	f.Add(fixedSense(0x02, 0x04, 0x1b, 0x8000))
	f.Add([]byte{0x72, 0x02, 0x04, 0x1b, 0, 0, 0, 8, 0x02, 0x06, 0, 0, 0x80, 0x40, 0x00, 0})
	f.Fuzz(func(t *testing.T, b []byte) {
		st := ParseSanitizeStatus(b)
		if st.Progress < 0 || st.Progress > 0xffff || st.State == "" {
			t.Fatalf("%+v", st)
		}
	})
}
