package multipath

import (
	"bytes"
	"math/bits"
)

func bitRange(lo, hi int) uint64 {
	return (^uint64(0) << uint(lo)) & (^uint64(0) >> uint(64-hi))
}

// Store a block while verifying every overlapping byte. Common new/full-word
// ranges avoid per-byte work; rare partial overlaps use contiguous set-bit runs.
// Validation of this page completes before any bytes or accounting are changed.
func (p *receivePage) store(start int, data []byte) (int, error) {
	end := start + len(data)
	overlapBytes := 0
	for at := start; at < end; {
		base := at / 64 * 64
		stop := min(end, base+64)
		mask := bitRange(at-base, stop-base)
		overlap := p.present[at/64] & mask
		overlapBytes += bits.OnesCount64(overlap)
		if overlap == mask {
			if !bytes.Equal(p.data[at:stop], data[at-start:stop-start]) {
				return 0, ErrProtocol
			}
		} else {
			for overlap != 0 {
				lo := bits.TrailingZeros64(overlap)
				count := bits.TrailingZeros64(^(overlap >> uint(lo)))
				hi := lo + count
				if !bytes.Equal(p.data[base+lo:base+hi], data[base+lo-start:base+hi-start]) {
					return 0, ErrProtocol
				}
				overlap &^= bitRange(lo, hi)
			}
		}
		at = stop
	}
	copy(p.data[start:end], data)
	for at := start; at < end; {
		base := at / 64 * 64
		stop := min(end, base+64)
		p.present[at/64] |= bitRange(at-base, stop-base)
		at = stop
	}
	added := len(data) - overlapBytes
	p.live += added
	return added, nil
}

func (p *receivePage) contiguous(start, limit int) int {
	at := start
	for at < limit {
		shift := at % 64
		count := min(64-shift, limit-at)
		available := min(count, bits.TrailingZeros64(^(p.present[at/64] >> uint(shift))))
		at += available
		if available < count {
			break
		}
	}
	return at - start
}

func (p *receivePage) clear(start, end int) {
	for start < end {
		base := start / 64 * 64
		stop := min(end, base+64)
		p.present[start/64] &^= bitRange(start-base, stop-base)
		start = stop
	}
}
