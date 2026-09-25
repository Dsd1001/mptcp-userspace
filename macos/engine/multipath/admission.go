package multipath

import (
	"net"
	"net/netip"
	"sort"
	"time"
)

const (
	handshakeGlobalLimit = 64
	handshakeSourceLimit = 8
	handshakeSourceMax   = 4096
	handshakeBurst       = 16.0
	handshakeRate        = 8.0
)

type handshakeSource struct {
	active                     int
	tokens                     float64
	updated                    time.Time
	accepted, rejected         uint64
	lastReason, lastRejectedAt string
}
type SourceAdmissionStats struct {
	Source         string `json:"source"`
	Active         int    `json:"active"`
	Accepted       uint64 `json:"accepted"`
	Rejected       uint64 `json:"rejected"`
	LastReason     string `json:"last_reason,omitempty"`
	LastRejectedAt string `json:"last_rejected_at,omitempty"`
}
type AdmissionStats struct {
	Active                 int                    `json:"active"`
	Sources                int                    `json:"source_entries"`
	Accepted               uint64                 `json:"accepted"`
	Rejected               uint64                 `json:"rejected"`
	RejectionReasons       map[string]uint64      `json:"rejection_reasons"`
	HandshakeOutcomes      map[string]uint64      `json:"handshake_outcomes"`
	AcceptRetries          uint64                 `json:"accept_resource_retries"`
	LastAcceptError        string                 `json:"last_accept_error,omitempty"`
	GlobalLimit            int                    `json:"global_concurrency_limit"`
	SourceLimit            int                    `json:"source_concurrency_limit"`
	SourceRate             float64                `json:"source_attempts_per_second"`
	SourceBurst            float64                `json:"source_burst"`
	SourceDetails          []SourceAdmissionStats `json:"source_details,omitempty"`
	SourceDetailsTruncated bool                   `json:"source_details_truncated,omitempty"`
}

func handshakeSourceKey(address net.Addr) string {
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return "unknown"
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	ip = ip.Unmap()
	if ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().String()
	}
	return ip.String()
}
func (srv *Server) handshakeOutcomeLocked(reason string) {
	if srv.handshakeOutcomes == nil {
		srv.handshakeOutcomes = make(map[string]uint64)
	}
	srv.handshakeOutcomes[reason]++
}

// Limits are unchanged from 0.7.0; diagnostics distinguish admission from
// authentication. An accepted slot is NOT proof of a successful PSK handshake.
func (srv *Server) admitLocked(source string, now time.Time) bool {
	reject := func(reason string) bool {
		srv.admissionRejected++
		if srv.admissionReasons == nil {
			srv.admissionReasons = make(map[string]uint64)
		}
		srv.admissionReasons[reason]++
		if b := srv.sources[source]; b != nil {
			b.rejected++
			b.lastReason = reason
			b.lastRejectedAt = now.UTC().Format(time.RFC3339Nano)
		}
		return false
	}
	if len(srv.handshakes) >= handshakeGlobalLimit {
		return reject("global_concurrency")
	}
	if srv.sources == nil {
		srv.sources = make(map[string]*handshakeSource)
	}
	bucket := srv.sources[source]
	if bucket == nil {
		if len(srv.sources) >= handshakeSourceMax {
			for key, item := range srv.sources {
				if item.active == 0 && now.Sub(item.updated) > time.Minute {
					delete(srv.sources, key)
				}
			}
		}
		if len(srv.sources) >= handshakeSourceMax {
			return reject("source_table")
		}
		bucket = &handshakeSource{tokens: handshakeBurst, updated: now}
		srv.sources[source] = bucket
	}
	bucket.tokens = min(handshakeBurst, bucket.tokens+max(0, now.Sub(bucket.updated).Seconds())*handshakeRate)
	bucket.updated = now
	if bucket.active >= handshakeSourceLimit {
		return reject("source_concurrency")
	}
	if bucket.tokens < 1 {
		return reject("source_rate")
	}
	bucket.tokens--
	bucket.active++
	bucket.accepted++
	srv.admissionAccepted++
	return true
}
func (srv *Server) releaseAdmissionLocked(source string) {
	if bucket := srv.sources[source]; bucket != nil {
		bucket.active--
		bucket.updated = time.Now()
	}
}
func (srv *Server) AdmissionSnapshot() AdmissionStats {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	r := AdmissionStats{Active: len(srv.handshakes), Sources: len(srv.sources), Accepted: srv.admissionAccepted, Rejected: srv.admissionRejected, RejectionReasons: copyCounts(srv.admissionReasons), HandshakeOutcomes: copyCounts(srv.handshakeOutcomes), AcceptRetries: srv.acceptRetries, LastAcceptError: srv.lastAcceptError, GlobalLimit: handshakeGlobalLimit, SourceLimit: handshakeSourceLimit, SourceRate: handshakeRate, SourceBurst: handshakeBurst}
	for source, b := range srv.sources {
		r.SourceDetails = append(r.SourceDetails, SourceAdmissionStats{source, b.active, b.accepted, b.rejected, b.lastReason, b.lastRejectedAt})
	}
	sort.Slice(r.SourceDetails, func(i, j int) bool {
		if r.SourceDetails[i].Rejected != r.SourceDetails[j].Rejected {
			return r.SourceDetails[i].Rejected > r.SourceDetails[j].Rejected
		}
		return r.SourceDetails[i].Source < r.SourceDetails[j].Source
	})
	if len(r.SourceDetails) > 16 {
		r.SourceDetails = r.SourceDetails[:16]
		r.SourceDetailsTruncated = true
	}
	return r
}
