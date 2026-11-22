package studio

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestPostgresMediaAdmissionWithFullForeignCache(t *testing.T) {
	const backlog = 512
	db := testDatabase(t)
	cfg := Config{DatabaseURL: db, AuthMode: "required", PollInterval: time.Hour,
		AssetDir: t.TempDir(), ReferenceDir: t.TempDir(),
		Admission: AdmissionLimits{WorkspaceTasks: 1, ModelTasks: 1}}
	seed, seedHTTP := testApp(t, cfg)
	alice, bob := authClient(t), authClient(t)
	a := registerFixture(t, alice, seedHTTP.URL, "backlog@example.test")
	b := registerFixture(t, bob, seedHTTP.URL, "create@example.test")
	seedHTTP.Close()
	seed.Close()

	// Model a backlog that was accepted under an older, higher workspace quota.
	// Every row has an original upstream handle and remains eligible for GET later.
	admin := deliveryAdmin(t, db)
	_, err := admin.Exec(context.Background(), `INSERT INTO studio_tasks(id,workspace_id,project_id,document,input,handle,next_poll)
SELECT id,$1,$2,jsonb_build_object('id',id,'workspace_id',$1::text,'project_id',$2::text,'model_id','studio-image','kind','image','provider_mode','demo',
'status','RUNNING','version',2,'created_at',now(),'updated_at',now()),
jsonb_build_object('project_id',$2::text,'kind','image','model_id','studio-image','inputs',jsonb_build_object('prompt','fixture')),
jsonb_build_object('ID','original-'||id,'Kind','image','Count',1),now()+interval '1 day'
FROM (SELECT 'task_full_cache_'||i AS id FROM generate_series(1,$3::int) i) AS seeded`, a.Workspace.ID, a.Project.ID, backlog)
	if err != nil {
		t.Fatal(err)
	}
	app, server := testApp(t, cfg)
	checkCache := func() {
		t.Helper()
		app.mu.Lock()
		defer app.mu.Unlock()
		if len(app.tasks) != backlog {
			t.Fatalf("cache contains %d records, want %d", len(app.tasks), backlog)
		}
		for _, record := range app.tasks {
			if record.Task.WorkspaceID != a.Workspace.ID || record.Task.Status != "RUNNING" || record.Handle.ID != "original-"+record.Task.ID {
				t.Fatal("parked record replaced, changed, or missing its original handle")
			}
		}
	}
	checkCache()
	request := taskInput("image")
	request.ProjectID = b.Project.ID
	code, body, _ := uploadReferenceFixture(t, bob, server.URL, b.Project.ID, "full-cache-reference", "edit.png", referencePNG(t, 9), false)
	if code != 201 {
		t.Fatal("reference upload failed", code)
	}
	ref := decode[ReferenceAsset](t, body)
	for _, operation := range []string{"generate", "edit"} {
		in := request
		in.Operation = operation
		if operation == "edit" {
			in.Inputs.ReferenceAssetIDs = []string{ref.ID}
		}
		key := "full-cache-" + operation
		code, body, _ = authRequest(t, bob, server.URL, "POST", "/api/v1/tasks", key, b.Project.ID, in)
		if code != 202 {
			t.Fatal(operation, "was not accepted", code)
		}
		task := decode[Task](t, body)
		if task.Status != "QUEUED" || task.WorkspaceID != b.Workspace.ID || task.Operation != operation {
			t.Fatal("new task changed scope or operation")
		}
		checkCache()
		code, body, headers := authRequest(t, bob, server.URL, "POST", "/api/v1/tasks", key, b.Project.ID, in)
		if code != 202 || headers.Get("Idempotency-Replayed") != "true" || decode[Task](t, body).ID != task.ID {
			t.Fatal("durable replay failed", code)
		}
		code, body, _ = authRequest(t, bob, server.URL, "POST", "/api/v1/tasks", key+"-over-quota", b.Project.ID, in)
		if code != 429 || !bytes.Contains(body, []byte("ADMISSION_WORKSPACE_LIMIT")) {
			t.Fatal("uncached task released workspace quota", code)
		}
		path := "/api/v1/tasks/" + task.ID
		code, _, _ = authRequest(t, alice, server.URL, "GET", path, "", a.Project.ID, nil)
		if code != 404 {
			t.Fatal("foreign cold read was visible", code)
		}
		code, body, _ = authRequest(t, bob, server.URL, "GET", path, "", b.Project.ID, nil)
		if code != 200 || decode[Task](t, body).Version != task.Version {
			t.Fatal("cold snapshot disagrees with acceptance", code)
		}
		code, body, _ = authRequest(t, bob, server.URL, "POST", path+"/cancel", "", b.Project.ID, nil)
		if code != 200 || decode[Task](t, body).Status != "CANCELLED" {
			t.Fatal("cold cancel failed", code)
		}
		code, body, _ = authRequest(t, bob, server.URL, "GET", path, "", b.Project.ID, nil)
		cancelled := decode[Task](t, body)
		if code != 200 || cancelled.Version != task.Version+1 || cancelled.Status != "CANCELLED" {
			t.Fatal("cancellation did not persist", code)
		}
		snap, err := app.admissionSnapshot(b.Workspace.ID)
		if err != nil || snap.Tasks.Active != 0 {
			t.Fatal("cancelled task still counted", snap, err)
		}
		checkCache()
	}
	// A cold admission must also be visible to the production PostgreSQL claimant.
	code, body, _ = authRequest(t, bob, server.URL, "POST", "/api/v1/tasks", "full-cache-dispatch", b.Project.ID, request)
	if code != 202 {
		t.Fatal("could not accept cold task for dispatch", code)
	}
	queued := decode[Task](t, body)
	checkCache()
	advanceMediaScheduleDue(t, app, queued.ID)
	if !app.executePersistentTaskLane(mediaSubmitLane, queued.ID) {
		t.Fatal("durable worker did not claim the cold task")
	}
	stored, err := app.store.loadTask(queued.ID)
	if err != nil || stored.Handle.ID == "" || mediaLaneForTask(stored) != mediaQueryLane {
		t.Fatal("cold task did not persist its original upstream handle", err)
	}
	if n := outboxCount(t, admin, `SELECT count(*) FROM studio_tasks WHERE workspace_id=$1 AND document->>'status'='RUNNING' AND handle->>'ID'='original-'||id`, a.Workspace.ID); n != backlog {
		t.Fatal("original backlog changed", n)
	}
}

func TestMemoryMediaAdmissionAtRecordLimit(t *testing.T) {
	_, server := testApp(t, Config{MaxRecords: 1, PollInterval: time.Hour})
	in := taskInput("image")
	code, body, _ := request(t, server, "POST", "/api/v1/tasks", "memory-cache-first", in)
	if code != 202 {
		t.Fatal("first memory task was not accepted", code)
	}
	first := decode[Task](t, body)
	code, body, _ = request(t, server, "POST", "/api/v1/tasks", "memory-cache-next", in)
	if code != 503 || !bytes.Contains(body, []byte("LOCAL_STORE_FULL")) {
		t.Fatal("memory admitted a new record past its limit", code)
	}
	code, body, headers := request(t, server, "POST", "/api/v1/tasks", "memory-cache-first", in)
	if code != 202 || headers.Get("Idempotency-Replayed") != "true" || decode[Task](t, body).ID != first.ID {
		t.Fatal("full memory store lost the original replay", code)
	}
}
