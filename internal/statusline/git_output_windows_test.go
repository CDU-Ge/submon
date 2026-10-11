//go:build windows

package statusline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsGitDiesWithStatuslineParent(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "pid")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	parent := exec.CommandContext(ctx, exe)
	parent.Env = append(os.Environ(), "SUBMON_TEST_JOB_PARENT=1", "SUBMON_TEST_SLOW_GIT="+filepath.Join(dir, "spawns"), "SUBMON_TEST_GIT_PID_FILE="+pidPath)
	parent.SysProcAttr = hideWindowProcAttr()
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Process.Kill(); _ = parent.Wait() }()
	var raw []byte
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err = os.ReadFile(pidPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal("Git helper 未启动: ", err)
	}
	pidValue, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pidValue))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	defer windows.TerminateProcess(child, 1)
	time.Sleep(100 * time.Millisecond) // 等待父进程完成 Job Object 绑定。
	if state, err := windows.WaitForSingleObject(child, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("父进程仍运行时 Git 意外退出: %d %v", state, err)
	}
	_ = parent.Process.Kill()
	_ = parent.Wait()
	if state, err := windows.WaitForSingleObject(child, 2000); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("父进程终止后 Git 未退出: %d %v", state, err)
	}
}
