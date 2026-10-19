package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const vcRedistURL = "https://aka.ms/vs/17/release/vc_redist.x64.exe"

// Replaced in tests, which must never download or run the real installer.
var (
	hasBundle   = func() bool { return loadManifest(bundleFS) != nil }
	vcInstalled = vcRuntimeInstalled
	vcDownload  = download
	vcInstall   = func(ctx context.Context, exe string) (uint32, error) {
		return shellRun(ctx, "runas", exe, "/install /passive /norestart", windows.SW_SHOWNORMAL)
	}
	declinedMarker = func() string {
		dir, err := os.UserCacheDir()
		if err != nil {
			return ""
		}
		return filepath.Join(dir, "fox", "vcredist-declined")
	}
)

var errDeclined = errors.New("installation was declined")

// EnsureRuntime installs the Visual C++ 2015+ runtime most bundled tools need, if it is missing.
// Failures are only reported: tools that don't need the runtime still work.
func EnsureRuntime(ctx context.Context, logf func(format string, args ...any)) {
	if !hasBundle() || vcInstalled() {
		return
	}
	marker := declinedMarker()
	if _, err := os.Stat(marker); marker != "" && err == nil {
		logf("warning: the Microsoft Visual C++ runtime is missing, so many tools will fail; install it from %s "+
			"(fox stopped offering to after the installation was declined; delete %s to be asked again)", vcRedistURL, marker)
		return
	}
	logf("the Microsoft Visual C++ runtime needed by the bundled tools is missing; downloading and installing it (confirm the prompt)")
	err := installRuntime(ctx)
	if err == nil {
		logf("Visual C++ runtime installed")
		return
	}
	logf("warning: Visual C++ runtime not installed: %v; many tools will fail until it is installed from %s", err, vcRedistURL)
	if errors.Is(err, errDeclined) && marker != "" {
		// Otherwise every start, or every fox call of a script, downloads 25 MB and prompts again.
		os.MkdirAll(filepath.Dir(marker), 0o755)
		os.WriteFile(marker, nil, 0o644)
	}
}

func installRuntime(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "fox-vcredist-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	exe := filepath.Join(dir, "vc_redist.x64.exe")
	// A stalled download would otherwise hold up fox's start indefinitely.
	dctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := vcDownload(dctx, vcRedistURL, exe); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	code, err := vcInstall(ctx, exe)
	switch {
	case errors.Is(err, windows.ERROR_CANCELLED):
		return errDeclined
	case err != nil:
		return err
	case vcInstalled():
		return nil
	case code == uint32(windows.ERROR_INSTALL_USEREXIT): // cancelled in the installer window
		return errDeclined
	}
	return fmt.Errorf("installer exit code %d", code)
}

func vcRuntimeInstalled() bool {
	sys, _ := windows.GetSystemDirectory()
	// The DLLs are what the tools load; runtimes deployed without vc_redist (merge modules, other installers)
	// don't set the registry value, and fox would then prompt on every start.
	return vcRegistered() || sys != "" && hasFiles(sys, "vcruntime140.dll", "msvcp140.dll", "vcomp140.dll")
}

func vcRegistered() bool {
	// The x64 runtime registers in the 64-bit view; be explicit in case fox itself is a 32-bit build.
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\VisualStudio\14.0\VC\Runtimes\X64`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("Installed")
	return err == nil && v == 1
}

func hasFiles(dir string, names ...string) bool {
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			return false
		}
	}
	return true
}

func download(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	return errors.Join(err, f.Close())
}

// shellExecuteInfo is SHELLEXECUTEINFOW, which x/sys/windows does not define.
type shellExecuteInfo struct {
	size     uint32
	mask     uint32
	hwnd     windows.HWND
	verb     *uint16
	file     *uint16
	params   *uint16
	dir      *uint16
	show     int32
	instApp  windows.Handle
	idList   uintptr
	class    *uint16
	keyClass windows.Handle
	hotKey   uint32
	icon     windows.Handle
	process  windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x40
	seeMaskNoAsync        = 0x100
	seeMaskFlagNoUI       = 0x400
)

var procShellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// shellRun is used instead of os/exec because only ShellExecuteEx can trigger the UAC elevation prompt.
// On cancellation it stops waiting but leaves the process running: killing an installer midway is worse.
func shellRun(ctx context.Context, verb, file, params string, show int32) (uint32, error) {
	sei := shellExecuteInfo{
		mask:   seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI,
		verb:   windows.StringToUTF16Ptr(verb),
		file:   windows.StringToUTF16Ptr(file),
		params: windows.StringToUTF16Ptr(params),
		show:   show,
	}
	sei.size = uint32(unsafe.Sizeof(sei))
	if ok, _, err := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&sei))); ok == 0 {
		return 0, err
	}
	if sei.process == 0 {
		return 0, errors.New("no process started")
	}
	defer windows.CloseHandle(sei.process)
	for {
		ev, err := windows.WaitForSingleObject(sei.process, 100)
		if err != nil {
			return 0, err
		}
		if ev == windows.WAIT_OBJECT_0 {
			break
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
	}
	var code uint32
	err := windows.GetExitCodeProcess(sei.process, &code)
	return code, err
}
