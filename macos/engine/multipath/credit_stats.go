package multipath

import "time"

// Rev4 exposes actual offset commitment, historic WINDOW entitlement and
// physical page accounting separately. Cumulative counters never imply RAM.
type SharedCreditStats struct {
	CapabilityRevision int                   `json:"capability_revision"`
	CreditAccounting   string                `json:"credit_accounting"`
	TXCommitted        uint64                `json:"session_tx_committed"`
	PeerConsumed       uint64                `json:"session_peer_consumed"`
	PeerLimit          uint64                `json:"session_peer_limit"`
	RXCommitted        uint64                `json:"session_rx_committed"`
	RXConsumed         uint64                `json:"session_rx_consumed"`
	RXLimit            uint64                `json:"session_rx_limit"`
	TXUsed             int                   `json:"session_tx_unconsumed_bytes"`
	TXBootstrap        int                   `json:"session_tx_bootstrap_bytes"`
	TXGrowth           int                   `json:"session_tx_growth_bytes"`
	ClosingStreams     int                   `json:"closing_streams"`
	OccupiedSlots      int                   `json:"occupied_stream_slots"`
	TerminalStreams    int                   `json:"terminal_stream_records"`
	ReceivePages       int                   `json:"receive_page_count"`
	WindowEntitlements uint64                `json:"stream_window_entitlement_bytes"`
	IdleActualData     int                   `json:"idle_actual_data_bytes"`
	ActualRXPeak       int                   `json:"session_rx_actual_peak_bytes"`
	ActualTXPeak       int                   `json:"session_tx_actual_peak_bytes"`
	ActualGrowthPeak   int                   `json:"session_rx_growth_peak_bytes"`
	WriteWaits         map[string]creditWait `json:"write_wait_reasons"`
}

func (s *Session) sharedCreditSnapshotLocked() SharedCreditStats {
	fc := &s.credit
	r := SharedCreditStats{
		CapabilityRevision: CapabilityRevision, CreditAccounting: "rev4_actual_offset",
		TXCommitted: fc.txCommitted, PeerConsumed: fc.peerConsumed, PeerLimit: fc.peerLimit,
		RXCommitted: fc.rxCommitted, RXConsumed: fc.rxConsumed, RXLimit: fc.rxLimit,
		TXUsed: fc.txUsed, TXBootstrap: fc.txUsed - fc.txGrowth, TXGrowth: fc.txGrowth,
		ClosingStreams: len(s.closing), OccupiedSlots: len(s.streams) + len(s.closing), TerminalStreams: len(s.terminal),
		ReceivePages: s.receiveAllocated / receivePageCost, ActualRXPeak: fc.peakRX, ActualTXPeak: fc.peakTX, ActualGrowthPeak: fc.peakGrowth,
		WriteWaits: make(map[string]creditWait, len(fc.waits)-1),
	}
	for i := 1; i < len(fc.waits); i++ {
		r.WriteWaits[creditWaitNames[i]] = fc.waits[i]
	}
	now := time.Now()
	for _, st := range s.streams {
		if st.rxLimit >= st.rxRead {
			r.WindowEntitlements += st.rxLimit - st.rxRead
		}
		if now.Sub(st.lastRead) > creditIdle {
			r.IdleActualData += int(st.rxHigh - st.rxRead)
		}
	}
	return r
}
