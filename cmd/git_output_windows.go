//go:build windows

package cmd

import (
	"bytes"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Job handle 不继承给子进程。父进程被 Claude Code 强制终止时,系统关闭
// 最后一个 job handle,终止其 Git 及后代;正常退出同样清理残留后代。
func gitOutput(c *exec.Cmd) ([]byte, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	c.Stdout = &out
	if err := c.Start(); err != nil {
		return nil, err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(c.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, handle)
		_ = windows.CloseHandle(handle)
	}
	if err != nil {
		_ = c.Process.Kill()
		_ = c.Wait()
		return nil, err // 无法约束生命周期时省略 Git 段。
	}
	err = c.Wait()
	return out.Bytes(), err
}
