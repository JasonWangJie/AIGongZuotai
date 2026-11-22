package studio

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestChatExecutionReceivesConfiguredDeadline(t *testing.T) {
	for _, tc := range []struct {
		name             string
		configured, want time.Duration
	}{
		{"default", 0, 10 * time.Minute},
		{"long-reply", 35 * time.Minute, 35 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, server := testApp(t, Config{RunTimeout: tc.configured})
			seen := make(chan time.Time, 1)
			app.chat = chatFunc(func(ctx context.Context, _ string, emit func(string) error) error {
				deadline, _ := ctx.Deadline()
				seen <- deadline
				return emit("confirmed")
			})
			earliest := time.Now().Add(tc.want)
			status, data, _ := request(t, server, "POST", "/api/v1/runs", "run-deadline-0001", runInput())
			if status != http.StatusCreated {
				t.Fatal("run admission", status)
			}
			run := waitRun(t, server, decode[Run](t, data).ID)
			if run.Status != "COMPLETED" || run.Output != "confirmed" {
				t.Fatal("configured run did not finish normally", run.Status)
			}
			select {
			case deadline := <-seen:
				if deadline.Before(earliest) || deadline.After(time.Now().Add(tc.want)) {
					t.Fatal("provider did not receive the single-run deadline")
				}
			default:
				t.Fatal("provider was not called")
			}
		})
	}
}

func TestChatExecutionExpiresWithConfirmedPrefix(t *testing.T) {
	app, server := testApp(t, Config{RunTimeout: time.Second})
	app.chat = chatFunc(func(ctx context.Context, _ string, emit func(string) error) error {
		if err := emit("confirmed prefix"); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})
	status, data, _ := request(t, server, "POST", "/api/v1/runs", "run-deadline-0002", runInput())
	if status != http.StatusCreated {
		t.Fatal("run admission", status)
	}
	run := waitRun(t, server, decode[Run](t, data).ID)
	if run.Status != "FAILED" || run.Error == nil || run.Error.Code != "PROVIDER_STREAM_FAILED" || run.Output != "confirmed prefix" {
		t.Fatal("expiry changed the failure or lost the confirmed prefix", run.Status)
	}
}
