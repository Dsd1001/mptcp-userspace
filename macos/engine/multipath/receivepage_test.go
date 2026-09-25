package multipath

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"
)

func TestPageWordRangesAndConflict(t *testing.T) {
	for start := 0; start < 130; start++ {
		for _, size := range []int{1, 2, 63, 64, 65, 129, MaxPayload - 130} {
			var p receivePage
			data := bytes.Repeat([]byte{byte(start + size)}, size)
			added, err := p.store(start, data)
			if err != nil || added != size || p.live != size || p.contiguous(start, start+size) != size {
				t.Fatalf("insert %d/%d: %d %v", start, size, added, err)
			}
			added, err = p.store(start, data)
			if err != nil || added != 0 || p.live != size {
				t.Fatal("duplicate charged twice")
			}
			data[size/2] ^= 1
			if _, err = p.store(start, data); !errors.Is(err, ErrProtocol) {
				t.Fatal("conflicting overlap accepted")
			}
			p.clear(start, start+size)
			if p.contiguous(start, start+size) != 0 {
				t.Fatal("word clear retained bits")
			}
		}
	}
}

func TestPageRandomOverlapsAgainstByteModel(t *testing.T) {
	rng := rand.New(rand.NewSource(713))
	var p receivePage
	gold := make([]byte, MaxPayload)
	seen := make([]bool, MaxPayload)
	payload := make([]byte, MaxPayload)
	rng.Read(payload)
	live := 0
	for i := 0; i < 5000; i++ {
		start := rng.Intn(MaxPayload)
		end := min(MaxPayload, start+1+rng.Intn(2048))
		if i%7 == 0 {
			p.clear(start, end)
			for at := start; at < end; at++ {
				if seen[at] {
					seen[at] = false
					live--
					p.live--
				}
			}
		} else {
			added, err := p.store(start, payload[start:end])
			if err != nil {
				t.Fatal(err)
			}
			expected := 0
			for at := start; at < end; at++ {
				if !seen[at] {
					seen[at] = true
					live++
					expected++
				}
				gold[at] = payload[at]
			}
			if added != expected || p.live != live {
				t.Fatalf("accounting: %d/%d live=%d/%d", added, expected, p.live, live)
			}
		}
		begin := rng.Intn(MaxPayload)
		want := 0
		for at := begin; at < MaxPayload && seen[at]; at++ {
			want++
		}
		if p.contiguous(begin, MaxPayload) != want {
			t.Fatal("word prefix differs from byte model")
		}
	}
	for i := range gold {
		if seen[i] && gold[i] != p.data[i] {
			t.Fatal("bytes changed")
		}
	}
}

func FuzzReceivePageOverlap(f *testing.F) {
	f.Add([]byte{0, 63, 64, 127, 255, 1, 2})
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) < 2 || len(input) > 4096 {
			return
		}
		var p receivePage
		data := bytes.Repeat([]byte{7}, 512)
		for i := 1; i < len(input); i++ {
			start := int(input[i-1])
			n := 1 + int(input[i])
			if _, err := p.store(start, data[:n]); err != nil {
				t.Fatal(err)
			}
			if _, err := p.store(start, data[:n]); err != nil {
				t.Fatal(err)
			}
			bad := append([]byte(nil), data[:n]...)
			bad[n/2] ^= 1
			if _, err := p.store(start, bad); !errors.Is(err, ErrProtocol) {
				t.Fatal("overlap conflict missed")
			}
		}
	})
}
