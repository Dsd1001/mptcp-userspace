package main

import (
	"errors"
	"fmt"
)

func validateWindowsBundle(bundle *managedBundle) error {
	if bundle.SchemaVersion != 2 || bundle.Kind != "bundle" {
		return errors.New("Bundle 仅支持 schema_version=2 / kind=bundle")
	}
	if bundle.BundleID == "" || len(bundle.BundleID) > 128 || len(bundle.DisplayName) > 128 || len(bundle.Revision) > 128 {
		return errors.New("Bundle 标识、名称或 revision 无效")
	}
	if bundle.Mode != "single_select" && bundle.Mode != "parallel" {
		return errors.New("Bundle mode 仅支持 single_select 或 parallel")
	}
	if len(bundle.Profiles) < 1 || len(bundle.Profiles) > 32 {
		return errors.New("Bundle 需要 1–32 个 Profile")
	}
	seen := map[string]bool{}
	windowsProfiles := make([]managedProfile, 0, len(bundle.Profiles))
	for _, p := range bundle.Profiles {
		if p.ProfileID == "" || seen[p.ProfileID] {
			return errors.New("Bundle Profile ID 必须非空且唯一")
		}
		seen[p.ProfileID] = true
		if p.Mode != "userspace_multipath" {
			// Windows intentionally has no Native MPTCP fallback. Mac-only
			// profiles may coexist in a shared Bundle, but they are hidden.
			continue
		}
		if err := p.validateWindows(); err != nil {
			return fmt.Errorf("Profile %s: %w", p.DisplayName, err)
		}
		windowsProfiles = append(windowsProfiles, p)
	}
	if len(windowsProfiles) == 0 {
		return errors.New("Bundle 中没有 Windows 可用的 userspace_multipath Profile")
	}
	bundle.Profiles = windowsProfiles
	return nil
}
