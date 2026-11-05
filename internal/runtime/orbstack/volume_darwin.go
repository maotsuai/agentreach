package orbstack

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/bojieli/agentreach/internal/project"
	"github.com/bojieli/agentreach/internal/runtime/docker"
)

// CheckNativeControlHome resolves the actual UID's account record independently
// of HOME, which offline probes intentionally replace with a temporary home.
func CheckNativeControlHome(ctx context.Context, home string) error {
	uid := os.Getuid()
	cmd := exec.CommandContext(ctx, "/usr/bin/dscacheutil", "-q", "user", "-a", "uid", strconv.Itoa(uid))
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	out, err := cmd.Output()
	if err != nil {
		return docker.Fail("storage_unavailable", "native account lookup: %v", err)
	}
	return verifyControlHome(out, uid, home)
}

// CheckNativeVolume reads diskutil, which has no Mounted field. MountPoint plus
// statfs's actual mount table establishes the mounted root independently.
func CheckNativeVolume(ctx context.Context, v project.VolumeClaim) error {
	out, err := exec.CommandContext(ctx, "/usr/sbin/diskutil", "info", "-plist", v.Path).Output()
	if err != nil {
		return docker.Fail("storage_unavailable", "diskutil: %v", err)
	}
	values, err := parsePlist(out)
	if err != nil {
		return docker.Fail("storage_unavailable", "diskutil plist: %v", err)
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(v.Path, &fs); err != nil {
		return docker.Fail("storage_unavailable", "statfs: %v", err)
	}
	var mount []byte
	for _, c := range fs.Mntonname {
		if c == 0 {
			break
		}
		mount = append(mount, byte(c))
	}
	if values["MountPoint"] != v.Path || string(mount) != v.Path || !strings.EqualFold(values["VolumeUUID"], v.UUID) || values["VolumeName"] != v.Name || values["Internal"] != "false" || values["Writable"] != "true" {
		return docker.Fail("storage_unavailable", "external volume identity/mount mismatch: %s", v.Path)
	}
	return nil
}

// CheckNativeEngineData verifies OrbStack 2.2.4's native data_dir read-only.
// Unknown formats fail closed; no orb command or storage mutation is attempted.
func CheckNativeEngineData(_ context.Context, config, path string) error {
	resolved, err := filepath.EvalSymlinks(config)
	if err != nil || resolved != config {
		return docker.Fail("storage_unavailable", "native OrbStack vmconfig is unavailable/noncanonical")
	}
	fi, err := os.Stat(config)
	if err != nil || fi.Size() > 1<<20 {
		return docker.Fail("storage_unavailable", "invalid native OrbStack vmconfig")
	}
	b, err := os.ReadFile(config)
	if err != nil {
		return docker.Fail("storage_unavailable", "read native OrbStack storage configuration: %v", err)
	}
	var v struct {
		DataDir string `json:"data_dir"`
	}
	if err := json.Unmarshal(b, &v); err != nil || v.DataDir == "" || v.DataDir != path {
		return docker.Fail("storage_unavailable", "unverified OrbStack data_dir; registration does not match native configuration")
	}
	return nil
}

// CheckNativePathVolume rejects nested mounts that are not the approved volume.
func CheckNativePathVolume(_ context.Context, p string, v project.VolumeClaim) error {
	var root, actual syscall.Statfs_t
	if err := syscall.Statfs(v.Path, &root); err != nil {
		return docker.Fail("storage_unavailable", "statfs approved volume: %v", err)
	}
	if err := syscall.Statfs(p, &actual); err != nil {
		return docker.Fail("storage_unavailable", "statfs used path: %v", err)
	}
	if root.Fsid != actual.Fsid || root.Mntonname != actual.Mntonname {
		return docker.Fail("storage_unavailable", "used path is on an unapproved nested mount: %s", p)
	}
	return nil
}
