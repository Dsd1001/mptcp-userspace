package multipath

import (
	"bytes"
	"testing"
)

// Reusable Secure Record storage must not retain application plaintext.
func TestSecureWriteFramesWipesPlaintextScratch(t *testing.T) {
	key, err := ParseKey(testToken)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newSecure(benchmarkSinkConn{}, key, []byte("release-v1.1.3-scratch-test"), true)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("v113-transient-sample-plaintext")
	if err := c.writeFrames([]frame{{kind: kindData, stream: 1, id: 1, data: secret}}); err != nil {
		t.Fatal(err)
	}
	if cap(c.recordScratch) == 0 {
		t.Fatal("missing reusable record allocation")
	}
	check := func() {
		t.Helper()
		if len(c.recordScratch) != 0 {
			t.Fatalf("record scratch len=%d", len(c.recordScratch))
		}
		raw := c.recordScratch[:cap(c.recordScratch)]
		if bytes.Contains(raw, secret) {
			t.Fatal("plaintext retained in reusable buffer")
		}
		for i, b := range raw {
			if b != 0 {
				t.Fatalf("record scratch not wiped at byte %d", i)
			}
		}
	}
	check()
	if err := c.writeFrames([]frame{{kind: kindData, stream: 1, id: 2, data: []byte("short")}}); err != nil {
		t.Fatal(err)
	}
	check()
}
