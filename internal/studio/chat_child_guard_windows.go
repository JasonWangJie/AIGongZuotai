package studio

import (
	"errors"
	"golang.org/x/sys/windows"
	"os/exec"
	"syscall"
	"unsafe"
)

func chatChildGuard(cmd *exec.Cmd) (func() error, func(), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, nil, err
	}
	closeJob := func() { windows.CloseHandle(job) }
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		closeJob()
		return nil, nil, err
	}
	attach := func() error {
		var assignErr error
		handleErr := cmd.Process.WithHandle(func(handle uintptr) { assignErr = windows.AssignProcessToJobObject(job, windows.Handle(handle)) })
		return errors.Join(handleErr, assignErr)
	}
	return attach, closeJob, nil
}
