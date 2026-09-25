package multipath

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

// Only calibrates the shaped relay fixture. Parallel sockets here are NOT
// reported as single-stream MPX aggregation results.
func TestHighBDPRelayCapacity(t *testing.T) {
	if os.Getenv("MPX_RELAY_CALIBRATE") == "" {
		t.Skip("opt-in fixture calibration")
	}
	backend, _ := echoBackend(t)
	const paths = 6
	const size = 16 << 20
	payload := bytes.Repeat([]byte("fixture-calibration\x00\xff"), 1024)
	results := make(chan error, paths)
	ready := make(chan struct{})
	for i := 0; i < paths; i++ {
		addr := highBDPRelay(t, backend, 6250000, 15*time.Millisecond)
		c, err := PlainDial(context.Background(), addr)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			defer c.Close()
			c.SetDeadline(time.Now().Add(30 * time.Second))
			<-ready
			sent := make(chan error, 1)
			want := sha256.New()
			go func() {
				for pos := 0; pos < size; {
					n := min(size-pos, len(payload))
					want.Write(payload[:n])
					if err := writeAll(c, payload[:n]); err != nil {
						sent <- err
						return
					}
					pos += n
				}
				sent <- nil
			}()
			got := sha256.New()
			n, err := io.CopyN(got, c, size)
			if err == nil {
				err = <-sent
			}
			if err == nil && (n != size || !bytes.Equal(want.Sum(nil), got.Sum(nil))) {
				err = fmt.Errorf("fixture bytes changed")
			}
			results <- err
		}()
	}
	started := time.Now()
	close(ready)
	for i := 0; i < paths; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	mbps := float64(paths*size*8) / time.Since(started).Seconds() / 1e6
	t.Logf("FIXTURE_ONLY six independent raw TCP echo sockets: %.3f Mbps of configured 300 Mbps", mbps)
	if mbps < 270 {
		t.Fatalf("fixture itself does not reach 90 percent capacity: %.3f", mbps)
	}
}
