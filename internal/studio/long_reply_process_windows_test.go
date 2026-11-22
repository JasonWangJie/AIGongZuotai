package studio

import (
	"errors"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Matches JOBOBJECT_BASIC_ACCOUNTING_INFORMATION in winnt.h.
type longReplyJobAccounting struct {
	totalUserTime, totalKernelTime   int64
	periodUserTime, periodKernelTime int64
	pageFaults, totalProcesses       uint32
	activeProcesses, terminated      uint32
}

func newLongReplyProcessGuard(cmd *exec.Cmd) (func() error, func() error, error) {
	// No name or inheritable handle. If the outer test is killed, the
	// kernel closes the last handle and terminates every process in the job.
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, nil, err
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		windows.CloseHandle(job)
		return nil, nil, err
	}
	attach := func() error {
		var assignErr error
		err := cmd.Process.WithHandle(func(handle uintptr) {
			assignErr = windows.AssignProcessToJobObject(job, windows.Handle(handle))
		})
		return errors.Join(err, assignErr)
	}
	stop := func() error {
		// Confirm all job members have exited before the test reopens the
		// private directories and database. Closing is the fallback on error.
		err := windows.TerminateJobObject(job, 1)
		deadline := time.Now().Add(5 * time.Second)
		for err == nil {
			var info longReplyJobAccounting
			err = windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation,
				uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
			if err != nil || info.activeProcesses == 0 {
				break
			}
			if time.Now().After(deadline) {
				err = errors.New("timed out waiting for the long reply job to exit")
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		return errors.Join(err, windows.CloseHandle(job))
	}
	return attach, stop, nil
}
