package multipath

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"
)

func TestMPX4StableFrozenCoreVectorSet(t *testing.T) {
	expected := []string{
		"carrier-generation.json",
		"close-ordering.json",
		"confirmation-validity.json",
		"error-scope.json",
		"frame-encoding.json",
		"handshake-ambiguity.json",
		"handshake-reject.json",
		"identity-lifecycle.json",
		"key-schedule.json",
		"max-carriers.json",
		"recovery-progress.json",
		"reordering-reliability.json",
		"secure-record.json",
		"session-lifecycle.json",
		"state-validity.json",
		"tcp-binding.json",
		"terminal-flow-control.json",
		"transmission-allocation.json",
		"varint.json",
		"version-compatibility.json",
	}
	entries, err := filepath.Glob("testdata/*.json")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, path := range entries {
		got = append(got, filepath.Base(path))
	}
	sort.Strings(got)
	sort.Strings(expected)
	if len(got) != len(expected) {
		t.Fatalf("Core vector count=%d want=%d: %v", len(got), len(expected), got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Fatalf("Core vector set mismatch at %d: got=%s want=%s", i, got[i], expected[i])
		}
		data, err := os.ReadFile(filepath.Join("testdata", got[i]))
		if err != nil {
			t.Fatal(err)
		}
		var meta struct {
			Protocol string `json:"protocol"`
			Revision string `json:"revision"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatal(err)
		}
		if meta.Protocol != "MPX/4" || meta.Revision != "Draft 11" {
			t.Fatalf("%s metadata=%+v", got[i], meta)
		}
	}
}

func TestMPX4StableHandshakeRejectOfficialEncodings(t *testing.T) {
	cases := []struct {
		code uint64
		wire string
	}{
		{mpx4ErrInternal, "060101"},
		{mpx4ErrProtocolViolation, "060102"},
		{mpx4ErrAuthenticationFailed, "060103"},
		{mpx4ErrResourceLimit, "060105"},
		{mpx4ErrSessionNotFound, "060106"},
		{mpx4ErrSessionConflict, "060107"},
		{mpx4ErrCarrierConflict, "06010c"},
		{mpx4ErrUnsupportedParameter, "06010d"},
	}
	for _, tc := range cases {
		if !validCoreHandshakeRejectCode(tc.code) {
			t.Fatalf("allowed code rejected: 0x%x", tc.code)
		}
		wire, err := encodeHandshakeCodeMessage(mpx4HSHandshakeReject, tc.code)
		if err != nil {
			t.Fatal(err)
		}
		if got := hex.EncodeToString(wire); got != tc.wire {
			t.Fatalf("HANDSHAKE_REJECT 0x%x wire=%s want=%s", tc.code, got, tc.wire)
		}
	}
	for _, code := range []uint64{mpx4ErrNoError, mpx4ErrVersionUnsupported, mpx4ErrStreamLimit, mpx4ErrFlowControl, mpx4ErrFrameEncoding, mpx4ErrStreamState, mpx4ErrFinalSize, mpx4ErrTransmissionID} {
		if validCoreHandshakeRejectCode(code) {
			t.Fatalf("invalid Core HANDSHAKE_REJECT code allowed: 0x%x", code)
		}
		if err := handshakeRejectError(code); !errors.Is(err, ErrProtocol) {
			t.Fatalf("invalid Core HANDSHAKE_REJECT code 0x%x error=%v", code, err)
		}
	}
	if err := handshakeRejectError(0x40); err == nil {
		t.Fatal("unknown extension HANDSHAKE_REJECT code did not terminate candidate")
	}
}

func TestMPX4StableVersionNegotiationEncodingAndValidation(t *testing.T) {
	wire, err := encodeVersionNegotiation([]uint64{5, 4, 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(wire); got != "050403050403" {
		t.Fatalf("VERSION_NEGOTIATION wire=%s", got)
	}
	versions, err := parseVersionNegotiation([]byte{3, 5, 4, 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 3 || versions[0] != 5 || versions[1] != 4 || versions[2] != 3 {
		t.Fatalf("versions=%v", versions)
	}
	for _, bad := range [][]uint64{{}, {4, 4}, {4, 5}, {4, 0}} {
		if _, err := encodeVersionNegotiation(bad); !errors.Is(err, ErrProtocol) {
			t.Fatalf("invalid versions accepted: %v err=%v", bad, err)
		}
	}
	for _, bad := range [][]byte{{0}, {2, 4}, {2, 4, 4}, {2, 4, 5}, {1, 0}, {1, 4, 3}} {
		if _, err := parseVersionNegotiation(bad); !errors.Is(err, ErrProtocol) {
			t.Fatalf("invalid VERSION_NEGOTIATION body accepted: %x err=%v", bad, err)
		}
	}
}

func TestMPX4StableUnsupportedPrefaceGetsVersionNegotiation(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 32)
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	done := make(chan error, 1)
	go func() {
		_, err := readHandshake(server, key)
		done <- err
	}()

	if err := client.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte{'M', 'P', 'X', 0, 5}); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(client)
	typ, body, _, err := readHandshakeMessage(r)
	if err != nil {
		t.Fatal(err)
	}
	if typ != mpx4HSVersionNegotiation {
		t.Fatalf("message type=0x%x", typ)
	}
	versions, err := parseVersionNegotiation(body)
	if err != nil || len(versions) != 1 || versions[0] != WireProtocol {
		t.Fatalf("VERSION_NEGOTIATION versions=%v err=%v", versions, err)
	}
	if err := <-done; !errors.Is(err, ErrProtocol) {
		t.Fatalf("server unsupported-version result=%v", err)
	}
}

func TestMPX4StableUnknownCriticalParameterGetsHandshakeReject(t *testing.T) {
	key := bytes.Repeat([]byte{0x22}, 32)
	var p handshakeParams
	p.sid[0] = 1
	p.carrier = 1
	p.maxCarriers = MaxCarriers
	p.clientNonce[0] = 1
	body, err := encodeClientParams(p)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := encodeParam(0x41, 1, []byte{1})
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, unknown...)
	init, err := encodeHandshakeMessage(mpx4HSClientInit, body)
	if err != nil {
		t.Fatal(err)
	}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		_, err := readHandshake(server, key)
		done <- err
	}()

	if err := client.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(append([]byte{'M', 'P', 'X', 0, 4}, init...)); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(client)
	typ, rejectBody, _, err := readHandshakeMessage(r)
	if err != nil {
		t.Fatal(err)
	}
	code, err := decodeHandshakeCode(rejectBody)
	if err != nil {
		t.Fatal(err)
	}
	if typ != mpx4HSHandshakeReject || code != mpx4ErrUnsupportedParameter {
		t.Fatalf("reject type=0x%x code=0x%x", typ, code)
	}
	var failure *mpx4Failure
	if err := <-done; !errors.As(err, &failure) || failure.code != mpx4ErrUnsupportedParameter {
		t.Fatalf("server error=%v", err)
	}
}

func TestMPX4StableMaxCarriersOfficialParameterVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/max-carriers.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		EncodingCases []struct {
			Value        string `json:"value"`
			ParameterHex string `json:"parameter_hex"`
		} `json:"encoding_cases"`
	}
	if err := json.Unmarshal(data, &vector); err != nil {
		t.Fatal(err)
	}
	if len(vector.EncodingCases) == 0 {
		t.Fatal("no MAX_CARRIERS encoding cases")
	}
	for _, tc := range vector.EncodingCases {
		value, err := strconv.ParseUint(tc.Value, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		v, err := paramVarInt(value)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := encodeParam(mpx4ParamMaxCarriers, 1, v)
		if err != nil {
			t.Fatal(err)
		}
		if got := hex.EncodeToString(wire); got != tc.ParameterHex {
			t.Fatalf("MAX_CARRIERS %s wire=%s want=%s", tc.Value, got, tc.ParameterHex)
		}
	}
}

func TestMPX4StableCapacityHintOfficialExtensionVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/extensions/capacity-hint.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		EncodingCases []struct {
			Units        string `json:"receive_capacity_units"`
			ParameterHex string `json:"parameter_hex"`
		} `json:"encoding_cases"`
	}
	if err := json.Unmarshal(data, &vector); err != nil {
		t.Fatal(err)
	}
	for _, tc := range vector.EncodingCases {
		value, err := strconv.ParseUint(tc.Units, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		v, _ := paramVarInt(value)
		wire, err := encodeParam(mpx4ParamReceiveCapacityHint, 0, v)
		if err != nil {
			t.Fatal(err)
		}
		if got := hex.EncodeToString(wire); got != tc.ParameterHex {
			t.Fatalf("RECEIVE_CAPACITY_HINT %s wire=%s want=%s", tc.Units, got, tc.ParameterHex)
		}
	}

	var p handshakeParams
	p.sid[0] = 1
	p.carrier = 1
	p.maxCarriers = MaxCarriers
	p.clientNonce[0] = 1
	p.hasReceiveCapacityHint = true
	for _, invalid := range []uint64{0, 65536} {
		p.receiveCapacityHint = invalid
		if _, err := encodeClientParams(p); !errors.Is(err, ErrProtocol) {
			t.Fatalf("invalid capacity hint %d accepted: %v", invalid, err)
		}
	}
	p.hasReceiveCapacityHint = false
	body, err := encodeClientParams(p)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := paramVarInt(96)
	critical, _ := encodeParam(mpx4ParamReceiveCapacityHint, 1, v)
	if _, err := parseParams(append(body, critical...), true); !errors.Is(err, ErrProtocol) {
		t.Fatalf("critical RECEIVE_CAPACITY_HINT accepted: %v", err)
	}
}
