// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/rayy3535/cryptoerase/blockdev"
)

const chunk = 1 << 20 // marker size: 1 MiB, aligned for any logical block size

var (
	errMarkerWrite    = errors.New("marker write failed")
	errMarkerReadback = errors.New("marker read-back mismatch")
	zeroSum           = sha256.Sum256(make([]byte, chunk))
)

type markers struct {
	offsets []int64
	sums    [][32]byte
}

type verifyResult struct {
	total, changed, zero, unreadable int
}

func (v *verifyResult) add(o verifyResult) {
	v.total += o.total
	v.changed += o.changed
	v.zero += o.zero
	v.unreadable += o.unreadable
}

// sampleOffsets spreads n chunk-aligned offsets evenly over the device,
// including the first and the last chunk.
func sampleOffsets(size int64, n int) []int64 {
	total := size / chunk
	if total < 1 {
		return nil
	}
	if int64(n) > total {
		n = int(total)
	}
	if n == 1 {
		return []int64{0}
	}
	var out []int64
	seen := map[int64]bool{}
	for i := range n {
		idx := int64(i) * (total - 1) / int64(n-1)
		if !seen[idx] {
			seen[idx] = true
			out = append(out, idx*chunk)
		}
	}
	return out
}

func (r *runner) buffer() ([]byte, func(), error) {
	if r.opts.DisableDirectIO {
		return make([]byte, chunk), func() {}, nil
	}
	return blockdev.Buffer(chunk)
}

// writeMarkers writes random 1 MiB markers and reads each one back.
func (r *runner) writeMarkers(path string) (*markers, error) {
	dev, err := r.opts.OpenBlock(path, !r.opts.DisableDirectIO)
	if err != nil {
		return nil, fmt.Errorf("%w: open %s: %w", errMarkerWrite, path, err)
	}
	defer dev.Close()
	size, err := dev.Size()
	if err != nil {
		return nil, fmt.Errorf("%w: size of %s: %w", errMarkerWrite, path, err)
	}
	offs := sampleOffsets(size, r.opts.Samples)
	if len(offs) == 0 {
		return nil, fmt.Errorf("%w: %s is smaller than 1 MiB", errMarkerWrite, path)
	}
	buf, release, err := r.buffer()
	if err != nil {
		return nil, err
	}
	defer release()
	rb, release2, err := r.buffer()
	if err != nil {
		return nil, err
	}
	defer release2()
	m := &markers{}
	for _, off := range offs {
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(buf)
		if _, err := dev.WriteAt(buf, off); err != nil {
			return nil, fmt.Errorf("%w at offset %d: %w", errMarkerWrite, off, err)
		}
		if err := dev.Sync(); err != nil {
			return nil, fmt.Errorf("%w: sync: %w", errMarkerWrite, err)
		}
		if _, err := dev.ReadAt(rb, off); err != nil {
			return nil, fmt.Errorf("%w: read at %d: %w", errMarkerReadback, off, err)
		}
		if sha256.Sum256(rb) != sum {
			return nil, fmt.Errorf("%w at offset %d", errMarkerReadback, off)
		}
		m.offsets = append(m.offsets, off)
		m.sums = append(m.sums, sum)
	}
	return m, nil
}

// verifyMarkers reads every marker back after the erase.
func (r *runner) verifyMarkers(path string, m *markers) (verifyResult, error) {
	res := verifyResult{}
	dev, err := r.opts.OpenBlock(path, !r.opts.DisableDirectIO)
	if err != nil {
		res.total, res.unreadable = len(m.offsets), len(m.offsets)
		return res, err
	}
	defer dev.Close()
	if f, ok := dev.(interface{ FlushBuffers() error }); ok {
		_ = f.FlushBuffers()
	}
	buf, release, err := r.buffer()
	if err != nil {
		return res, err
	}
	defer release()
	for i, off := range m.offsets {
		res.total++
		if n, err := dev.ReadAt(buf, off); err != nil || n != chunk {
			res.unreadable++
			continue
		}
		s := sha256.Sum256(buf)
		if s != m.sums[i] {
			res.changed++
		}
		if s == zeroSum {
			res.zero++
		}
	}
	return res, nil
}

func (r *runner) verification(v verifyResult) *Verification {
	return &Verification{
		Method:         fmt.Sprintf("device completion status + %d x 1 MiB random markers per namespace/disk, written and read back before erase, read back after erase", r.opts.Samples),
		Samples:        v.total,
		Changed:        v.changed,
		ZeroAfterErase: v.zero,
		Unreadable:     v.unreadable,
	}
}

func verifiedResult(v verifyResult) (Result, string) {
	switch {
	case v.unreadable > 0:
		return Fail, fmt.Sprintf("%d marker region(s) unreadable after erase", v.unreadable)
	case v.total == 0 || v.changed != v.total:
		return Fail, fmt.Sprintf("%d of %d markers unchanged after erase", v.total-v.changed, v.total)
	}
	return Pass, ""
}
