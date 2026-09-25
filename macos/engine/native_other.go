//go:build !darwin

package main

import (
	"context"
	"errors"
	"net"
)

type nativeConn struct{ net.Conn }

func (c *nativeConn) pathStatus() (int, error) { return 0, nativePreflight() }

func (c *nativeConn) CloseWrite() error { return nativePreflight() }

func nativePreflight() error                                   { return errors.New("桌面 MPTCP 传输只支持 macOS") }
func nativeDial(context.Context, []Relay) (*nativeConn, error) { return nil, nativePreflight() }
func (c *nativeConn) paths() int                               { return 0 }

func nativePathCounts(connections []forwardConn) ([]int, error) {
	counts := make([]int, len(connections))
	for i, conn := range connections {
		counts[i] = conn.paths()
	}
	return counts, nil
}
