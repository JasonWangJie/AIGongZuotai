package videoexport

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func resumeCodec(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	var thread windows.ThreadEntry32
	thread.Size = uint32(unsafe.Sizeof(thread))
	for err = windows.Thread32First(snap, &thread); err == nil; err = windows.Thread32Next(snap, &thread) {
		if thread.OwnerProcessID != pid {
			continue
		}
		h, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, thread.ThreadID)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(h)
		previous, err := windows.ResumeThread(h)
		if err != nil || previous != 1 {
			return ErrUnavailable
		}
		return nil
	}
	return ErrUnavailable
}

// No codec instructions run before the kernel enforces the memory cap and
// descendant limit. Keep the Job handle until Wait has reaped the process.
func runControlled(cmd *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer windows.CloseHandle(job)
	var lim windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	lim.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS | windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY
	lim.BasicLimitInformation.ActiveProcessLimit = 1
	lim.ProcessMemoryLimit = 768 << 20
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&lim)), uint32(unsafe.Sizeof(lim))); err != nil {
		return ErrUnavailable
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW}
	if err = cmd.Start(); err != nil {
		return ErrUnavailable
	}
	pid := uint32(cmd.Process.Pid)
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err == nil {
		err = windows.AssignProcessToJobObject(job, p)
		windows.CloseHandle(p)
	}
	if err == nil {
		err = resumeCodec(pid)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return ErrUnavailable
	}
	return cmd.Wait()
}
