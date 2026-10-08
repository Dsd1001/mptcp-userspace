package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
)

func validateTransportKey(key string) error {
	if len(key) != 64 {
		return errors.New("Transport Key 必须是 64 位十六进制")
	}
	data, err := hex.DecodeString(key)
	if err != nil || len(data) != 32 {
		return errors.New("Transport Key 必须是 64 位十六进制")
	}
	return nil
}

func validateLocalProfile(p LocalProfile, keyStored bool) error {
	if p.ListenPort < 1024 || p.ListenPort > 65535 {
		return errors.New("本地监听端口必须在 1024–65535")
	}
	if !p.TCPEnabled && !p.UDPEnabled && !p.UOTEnabled {
		return errors.New("TCP、UDP、UoT 至少启用一项")
	}
	if len(p.Relays) < 2 || len(p.Relays) > 8 {
		return errors.New("Windows Userspace Profile 需要 2–8 条 Relay")
	}
	switch p.SchedulerMode {
	case "auto", "aggregate", "protect", "weighted":
	default:
		return errors.New("Scheduler 仅支持 auto、aggregate、protect、weighted")
	}
	for i, relay := range p.Relays {
		host := strings.TrimSpace(relay.Host)
		if host == "" {
			return fmt.Errorf("Relay %d 地址不能为空", i+1)
		}
		if relay.Port < 1 || relay.Port > 65535 {
			return fmt.Errorf("Relay %d 端口无效", i+1)
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() && relay.Port == p.ListenPort {
			return fmt.Errorf("Relay %d 不能指向本地转发入口", i+1)
		}
		if p.SchedulerMode == "weighted" {
			if relay.DownloadMbps == nil || *relay.DownloadMbps <= 0 || *relay.DownloadMbps > 6553.5 {
				return fmt.Errorf("Relay %d Weighted 下行 Mbps 必须在 0.1–6553.5", i+1)
			}
			if relay.UploadMbps != nil && (*relay.UploadMbps <= 0 || *relay.UploadMbps > 6553.5) {
				return fmt.Errorf("Relay %d Weighted 上行 Mbps 留空表示自动估算；填写时必须在 0.1–6553.5", i+1)
			}
		}
	}
	if !keyStored && strings.TrimSpace(p.TransportKey) == "" {
		return errors.New("Transport Key 不能为空")
	}
	return nil
}

func (p managedProfile) validateWindows() error {
	if p.SchemaVersion != 1 {
		return errors.New("Provisioning Profile schema_version 必须为 1")
	}
	if p.Mode != "userspace_multipath" {
		return errors.New("Windows 仅支持 userspace_multipath，不支持 Native/内核 MPTCP fallback")
	}
	local := LocalProfile{
		ListenPort:    p.ListenPort,
		TCPEnabled:    p.TCPEnabled,
		UDPEnabled:    p.UDPEnabled,
		UOTEnabled:    p.UOTEnabled,
		SchedulerMode: p.SchedulerMode,
		Relays:        p.Relays,
		TransportKey:  p.TransportKey,
	}
	if local.SchedulerMode == "" {
		local.SchedulerMode = "auto"
	}
	if err := validateTransportKey(p.TransportKey); err != nil {
		return err
	}
	return validateLocalProfile(local, true)
}

func (p managedProfile) engineConfig() engineConfig {
	mode := p.SchedulerMode
	if mode == "" {
		mode = "auto"
	}
	return engineConfig{
		SchemaVersion: 3,
		Mode:          "userspace_multipath",
		ListenPort:    p.ListenPort,
		TCPEnabled:    p.TCPEnabled,
		UDPEnabled:    p.UDPEnabled,
		UOTEnabled:    p.UOTEnabled,
		SchedulerMode: mode,
		Relays:        p.Relays,
		TransportKey:  p.TransportKey,
	}
}
