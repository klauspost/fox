package optimize

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func procAttr(low bool) *syscall.SysProcAttr {
	flags := uint32(windows.CREATE_NO_WINDOW)
	if low {
		flags |= windows.BELOW_NORMAL_PRIORITY_CLASS
	}
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
}

// contain puts the tool in a job object, so cancelling it, or the tool exiting, also ends processes it
// spawned (m7zRepacker runs 7z.exe); those would otherwise keep running and lock the job's temp files.
// started must be called after cmd.Start, done after cmd.Wait.
func contain(cmd *exec.Cmd, _ bool) (started, done func()) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return func() {}, func() {}
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, _ = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	cmd.Cancel = func() error {
		_ = windows.TerminateJobObject(job, 1)
		// The process may not have been assigned yet.
		return cmd.Process.Kill()
	}
	started = func() {
		h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
		if err == nil {
			_ = windows.AssignProcessToJobObject(job, h)
			_ = windows.CloseHandle(h)
		}
	}
	return started, func() { _ = windows.CloseHandle(job) }
}

// shortPath returns the 8.3 form of an existing path, since old tools fail on Unicode and spaces.
func shortPath(p string) string {
	u, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetShortPathName(u, &buf[0], uint32(len(buf)))
		if err != nil || n == 0 {
			return p
		}
		if int(n) <= len(buf) {
			return windows.UTF16ToString(buf[:n])
		}
		buf = make([]uint16, n)
	}
}

// tmpRoot can be shared by name since TEMP is per user on Windows.
func tmpRoot() string { return filepath.Join(os.TempDir(), "fox") }

func ensurePrivate(string) error { return nil }

func atime(fi os.FileInfo) time.Time {
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		return time.Unix(0, d.LastAccessTime.Nanoseconds())
	}
	return time.Time{}
}
