package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Staggering avoids paying the full timeout of an unhealthy preferred Relay.
// Only the winner carries application data; unused sockets are always closed.
func raceRelays(ctx context.Context, relays []Relay, delay time.Duration, dial func(context.Context, Relay) (forwardConn, error)) (forwardConn, Relay, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		conn  forwardConn
		relay Relay
		err   error
	}
	results := make(chan result)
	for i, relay := range relays {
		go func(index int, r Relay) {
			if index > 0 {
				timer := time.NewTimer(time.Duration(index) * delay)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					return
				}
			}
			conn, err := dial(ctx, r)
			select {
			case results <- result{conn, r, err}:
			case <-ctx.Done():
				if conn != nil {
					conn.Close()
				}
			}
		}(i, relay)
	}
	var failures []error
	for range relays {
		select {
		case <-ctx.Done():
			return nil, Relay{}, ctx.Err()
		case result := <-results:
			if result.err == nil {
				if err := ctx.Err(); err != nil {
					result.conn.Close()
					return nil, Relay{}, err
				}
				return result.conn, result.relay, nil
			}
			if result.conn != nil {
				result.conn.Close()
			}
			failures = append(failures, fmt.Errorf("%s:%d: %w", result.relay.Host, result.relay.Port, result.err))
		}
	}
	return nil, Relay{}, fmt.Errorf("全部 %d 台 Relay 未能协商 MPTCP: %w", len(relays), errors.Join(failures...))
}
