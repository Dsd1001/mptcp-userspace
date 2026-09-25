package main

import (
	"encoding/binary"
	"errors"
	"fmt"
)

var errPCBRead = errors.New("读取 MPTCP 子流状态失败")

func minimumPathCount(counts []int) int {
	minimum, found := 0, false
	for _, count := range counts {
		if count == -2 {
			continue
		}
		if !found || count < minimum {
			minimum = count
			found = true
		}
	}
	return minimum
}

func readyFlags(flags uint32) (bool, error) {
	if flags&0x400 != 0 {
		return false, errors.New("TCP 已连接，但未协商 MPTCP（已降级为普通 TCP）；请检查 TUN 排除规则、Relay 转发和 Landing 的 MPTCP 监听")
	}
	if flags&2 == 0 {
		return false, nil
	}
	if flags&0x100 == 0 {
		return false, errors.New("已连接的 socket 没有 MPTCP 能力")
	}
	if flags&0x1000 == 0 {
		return false, errors.New("对端没有协商 MPTCP v1")
	}
	return true, nil
}

type flowTuple [12]byte

// Darwin pcblist supplies lengths/offsets for each record. Only the stable
// IPv4 tuple and subflow-state prefix are read; other sockets are not retained.
func readySubflows(data []byte, own flowTuple) (int, error) {
	for len(data) > 0 {
		if len(data) < 24 {
			return 0, errors.New("MPTCP 诊断记录头部不完整")
		}
		size := binary.LittleEndian.Uint64(data[:8])
		start := binary.LittleEndian.Uint64(data[8:16])
		count := binary.LittleEndian.Uint64(data[16:24])
		if size < 24 || size > uint64(len(data)) || start < 24 || start > size || count > 64 {
			return 0, errors.New("不支持当前 macOS 的 MPTCP 诊断记录格式")
		}
		record := data[:int(size)]
		offset := int(start)
		matched := false
		ready := 0
		for i := uint64(0); i < count; i++ {
			if len(record)-offset < 292 {
				return 0, errors.New("MPTCP 子流诊断记录不完整")
			}
			flow := record[offset:]
			length := binary.LittleEndian.Uint64(flow[:8])
			tcpOffset := binary.LittleEndian.Uint64(flow[8:16])
			if length < 292 || length > uint64(len(flow)) || tcpOffset < 292 || tcpOffset > length {
				return 0, errors.New("MPTCP 子流诊断记录长度无效")
			}
			flags := binary.LittleEndian.Uint32(flow[16:20])
			soerror := binary.LittleEndian.Uint32(flow[284:288])
			if flow[24] == 16 && flow[25] == 2 && flow[152] == 16 && flow[153] == 2 {
				var tuple flowTuple
				copy(tuple[0:4], flow[28:32])
				copy(tuple[4:6], flow[26:28])
				copy(tuple[6:10], flow[156:160])
				copy(tuple[10:12], flow[154:156])
				if tuple == own {
					matched = true
				}
			}
			// CONNECTED + MP_CAPABLE counts an established MPTCP subflow.
			// MP_READY gates further JOINs and may be absent on an idle primary.
			if flags&0x48 == 0x48 && flags&0x136 == 0 && soerror == 0 {
				ready++
			}
			offset += int(length)
		}
		if matched {
			return ready, nil
		}
		data = data[int(size):]
	}
	return 0, fmt.Errorf("尚未找到当前连接的 MPTCP 子流")
}
