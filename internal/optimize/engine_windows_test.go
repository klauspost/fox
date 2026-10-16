package optimize

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/klauspost/fox/internal/pipeline"
	"golang.org/x/sys/windows"
)

// Cancelling must also kill processes the tool started, or they keep temp files locked and
// cmd.Wait hangs on the inherited pipe until WaitDelay.
func TestCancelKillsTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	e := newTestEngine(t, []pipeline.Step{step("spawn", pidFile, "{in}")}, nil)
	id := e.Add(testFile(t, t.TempDir(), "a.png", 64), false)[0]
	var pid int
	waitFor(t, e, id, func(Event) bool {
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(string(b))
		return pid != 0
	})
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	defer windows.TerminateProcess(h, 1)

	start := time.Now()
	e.Cancel(id)
	e.Wait()
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("cancel took %v", d)
	}
	if ev, _ := windows.WaitForSingleObject(h, 3000); ev != windows.WAIT_OBJECT_0 {
		t.Fatal("grandchild still running")
	}
	if es, _ := os.ReadDir(e.h.tmp); len(es) != 0 {
		t.Errorf("temp dir not cleaned: %v", es)
	}
}
