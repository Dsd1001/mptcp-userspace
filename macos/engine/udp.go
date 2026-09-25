package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

type udpKey struct {
	client netip.AddrPort
	relay  int
}
type udpAssociation struct {
	conn *net.UDPConn
	last atomic.Int64
}

// Each source/Relay pair owns a connected UDP socket, so replies cannot be
// delivered to another local sender. Datagrams are never framed or retried.
func serveUDP(ctx context.Context, listener *net.UDPConn, relays []Relay, idle time.Duration, limit int) error {
	if len(relays) == 0 || limit < 1 || idle <= 0 {
		listener.Close()
		return errors.New("invalid UDP forwarding limits")
	}
	// macOS defaults can reject UDP datagrams larger than a few KiB.
	if err := listener.SetReadBuffer(256 * 1024); err != nil {
		listener.Close()
		return err
	}
	if err := listener.SetWriteBuffer(65535); err != nil {
		listener.Close()
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	flows := make(map[udpKey]*udpAssociation)
	var readers sync.WaitGroup
	var sent, received atomic.Int64
	remove := func(key udpKey, flow *udpAssociation) {
		mu.Lock()
		if flows[key] == flow {
			delete(flows, key)
		}
		mu.Unlock()
		flow.conn.Close()
	}
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		interval := time.Second
		if idle < interval {
			interval = idle
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				listener.Close()
				return
			case now := <-ticker.C:
				mu.Lock()
				for key, flow := range flows {
					if now.Sub(time.Unix(0, flow.last.Load())) >= idle {
						delete(flows, key)
						flow.conn.Close()
					}
				}
				count := len(flows)
				mu.Unlock()
				emit(Event{Kind: "udp_stats", Connections: int64(count), Sent: sent.Load(), Received: received.Load()})
			}
		}
	}()
	defer func() {
		cancel()
		listener.Close()
		<-monitorDone
		mu.Lock()
		for _, flow := range flows {
			flow.conn.Close()
		}
		mu.Unlock()
		readers.Wait()
	}()
	buffer := make([]byte, 65535)
	next := 0
	for {
		n, client, err := listener.ReadFromUDPAddrPort(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		key := udpKey{client: client, relay: next}
		next = (next + 1) % len(relays)
		mu.Lock()
		flow := flows[key]
		if flow == nil && len(flows) < limit {
			relay := relays[key.relay]
			conn, dialErr := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP(relay.Host), Port: relay.Port})
			if dialErr == nil {
				dialErr = conn.SetReadBuffer(256 * 1024)
				if dialErr == nil {
					dialErr = conn.SetWriteBuffer(65535)
				}
				if dialErr != nil {
					conn.Close()
				}
			}
			if dialErr == nil {
				flow = &udpAssociation{conn: conn}
				flow.last.Store(time.Now().UnixNano())
				flows[key] = flow
				readers.Add(1)
				go func(key udpKey, flow *udpAssociation) {
					defer readers.Done()
					defer remove(key, flow)
					response := make([]byte, 65535)
					for {
						n, err := flow.conn.Read(response)
						if err != nil {
							return
						}
						flow.last.Store(time.Now().UnixNano())
						if n, err = listener.WriteToUDPAddrPort(response[:n], key.client); err == nil {
							received.Add(int64(n))
						}
					}
				}(key, flow)
			}
		}
		if flow != nil {
			flow.last.Store(time.Now().UnixNano())
		}
		mu.Unlock()
		if flow == nil {
			continue
		}
		if err := flow.conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
			remove(key, flow)
			continue
		}
		if written, err := flow.conn.Write(buffer[:n]); err != nil {
			remove(key, flow)
		} else {
			sent.Add(int64(written))
		}
	}
}
