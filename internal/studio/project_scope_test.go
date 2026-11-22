package studio

import (
	"bufio"
	"context"
	"net/http"
	"testing"
	"time"
)

func TestPostgresProjectScopeWithoutPreload(t *testing.T) {
	database := testDatabase(t)
	app, server := testApp(t, Config{DatabaseURL: database, AuthMode: "required"})
	alice, bob := authClient(t), authClient(t)
	owner := registerFixture(t, alice, server.URL, "project-scope-owner@example.test")
	other := registerFixture(t, bob, server.URL, "project-scope-other@example.test")
	// A project created after startup must be visible without populating an
	// application-wide cache. Give it the same creation time as another cold
	// row to check stable list order.
	conn := deliveryAdmin(t, database)
	const cold = "prj_scope_cold_a"
	_, err := conn.Exec(context.Background(), `INSERT INTO studio_projects(id,workspace_id,name,created_at)
VALUES ($2,$1,'cold A',now()+interval '1 minute'),('prj_scope_cold_b',$1,'cold B',now()+interval '1 minute')`, owner.Workspace.ID, cold)
	if err != nil {
		t.Fatal(err)
	}
	code, data, _ := authRequest(t, alice, server.URL, "GET", "/api/v1/projects", "", "", nil)
	projects := decode[struct{ Items []Project }](t, data).Items
	if code != 200 || len(projects) != 3 || projects[0].ID != owner.Project.ID || projects[1].ID != cold || projects[2].ID != "prj_scope_cold_b" {
		t.Fatal("workspace list did not preserve persisted membership and creation order", code)
	}
	code, data, _ = authRequest(t, alice, server.URL, "GET", "/api/v1/auth/session", "", cold, nil)
	if code != 200 || decode[testIdentity](t, data).Project.ID != cold {
		t.Fatal("session did not select the workspace's cold project")
	}
	streamURL := server.URL + "/api/v1/workspaces/" + owner.Workspace.ID + "/events?project_id=" + cold
	response, err := alice.Get(streamURL)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		_ = response.Body.Close()
		t.Fatal("owned cold project stream rejected", response.StatusCode)
	}
	name, _, _ := readSSEFixture(t, bufio.NewReader(response.Body))
	_ = response.Body.Close()
	if name != "stream.reset" {
		t.Fatal("cold project stream did not reset to its scoped cursor")
	}
	code, _, _ = authRequest(t, bob, server.URL, "GET", "/api/v1/workspaces/"+other.Workspace.ID+"/events", "", cold, nil)
	if code != http.StatusNotFound {
		t.Fatal("foreign project stream was accessible", code)
	}
	input := runInput()
	input.ProjectID = cold
	code, _, _ = authRequest(t, alice, server.URL, "POST", "/api/v1/runs", "project-scope-run", cold, input)
	if code != 201 {
		t.Fatal("could not accept generation in a cold project", code)
	}
	app.mu.Lock()
	cached := len(app.projects)
	app.mu.Unlock()
	if cached != 0 {
		t.Fatal("persistent registration, lookup or authorization cached projects", cached)
	}
}

func TestPostgresProjectLimitLocksWorkspace(t *testing.T) {
	s, database, observer := outboxStore(t)
	s.close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := observer.Exec(ctx, `INSERT INTO studio_projects(id,workspace_id,name) SELECT 'prj_limit_'||i, 'ws_demo','fixture' FROM generate_series(1,98) i`)
	if err != nil {
		t.Fatal(err)
	}
	first, second := deliveryAdmin(t, database), deliveryAdmin(t, database)
	tx, err := first.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	project := Project{ID: newID("prj"), WorkspaceID: "ws_demo", Name: "hundredth", CreatedAt: time.Now().UTC()}
	if err = insertWorkspaceProjectSQL(ctx, tx, project); err != nil {
		t.Fatal(err)
	}
	competing := project
	competing.ID = newID("prj")
	result := make(chan error, 1)
	firstPID, secondPID := first.PgConn().PID(), second.PgConn().PID()
	done := make(chan struct{})
	go func() {
		defer close(done)
		other, err := second.Begin(ctx)
		if err == nil {
			err = insertWorkspaceProjectSQL(ctx, other, competing)
			_ = other.Rollback(context.Background())
		}
		result <- err
	}()
	defer func() {
		cancel()
		_ = tx.Rollback(context.Background())
		<-done
	}()
	// Wait until PostgreSQL identifies the first transaction as the blocker,
	// then let its 100th project commit. No in-process mutex is shared here.
	for {
		var blocked bool
		err = observer.QueryRow(ctx, "SELECT $1::int=ANY(pg_blocking_pids($2::int))", firstPID, secondPID).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err = <-result:
			t.Fatal("project limit check escaped the workspace lock", err)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	requireOperation(t, <-result, "PROJECT_LIMIT")
	if outboxCount(t, observer, "SELECT count(*) FROM studio_projects WHERE workspace_id='ws_demo'") != maxWorkspaceProjects {
		t.Fatal("concurrent project creation exceeded workspace capacity")
	}
}
