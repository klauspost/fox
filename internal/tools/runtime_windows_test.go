package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestMain(m *testing.M) {
	vcDownload = func(context.Context, string, string) error { panic("test tried to download vc_redist") }
	vcInstall = func(context.Context, string) (uint32, error) { panic("test tried to run vc_redist") }
	dir, err := os.MkdirTemp("", "fox-test-")
	if err != nil {
		panic(err)
	}
	declinedMarker = func() string { return filepath.Join(dir, "vcredist-declined") }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestEnsureRuntime(t *testing.T) {
	for _, tc := range []struct {
		name              string
		bundle, installed bool
		downloadErr       error
		code              uint32
		installErr        error
		installedAfter    bool
		download, install bool
		warn              string
		declined          bool
	}{
		{name: "no bundle", installed: false},
		{name: "installed", bundle: true, installed: true},
		{name: "ok", bundle: true, installedAfter: true, download: true, install: true},
		{name: "download fails", bundle: true, downloadErr: errors.New("offline"), download: true, warn: "offline"},
		{name: "declined", bundle: true, installErr: windows.ERROR_CANCELLED, download: true, install: true, warn: "declined", declined: true},
		{name: "cancelled in installer", bundle: true, code: 1602, download: true, install: true, warn: "declined", declined: true},
		{name: "still missing", bundle: true, code: 1603, download: true, install: true, warn: "exit code 1603"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var downloaded, ran bool
			installed := tc.installed
			marker := filepath.Join(t.TempDir(), "fox", "declined")
			stub(t, &declinedMarker, func() string { return marker })
			stub(t, &hasBundle, func() bool { return tc.bundle })
			stub(t, &vcInstalled, func() bool { return installed })
			stub(t, &vcDownload, func(ctx context.Context, url, dst string) error {
				downloaded = true
				if url != vcRedistURL {
					t.Errorf("url %q", url)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Error("download without deadline")
				}
				if tc.downloadErr != nil {
					return tc.downloadErr
				}
				return os.WriteFile(dst, []byte("MZ"), 0o644)
			})
			stub(t, &vcInstall, func(_ context.Context, exe string) (uint32, error) {
				ran = true
				if _, err := os.Stat(exe); err != nil {
					t.Errorf("installer not downloaded: %v", err)
				}
				installed = tc.installedAfter
				return tc.code, tc.installErr
			})
			var log []string
			logf := func(f string, a ...any) { log = append(log, fmt.Sprintf(f, a...)) }
			EnsureRuntime(t.Context(), logf)
			if downloaded != tc.download || ran != tc.install {
				t.Errorf("downloaded=%v ran=%v", downloaded, ran)
			}
			all := strings.Join(log, "\n")
			if tc.warn != "" && (!strings.Contains(all, "warning") || !strings.Contains(all, tc.warn)) {
				t.Errorf("want warning with %q, got %q", tc.warn, all)
			}
			if tc.warn == "" && strings.Contains(all, "warning") {
				t.Errorf("unexpected warning %q", all)
			}
			if !tc.download && len(log) > 0 {
				t.Errorf("unexpected output %q", all)
			}
			if _, err := os.Stat(marker); (err == nil) != tc.declined {
				t.Fatalf("declined marker: %v", err)
			}

			// After a decline, later starts must only warn instead of downloading and prompting again.
			downloaded, ran, log = false, false, nil
			EnsureRuntime(t.Context(), logf)
			if tc.declined && (downloaded || ran || !strings.Contains(strings.Join(log, "\n"), vcRedistURL)) {
				t.Errorf("after decline: downloaded=%v ran=%v log=%q", downloaded, ran, log)
			}
		})
	}
}

func TestHasFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.dll"), nil, 0o644)
	if !hasFiles(dir, "a.dll") || hasFiles(dir, "a.dll", "b.dll") {
		t.Fatal("wrong result")
	}
}

func stub[T any](t *testing.T, p *T, v T) {
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func TestRuntimeRegistry(t *testing.T) {
	sys, _ := windows.GetSystemDirectory()
	t.Logf("VC++ runtime registered: %v, DLLs in %s: %v", vcRegistered(), sys, hasFiles(sys, "vcruntime140.dll", "msvcp140.dll", "vcomp140.dll"))
}

func TestDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ok" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("payload"))
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "x.exe")
	if err := download(t.Context(), srv.URL+"/ok", dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "payload" {
		t.Fatalf("got %q", b)
	}
	if err := download(t.Context(), srv.URL+"/missing", dst); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("404 not reported: %v", err)
	}
}

// TestShellRun exercises the SHELLEXECUTEINFOW layout and the wait with a hidden cmd.exe; "runas" would prompt.
func TestShellRun(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) == 8 && unsafe.Sizeof(shellExecuteInfo{}) != 112 {
		t.Fatalf("SHELLEXECUTEINFOW size %d, want 112", unsafe.Sizeof(shellExecuteInfo{}))
	}
	cmd := os.Getenv("ComSpec")
	code, err := shellRun(t.Context(), "open", cmd, "/c exit 7", windows.SW_HIDE)
	if err != nil || code != 7 {
		t.Fatalf("got %d, %v", code, err)
	}
	if _, err := shellRun(t.Context(), "open", filepath.Join(t.TempDir(), "missing.exe"), "", windows.SW_HIDE); err == nil {
		t.Fatal("missing file did not fail")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := shellRun(ctx, "open", cmd, "/c ping -n 3 127.0.0.1 >nul", windows.SW_HIDE); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("cancel took %v", d)
	}
}
