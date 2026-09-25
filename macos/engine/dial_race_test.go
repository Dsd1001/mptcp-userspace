package main

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type raceTestConn struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

func newRaceTestConn() *raceTestConn      { return &raceTestConn{closed: make(chan struct{})} }
func (c *raceTestConn) Close() error      { c.once.Do(func() { close(c.closed) }); return nil }
func (c *raceTestConn) CloseWrite() error { return nil }
func (c *raceTestConn) paths() int        { return 1 }

func TestRelayRaceBypassesStalledPrimary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	relays := validProfile().Relays
	stopped := make(chan struct{})
	winner := newRaceTestConn()
	conn, relay, err := raceRelays(ctx, relays, time.Millisecond, func(ctx context.Context, r Relay) (forwardConn, error) {
		if r == relays[0] {
			<-ctx.Done()
			close(stopped)
			return nil, ctx.Err()
		}
		return winner, nil
	})
	if err != nil || conn != winner || relay != relays[1] {
		t.Fatalf("winner=%v relay=%v err=%v", conn, relay, err)
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("loser was not canceled")
	}
	select {
	case <-winner.closed:
		t.Fatal("winner was closed")
	default:
	}
	winner.Close()
}

func TestRelayRaceClosesLateSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	relays := validProfile().Relays
	started := make(chan struct{})
	late, winner := newRaceTestConn(), newRaceTestConn()
	conn, _, err := raceRelays(ctx, relays, 0, func(ctx context.Context, r Relay) (forwardConn, error) {
		if r == relays[0] {
			close(started)
			<-ctx.Done()
			return late, nil
		}
		<-started
		return winner, nil
	})
	if err != nil || conn != winner {
		t.Fatalf("winner=%v err=%v", conn, err)
	}
	defer winner.Close()
	select {
	case <-late.closed:
	case <-ctx.Done():
		t.Fatal("late connection leaked")
	}
}

func TestRelayRaceFailureAndCancellation(t *testing.T) {
	failure := errors.New("handshake failed")
	conn, _, err := raceRelays(context.Background(), validProfile().Relays, 0, func(context.Context, Relay) (forwardConn, error) {
		return nil, failure
	})
	if conn != nil || !errors.Is(err, failure) {
		t.Fatalf("failure lost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn, _, err = raceRelays(ctx, validProfile().Relays, time.Hour, func(ctx context.Context, _ Relay) (forwardConn, error) {
		return nil, ctx.Err()
	})
	if conn != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost: %v", err)
	}
}
