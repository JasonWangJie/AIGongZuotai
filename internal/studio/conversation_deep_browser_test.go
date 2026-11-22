package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type deepBrowserInput struct {
	URL          string   `json:"url"`
	Email        string   `json:"email"`
	Password     string   `json:"password"`
	Project      string   `json:"project"`
	Conversation string   `json:"conversation"`
	Title        string   `json:"title"`
	Leaf         string   `json:"leaf"`
	Turns        int      `json:"turns"`
	Digests      []string `json:"digests"`
}

type deepBrowserMeasurement struct {
	UniquePages      int   `json:"unique_authenticated_pages"`
	Indexed          int   `json:"indexed_nodes"`
	MaxMounted       int   `json:"max_sampled_mounted_nodes"`
	MaxResident      int   `json:"max_sampled_resident_bodies"`
	MaxResidentBytes int   `json:"max_sampled_resident_bytes"`
	Revisited        int   `json:"evicted_page_revisits"`
	GenerationPosts  int   `json:"generation_posts"`
	ElapsedMS        int64 `json:"elapsed_ms"`
}

func deepRunBrowser(t *testing.T, input deepBrowserInput) *deepBrowserMeasurement {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/conversation-deep-browser.mjs")
	cmd.Dir = filepath.Join("..", "..")
	for _, key := range []string{"PATH", "SystemRoot", "USERPROFILE", "LOCALAPPDATA", "HOME", "TMPDIR", "TEMP", "TMP", "WORKFLOW_CHROME", "LD_LIBRARY_PATH", "XDG_RUNTIME_DIR"} {
		if s, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+s)
		}
	}
	inputPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inputPipe.Close()
	attach, stop, err := longReplyProcessGuard(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
	}()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// No JSON or EOF before containment is active. The child cannot launch
	// Chromium until it has received and checked the complete fixture.
	if err := attach(); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal(err)
	}
	sendErr := json.NewEncoder(inputPipe).Encode(input)
	closeErr := inputPipe.Close()
	err = cmd.Wait()
	if sendErr != nil || closeErr != nil || err != nil {
		var fault struct {
			Phase string
			Step  int
		}
		_ = json.Unmarshal(stderr.Bytes(), &fault)
		switch fault.Phase {
		case "setup", "login", "pages", "revisit", "report":
		default:
			fault.Phase = "driver"
		}
		t.Fatalf("deep browser failed phase=%s step=%d", fault.Phase, fault.Step)
	}
	var report deepBrowserMeasurement
	err = json.Unmarshal(stdout.Bytes(), &report)
	if err != nil || report.UniquePages != len(input.Digests) || report.Indexed != input.Turns*2 ||
		report.Revisited != 3 || report.GenerationPosts != 0 || report.MaxMounted < 1 || report.MaxMounted > 24 ||
		report.MaxResident < 1 || report.MaxResident > 120 || report.MaxResidentBytes < 1 || report.MaxResidentBytes > 8<<20 || report.ElapsedMS <= 0 {
		t.Fatal("incomplete deep browser evidence")
	}
	return &report
}
