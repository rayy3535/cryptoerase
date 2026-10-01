// SPDX-License-Identifier: Apache-2.0

package tcg

import "testing"

func FuzzParseLevel0(f *testing.F) {
	f.Add(discovery(feature(0x0203, make([]byte, 16)...), feature(0x0002, 0x0f)))
	f.Add(make([]byte, 48))
	f.Add([]byte{0, 0, 0, 0xff})
	f.Fuzz(func(t *testing.T, b []byte) {
		l, err := ParseLevel0(b)
		if err != nil {
			return
		}
		if !l.HasLockingFeature && (l.Locked || l.LockingEnabled || l.MediaEncryption) {
			t.Fatal("locking flags without a locking feature")
		}
	})
}
