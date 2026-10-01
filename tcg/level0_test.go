// SPDX-License-Identifier: Apache-2.0

package tcg

import (
	"encoding/binary"
	"testing"
)

func discovery(features ...[]byte) []byte {
	body := []byte{}
	for _, f := range features {
		body = append(body, f...)
	}
	b := make([]byte, 48, 48+len(body)+16)
	b = append(b, body...)
	binary.BigEndian.PutUint32(b[0:4], uint32(len(b)-4))
	return append(b, make([]byte, 16)...) // trailing padding is ignored
}

func feature(code uint16, data ...byte) []byte {
	f := []byte{byte(code >> 8), byte(code), 0x10, byte(len(data))}
	return append(f, data...)
}

func TestParseLevel0(t *testing.T) {
	b := discovery(
		feature(0x0001, make([]byte, 12)...),                          // TPer
		feature(0x0002, append([]byte{0x0b}, make([]byte, 11)...)...), // supported, enabled, media encryption
		feature(0x0203, make([]byte, 16)...),                          // Opal 2
	)
	d, err := ParseLevel0(b)
	if err != nil {
		t.Fatal(err)
	}
	if !d.HasLockingFeature || !d.LockingSupported || !d.LockingEnabled || d.Locked || !d.MediaEncryption {
		t.Errorf("locking decode: %+v", d)
	}
	if len(d.SSC) != 1 || d.SSC[0] != "Opal 2" {
		t.Errorf("ssc %v", d.SSC)
	}
}

func TestParseLevel0Locked(t *testing.T) {
	d, err := ParseLevel0(discovery(feature(0x0002, 0x0f)))
	if err != nil || !d.Locked {
		t.Fatalf("%v %+v", err, d)
	}
}

func TestParseLevel0Short(t *testing.T) {
	if _, err := ParseLevel0(make([]byte, 10)); err == nil {
		t.Fatal("expected error")
	}
	// truncated descriptor must not panic
	b := discovery(feature(0x0002, 0x01, 0, 0, 0))
	b = b[:len(b)-16-2]
	binary.BigEndian.PutUint32(b[0:4], 200)
	if _, err := ParseLevel0(b); err != nil {
		t.Fatal(err)
	}
}
