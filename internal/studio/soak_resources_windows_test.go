package studio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	soakKernel256      = windows.NewLazySystemDLL("kernel32.dll")
	soakMemoryInfo256  = soakKernel256.NewProc("K32GetProcessMemoryInfo")
	soakHandleCount256 = soakKernel256.NewProc("GetProcessHandleCount")
)

// PROCESS_MEMORY_COUNTERS uses two DWORDs followed by eight SIZE_T fields.
type soakWindowsMemoryCounters256 struct {
	size, pageFaults               uint32
	peakWorkingSet, workingSet     uintptr
	peakPagedPool, pagedPool       uintptr
	peakNonpagedPool, nonpagedPool uintptr
	pagefile, peakPagefile         uintptr
}

func soakProcessSample256(handle windows.Handle) (soakProcessResources256, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return soakProcessResources256{}, fmt.Errorf("GetProcessTimes: %w", err)
	}
	ticks := func(t windows.Filetime) uint64 { return uint64(t.HighDateTime)<<32 | uint64(t.LowDateTime) }
	var memory soakWindowsMemoryCounters256
	memory.size = uint32(unsafe.Sizeof(memory))
	ok, _, err := soakMemoryInfo256.Call(uintptr(handle), uintptr(unsafe.Pointer(&memory)), uintptr(memory.size))
	if ok == 0 {
		return soakProcessResources256{}, fmt.Errorf("K32GetProcessMemoryInfo: %w", err)
	}
	var handles uint32
	ok, _, err = soakHandleCount256.Call(uintptr(handle), uintptr(unsafe.Pointer(&handles)))
	if ok == 0 {
		return soakProcessResources256{}, fmt.Errorf("GetProcessHandleCount: %w", err)
	}
	return soakProcessResources256{
		CPUMS: (ticks(kernel) + ticks(user)) / 10000, ResidentBytes: uint64(memory.workingSet), HandleCount: handles,
	}, nil
}

func soakResourceSample256(service *childAcceptanceServer) (soakResourcePair256, error) {
	if service == nil || service.proc == nil || service.proc.Process == nil {
		return soakResourcePair256{}, errors.New("owned service has not started")
	}
	var sample soakResourcePair256
	var sampleErr error
	handleErr := service.proc.Process.WithHandle(func(raw uintptr) {
		handle := windows.Handle(raw)
		sample.Service, sampleErr = soakProcessSample256(handle)
		if sampleErr != nil {
			return
		}
		sample.Driver, sampleErr = soakProcessSample256(windows.CurrentProcess())
		if sampleErr != nil {
			return
		}
		// WithHandle pins the original process object even through exit and
		// PID reuse. Accept both samples only if that service is still alive.
		var state uint32
		state, sampleErr = windows.WaitForSingleObject(handle, 0)
		if sampleErr == nil && state != uint32(windows.WAIT_TIMEOUT) {
			sampleErr = os.ErrProcessDone
		}
	})
	if err := errors.Join(handleErr, sampleErr); err != nil {
		return soakResourcePair256{}, err
	}
	return sample, nil
}

func TestSoakOwnedResources256(t *testing.T) {
	if _, err := soakResourceSample256(nil); err == nil {
		t.Fatal("accepted missing service")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSoakResourceHelper256$", "--", "owned-resource-child")
	cmd.Dir = t.TempDir()
	cmd.Env = childFixtureEnvironment(nil)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	service := &childAcceptanceServer{proc: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(service.done) }()
	defer service.crash(t)
	var ready [1]byte
	if _, err := io.ReadFull(out, ready[:]); err != nil || ready[0] != 1 {
		t.Fatal("owned sample process did not signal readiness")
	}
	a, err := soakResourceSample256(service)
	if err != nil {
		t.Fatal(err)
	}
	b, err := soakResourceSample256(service)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []soakProcessResources256{a.Service, a.Driver, b.Service, b.Driver} {
		if p.ResidentBytes == 0 || p.HandleCount == 0 {
			t.Fatal("missing working set or open handle count")
		}
	}
	if b.Service.CPUMS < a.Service.CPUMS || b.Driver.CPUMS < a.Driver.CPUMS {
		t.Fatal("cumulative process CPU time decreased")
	}
	service.crash(t)
	if sample, err := soakResourceSample256(service); err == nil || sample != (soakResourcePair256{}) {
		t.Fatal("accepted an exited service as a live window sample")
	}
}

func TestSoakResourceHelper256(t *testing.T) {
	if os.Args[len(os.Args)-1] != "owned-resource-child" {
		return
	}
	if _, err := os.Stdout.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}
