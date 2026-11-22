package studio

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func longReplyFixtureArgs(role, dir string) []string {
	return []string{"-test.run=^TestLongReplyProcessFixture$", "--", "long-reply-fixture", role, dir}
}

func TestLongReplyProcessFixture(t *testing.T) {
	args := os.Args
	if len(args) < 4 || args[len(args)-4] != "--" || args[len(args)-3] != "long-reply-fixture" {
		t.Skip("subprocess helper")
	}
	role, dir := args[len(args)-2], args[len(args)-1]
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if role == "owner" {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		driver := exec.CommandContext(ctx, executable, longReplyFixtureArgs("driver", dir)...)
		start, err := driver.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		defer start.Close()
		attach, stop, err := longReplyProcessGuard(driver)
		if err != nil {
			t.Fatal(err)
		}
		defer stop()
		if err := driver.Start(); err != nil {
			t.Fatal(err)
		}
		if err := attach(); err != nil {
			driver.Process.Kill()
			driver.Wait()
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(start, "start"); err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			t.Fatal("missing owner instruction")
		}
		switch scanner.Text() {
		case "close":
			if err := stop(); err != nil {
				t.Fatal(err)
			}
		case "cancel":
			cancel()
		default:
			t.Fatal("unexpected owner instruction")
		}
		var exit *exec.ExitError
		if !errors.As(driver.Wait(), &exit) {
			t.Fatal("driver did not terminate due to the guard")
		}
		return
	}
	if role == "driver" {
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() || scanner.Text() != "start" {
			return
		}
	} else if role != "leaf" {
		t.Fatal("invalid fixture role")
	}
	lock := flock.New(filepath.Join(dir, role+".lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	if role == "driver" {
		leaf := exec.Command(executable, longReplyFixtureArgs("leaf", dir)...)
		if err := leaf.Start(); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	// Both descendants ignore stdin. A separate lifetime cap prevents even
	// a broken guard from leaving a lasting fixture on the host.
	time.Sleep(20 * time.Second)
	t.Fatal("fixture exceeded its lifetime")
}

func TestLongReplyProcessGuardStopsDescendants(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process guard unavailable")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"close", "cancel", "kill-owner"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			owner := exec.Command(executable, longReplyFixtureArgs("owner", dir)...)
			input, err := owner.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			if err := owner.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if owner.ProcessState == nil {
					owner.Process.Kill()
					owner.Wait()
				}
			}()
			deadline := time.Now().Add(8 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("driver and leaf failed to become ready")
				}
				time.Sleep(10 * time.Millisecond)
			}
			locks := []*flock.Flock{flock.New(filepath.Join(dir, "driver.lock")), flock.New(filepath.Join(dir, "leaf.lock"))}
			for _, lock := range locks {
				held, err := lock.TryLock()
				if err != nil || held {
					lock.Unlock()
					t.Fatalf("missing descendant lock before shutdown: %v", err)
				}
			}
			if mode == "kill-owner" {
				if err := owner.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				var exit *exec.ExitError
				if !errors.As(owner.Wait(), &exit) {
					t.Fatal("owner did not terminate forcibly")
				}
			} else {
				if _, err := fmt.Fprintln(input, mode); err != nil {
					t.Fatal(err)
				}
				if err := owner.Wait(); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			for _, lock := range locks {
				held, err := lock.TryLockContext(ctx, 10*time.Millisecond)
				if err != nil || !held {
					t.Fatalf("descendant still owns its lock after shutdown: %v", err)
				}
				if err := lock.Unlock(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
