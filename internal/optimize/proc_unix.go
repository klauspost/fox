//go:build unix

package optimize

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func procAttr(bool) *syscall.SysProcAttr { return nil }

// contain lowers the priority after start, as Unix has no way to set it at creation.
// Process groups are not used: a wine tool may start the shared wineserver, which must survive a cancel.
func contain(cmd *exec.Cmd, low bool) (started, done func()) {
	return func() {
		if low {
			_ = unix.Setpriority(unix.PRIO_PROCESS, cmd.Process.Pid, 10)
		}
	}, func() {}
}

func shortPath(p string) string { return p }

// tmpRoot is per user: with a shared /tmp/fox, other users could block it or swap job dirs mid-run.
// sudo may keep HOME (macOS does by default); dirs created in another user's home would lock
// them out of fox, and dirs they own there are rejected by ensurePrivate.
func tmpRoot() string {
	uid := os.Getuid()
	if home, err := os.UserHomeDir(); err == nil && owner(home) == uid {
		if dir, err := os.UserCacheDir(); err == nil {
			return filepath.Join(dir, "fox", "tmp")
		}
	}
	return filepath.Join(os.TempDir(), "fox-"+strconv.Itoa(uid))
}

func owner(p string) int {
	if fi, err := os.Stat(p); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			return int(st.Uid)
		}
	}
	return -1
}

// ensurePrivate rejects a dir that another user owns or that is a symlink, since it may
// already exist (planted in a shared /tmp), and makes it inaccessible to others.
func ensurePrivate(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("temp dir %s is not a directory owned by the current user", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// atime returns the zero time, which makes os.Chtimes leave the access time alone.
func atime(os.FileInfo) time.Time { return time.Time{} }
