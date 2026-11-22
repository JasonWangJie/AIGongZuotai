package studio

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestPostgresRecoveryMailKeyCheckedBeforeGenerationAndBusiness(t *testing.T) {
	s, database := mailTestStore(t)
	smtp := newMailSMTPFixture(t, mailSMTPOptions{})
	mail, err := newMailDelivery(smtp.Config, s)
	if err != nil {
		t.Fatal(err)
	}
	item := enqueueTestMail(t, mail, testMailEnvelope())
	admin := deliveryAdmin(t, database)
	// Keep the pending mail in the future through both startups.
	if _, err = admin.Exec(context.Background(), "UPDATE studio_mail_outbox SET next_attempt_at=now()+interval '10 minutes' WHERE id=$1", item.ID); err != nil {
		t.Fatal(err)
	}
	recoverySeedRows(t, admin, 1, 1, 1, "live")
	s.close()

	cfg, media, chatCalls := recoveryFixtureConfig(t, database)
	cfg.AuthMode, cfg.Mail, cfg.mailSender = "required", smtp.Config, smtp.sender()
	cfg.Mail.OutboxKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("z", 32)))
	tables := []string{"studio_task_executor", "studio_tasks", "studio_runs", "studio_outbox_limits", "studio_outbox_events", "studio_mail_outbox"}
	before := schedulerOriginalSnapshot(t, admin, tables)
	app, err := New(cfg)
	if app != nil {
		app.Close()
		t.Fatal("startup accepted the wrong mail key")
	}
	var op *operationError
	if !errors.As(err, &op) || op.code != "MAIL_KEY_MISMATCH" {
		t.Fatalf("startup rejection did not identify a mail queue key mismatch: %T", err)
	}
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, admin, tables)) {
		t.Fatal("key mismatch changed the executor generation, lease, business records or queues")
	}
	cfg.Mail.OutboxKey = smtp.Config.OutboxKey
	app, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	recoveryAssertRows(t, admin, 1, 1)
	if media.calls.Load() != 0 || chatCalls.Load() != 0 || smtp.Connections.Load() != 0 {
		t.Fatal("key preflight or interrupted resource recovery started an external request")
	}
}
