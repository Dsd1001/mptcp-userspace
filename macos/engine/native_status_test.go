package main

import (
	"encoding/binary"
	"testing"
)

func TestClosedSocketDoesNotMaskLiveCounts(t *testing.T) {
	for _, test := range []struct {
		counts []int
		want   int
	}{
		{[]int{2, -2, 3}, 2}, {[]int{-2, -2}, 0}, {[]int{2, -1}, -1}, {nil, 0},
	} {
		if got := minimumPathCount(test.counts); got != test.want {
			t.Fatalf("%v: got %d want %d", test.counts, got, test.want)
		}
	}
}

func TestSessionFlags(t *testing.T) {
	for _, test := range []struct {
		flags         uint32
		ready, failed bool
	}{
		{0x1102, true, false},
		{0x1101, false, false},
		{0, false, false},
		{0x1402, false, true},
		{0x102, false, true},
	} {
		ready, err := readyFlags(test.flags)
		if ready != test.ready || (err != nil) != test.failed {
			t.Fatalf("flags %#x: ready=%v err=%v", test.flags, ready, err)
		}
	}
}

func flowRecord(tuple flowTuple, flags uint32) []byte {
	flow := make([]byte, 304)
	binary.LittleEndian.PutUint64(flow[:8], uint64(len(flow)))
	binary.LittleEndian.PutUint64(flow[8:16], 292)
	binary.LittleEndian.PutUint32(flow[16:20], flags)
	flow[24] = 16
	flow[25] = 2
	flow[152] = 16
	flow[153] = 2
	copy(flow[28:32], tuple[0:4])
	copy(flow[26:28], tuple[4:6])
	copy(flow[156:160], tuple[6:10])
	copy(flow[154:156], tuple[10:12])
	return flow
}
func sessionRecord(flows ...[]byte) []byte {
	record := make([]byte, 24)
	binary.LittleEndian.PutUint64(record[8:16], 24)
	binary.LittleEndian.PutUint64(record[16:24], uint64(len(flows)))
	for _, flow := range flows {
		record = append(record, flow...)
	}
	binary.LittleEndian.PutUint64(record[:8], uint64(len(record)))
	return record
}
func TestReadySubflowsBelongToSocket(t *testing.T) {
	own := flowTuple{192, 0, 2, 100, 0x90, 1, 198, 51, 100, 1, 0x4e, 0x20}
	second := own
	second[9] = 2
	unrelated := own
	unrelated[5] = 2
	data := append(sessionRecord(flowRecord(unrelated, 0xc8)), sessionRecord(flowRecord(own, 0xc8), flowRecord(second, 0xc8))...)
	n, err := readySubflows(data, own)
	if err != nil || n != 2 {
		t.Fatalf("ready=%d err=%v", n, err)
	}
	data = sessionRecord(flowRecord(own, 0xc8), flowRecord(second, 0x40))
	n, err = readySubflows(data, own)
	if err != nil || n != 1 {
		t.Fatalf("pending subflow counted: %d %v", n, err)
	}
	data = sessionRecord(flowRecord(own, 0xc8), flowRecord(second, 0x1c8))
	n, err = readySubflows(data, own)
	if err != nil || n != 1 {
		t.Fatalf("degraded subflow counted: %d %v", n, err)
	}
}
func TestSubflowRecordsRejectMalformedLengths(t *testing.T) {
	own := flowTuple{192, 0, 2, 100, 0x90, 1, 198, 51, 100, 1, 0x4e, 0x20}
	good := sessionRecord(flowRecord(own, 0xc8))
	for _, size := range []int{1, 23, 40, len(good) - 1} {
		if _, err := readySubflows(good[:size], own); err == nil {
			t.Fatalf("truncation %d accepted", size)
		}
	}
	bad := append([]byte(nil), good...)
	binary.LittleEndian.PutUint64(bad[:8], 0)
	if _, err := readySubflows(bad, own); err == nil {
		t.Fatal("zero record length accepted")
	}
	bad = append([]byte(nil), good...)
	binary.LittleEndian.PutUint64(bad[24:32], 0)
	if _, err := readySubflows(bad, own); err == nil {
		t.Fatal("zero subflow length accepted")
	}
}

func TestIdlePrimaryAndQueuedJoins(t *testing.T) {
	own := flowTuple{192, 0, 2, 1, 1, 2, 198, 51, 100, 1, 3, 4}
	// Observed on Darwin: a negotiated idle primary with three queued JOINs.
	data := sessionRecord(flowRecord(own, 0x46248), flowRecord(flowTuple{}, 0x6), flowRecord(flowTuple{}, 0x6), flowRecord(flowTuple{}, 0x6))
	n, err := readySubflows(data, own)
	if err != nil || n != 1 {
		t.Fatalf("idle primary/queued JOINs: count=%d err=%v", n, err)
	}
	for _, flags := range []uint32{0xc8 | 0x10, 0xc8 | 0x20, 0x8, 0x148} {
		n, err := readySubflows(sessionRecord(flowRecord(own, flags)), own)
		if err != nil || n != 0 {
			t.Fatalf("inactive flags %#x counted: %d %v", flags, n, err)
		}
	}
}
