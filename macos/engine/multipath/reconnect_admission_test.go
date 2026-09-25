package multipath

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"
)

// These six local relays deliberately share one source IP at the Landing.
// Actual production relays may not share a quota. This distinguishes a real
// reconnect-induced limiter event from a logical-stream admission failure.
func TestSixCarrierReconnectAdmissionPreservesLogicalSession(t *testing.T) {
	s, srv, relays, _ := testTCP(t, 6, 32<<20, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.WaitPaths(ctx, 6); err != nil {
		t.Fatal(err)
	}
	st, err := s.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.SetDeadline(time.Now().Add(20 * time.Second))
	before := srv.AdmissionSnapshot()
	for round := 0; round < 6; round++ {
		for _, r := range relays {
			r.fail()
		}
		time.Sleep(10 * time.Millisecond)
		for _, r := range relays {
			r.recover()
		}
		if err = s.WaitPaths(ctx, 6); err != nil {
			t.Fatal(err)
		}
		data := []byte(fmt.Sprintf("same-logical-stream-after-reconnect-%d", round))
		if _, err = st.Write(data); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(data))
		if _, err = io.ReadFull(st, got); err != nil || !bytes.Equal(data, got) {
			t.Fatalf("logical flow did not survive: %v", err)
		}
		if s.Snapshot().Lifecycle.Closed {
			t.Fatal("reconnect admission terminated session")
		}
	}
	if err = st.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(st); err != nil {
		t.Fatal(err)
	}
	st.Close()
	after := srv.AdmissionSnapshot()
	if after.Accepted <= before.Accepted || s.Snapshot().Paths != 6 {
		t.Fatal("no authenticated reconnection or paths not recovered")
	}
	for _, r := range s.Snapshot().Resources.Rejections {
		if r != 0 {
			t.Fatal("reconnect pressure became stream resource refusal")
		}
	}
	if after.Active > handshakeGlobalLimit {
		t.Fatal("global admission bound exceeded")
	}
	for _, p := range after.SourceDetails {
		if p.Active > handshakeSourceLimit {
			t.Fatal("source admission bound exceeded")
		}
	}
	report, _ := json.Marshal(map[string]any{"scope": "Controlled six-relay same-source reconnect injection; not attribution of historical accepted49/rejected141", "before": before, "after": after, "client": s.Snapshot()})
	t.Logf("RECONNECT_ADMISSION %s", report)
}
