package studio

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

const sessionCookie = "studio_session"
const sessionTTL = 7 * 24 * time.Hour

type principalKey struct{}
type principal struct {
	AccountRestricted bool
	User              *User
	Workspace         Workspace
	Project           Project
	Projects          []Project
	SessionHash       []byte
	sessionDeadline   time.Time // conservative local deadline from the database clock
	SessionID         string
	CredentialVersion int64
}

func principalFrom(r *http.Request) *principal {
	p, _ := r.Context().Value(principalKey{}).(*principal)
	return p
}

const maxWorkspaceProjects = 100

func accountProjectsSQL(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, workspace string) ([]Project, error) {
	rows, err := q.Query(ctx, "SELECT id,workspace_id,name,created_at FROM studio_projects WHERE workspace_id=$1 ORDER BY created_at,id", workspace)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Project, error) {
		var p Project
		e := row.Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.CreatedAt)
		return p, e
	})
}

func (a *App) projectsFor(workspaceID string) ([]Project, error) {
	items := make([]Project, 0)
	if a.store != nil {
		err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			var e error
			items, e = accountProjectsSQL(ctx, conn, workspaceID)
			return e
		})
		return items, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range a.projects {
		if p.WorkspaceID == workspaceID {
			items = append(items, p)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items, nil
}

// Internal generation and upload paths also check the persistent identity on
// re-entry after preparing inputs. Only memory mode owns a project map.
func (a *App) projectLocked(id string) (Project, bool, error) {
	if a.store == nil {
		project, found := a.projects[id]
		return project, found, nil
	}
	var p Project
	err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		return conn.QueryRow(ctx, "SELECT id,workspace_id,name,created_at FROM studio_projects WHERE id=$1", id).Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, false, nil
	}
	return p, err == nil, err
}

func (a *App) currentPrincipal(r *http.Request) (*principal, error) {
	p := &principal{Workspace: Workspace{ID: "ws_demo", Name: "本地开发工作区"}}
	if a.config.AuthMode == "required" {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			return nil, nil
		}
		if len(cookie.Value) != 43 {
			return nil, nil
		}
		digest := sha256.Sum256([]byte(cookie.Value))
		p.SessionHash = digest[:]
		err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			var user User
			var expires, dbNow time.Time
			beforeQuery := time.Now()
			if err := conn.QueryRow(ctx, `SELECT u.id,u.email,u.name,u.email_verified_at,w.id,w.name,s.id,u.credential_version,s.expires_at,clock_timestamp(),COALESCE(ac.state,'OPEN')<>'OPEN' FROM studio_sessions s JOIN studio_users u ON u.id=s.user_id JOIN studio_workspaces w ON w.id=s.workspace_id LEFT JOIN studio_account_controls ac ON ac.user_id=u.id WHERE s.token_hash=$1 AND s.expires_at>now() AND s.credential_version=u.credential_version`, p.SessionHash).Scan(&user.ID, &user.Email, &user.Name, &user.EmailVerifiedAt, &p.Workspace.ID, &p.Workspace.Name, &p.SessionID, &p.CredentialVersion, &expires, &dbNow, &p.AccountRestricted); err != nil {
				return err
			}
			p.sessionDeadline = beforeQuery.Add(expires.Sub(dbNow))
			user.EmailVerified = user.EmailVerifiedAt != nil
			p.User = &user
			return nil
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	if p.AccountRestricted {
		return p, nil
	}
	var err error
	p.Projects, err = a.projectsFor(p.Workspace.ID)
	if err != nil {
		return nil, err
	}
	if len(p.Projects) == 0 {
		return nil, failure(503, "PROJECTS_UNAVAILABLE", "工作区没有可用项目")
	}
	p.Project = p.Projects[0]
	requested := r.Header.Get("X-Project-ID")
	query := r.URL.Query().Get("project_id")
	if requested != "" && query != "" && requested != query {
		return nil, failure(400, "PROJECT_CONFLICT", "项目请求头与查询参数不一致")
	}
	if requested == "" {
		requested = query
	}
	if requested != "" {
		found := false
		for _, project := range p.Projects {
			if project.ID == requested {
				p.Project = project
				found = true
				break
			}
		}
		if !found {
			return nil, failure(404, "PROJECT_NOT_FOUND", "项目不存在或不属于当前工作区")
		}
	}
	return p, nil
}

func (a *App) authorizeProject(p *principal, projectID string) bool {
	if p == nil {
		return false
	}
	for _, project := range p.Projects {
		if project.ID == projectID {
			return true
		}
	}
	return false
}

func (a *App) identityJSON(p *principal) map[string]any {
	if p != nil && p.AccountRestricted {
		return map[string]any{"auth_enabled": true, "account_restricted": true, "user": p.User, "account_security": a.accountSecurityCapabilities()}
	}
	result := map[string]any{"account_restricted": false, "auth_enabled": a.config.AuthMode == "required", "user": nil, "account_security": a.accountSecurityCapabilities(), "operations": a.operationsCapabilities(p)}
	if p != nil {
		result["user"] = p.User
		result["workspace"] = p.Workspace
		result["project"] = p.Project
		result["projects"] = p.Projects
	}
	return result
}
func (a *App) authSession(w http.ResponseWriter, r *http.Request) {
	p, err := a.currentPrincipal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, a.identityJSON(p))
}

type authInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

func normalizeEmail(email string) (string, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	parsed, err := mail.ParseAddress(email)
	return email, err == nil && parsed.Address == email && utf8.ValidString(email) && !strings.ContainsRune(email, 0) && len(email) <= 254 && strings.Contains(email, "@")
}
func normalizeDeliveryEmail(email string) (string, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	_, err := strictMailbox(email)
	return email, err == nil && strings.Contains(email, "@")
}
func validName(name string) bool {
	return name != "" && !strings.ContainsRune(name, 0) && utf8.ValidString(name) && utf8.RuneCountInString(name) <= 80
}
func tokenPair() (string, []byte, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, err
	}
	raw := base64.RawURLEncoding.EncodeToString(b[:])
	digest := sha256.Sum256([]byte(raw))
	return raw, digest[:], nil
}
func setSession(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds()), Expires: time.Now().Add(sessionTTL)})
}

func (a *App) authRegister(w http.ResponseWriter, r *http.Request) {
	if a.config.AuthMode != "required" {
		writeError(w, failure(409, "AUTH_DISABLED", "当前本地模式未启用账户认证"))
		return
	}
	var in authInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	var valid bool
	in.Email, valid = normalizeDeliveryEmail(in.Email)
	in.Name = strings.TrimSpace(in.Name)
	if !valid || !validName(in.Name) || !validAccountPassword(in.Password) {
		writeError(w, failure(422, "INVALID_REGISTRATION", "请提供有效邮箱、1至80字符名称和10至72字节密码"))
		return
	}
	if !a.authAdmission(r, "register", in.Email) {
		writeError(w, failure(429, "AUTH_RATE_LIMIT", "注册请求过多，请15分钟后重试"))
		return
	}
	select {
	case a.bcryptSlots <- struct{}{}:
		defer func() { <-a.bcryptSlots }()
	default:
		writeError(w, failure(429, "AUTH_BUSY", "认证处理繁忙，请稍后重试"))
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), 12)
	if err != nil {
		writeError(w, err)
		return
	}
	token, digest, err := tokenPair()
	if err != nil {
		writeError(w, err)
		return
	}
	user := User{ID: newID("usr"), Email: in.Email, Name: in.Name}
	workspace := Workspace{ID: newID("ws"), Name: in.Name + "的工作区"}
	project := Project{ID: newID("prj"), WorkspaceID: workspace.ID, Name: "我的第一个项目", CreatedAt: time.Now().UTC()}
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, "INSERT INTO studio_users(id,email,name,password_hash) VALUES($1,$2,$3,$4)", user.ID, user.Email, user.Name, hash); err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" {
				return failure(409, "EMAIL_REGISTERED", "该邮箱已注册")
			}
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO studio_workspaces(id,owner_id,name) VALUES($1,$2,$3)", workspace.ID, user.ID, workspace.Name); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO studio_projects(id,workspace_id,name,created_at) VALUES($1,$2,$3,$4)", project.ID, workspace.ID, project.Name, project.CreatedAt); err != nil {
			return err
		}
		if err = insertAuditSQL(ctx, tx, newAuditEvent(workspace.ID, project.ID, user.ID, "project.created", "project", project.ID, AuditDetails{})); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO studio_sessions(token_hash,user_id,workspace_id,expires_at) VALUES($1,$2,$3,$4)", digest, user.ID, workspace.ID, time.Now().Add(sessionTTL)); err != nil {
			return err
		}
		if err = insertSecurityEventSQL(ctx, tx, user.ID, "account.registered"); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	setSession(w, r, token)
	writeJSON(w, 201, a.identityJSON(&principal{User: &user, Workspace: workspace, Project: project, Projects: []Project{project}}))
}

// The fixed dummy hash keeps an unknown-email attempt on the bcrypt path.
const dummyPasswordHash = "$2a$12$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW"

func (a *App) authLogin(w http.ResponseWriter, r *http.Request) {
	if a.config.AuthMode != "required" {
		writeError(w, failure(409, "AUTH_DISABLED", "当前本地模式未启用账户认证"))
		return
	}
	var in authInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	email, valid := normalizeEmail(in.Email)
	if !valid || len(in.Password) < 1 || len(in.Password) > 72 {
		writeError(w, failure(401, "INVALID_CREDENTIALS", "邮箱或密码错误"))
		return
	}
	if !a.authAdmission(r, "login", email) {
		writeError(w, failure(429, "AUTH_RATE_LIMIT", "登录尝试过多，请15分钟后重试"))
		return
	}
	select {
	case a.bcryptSlots <- struct{}{}:
		defer func() { <-a.bcryptSlots }()
	default:
		writeError(w, failure(429, "AUTH_BUSY", "认证处理繁忙，请稍后重试"))
		return
	}
	var user User
	var workspace Workspace
	var hash []byte
	var version int64
	err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		return conn.QueryRow(ctx, "SELECT u.id,u.email,u.name,u.email_verified_at,u.password_hash,u.credential_version,w.id,w.name FROM studio_users u JOIN studio_workspaces w ON w.owner_id=u.id WHERE u.email=$1 ORDER BY w.created_at LIMIT 1", email).Scan(&user.ID, &user.Email, &user.Name, &user.EmailVerifiedAt, &hash, &version, &workspace.ID, &workspace.Name)
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, err)
		return
	}
	if len(hash) == 0 {
		hash = []byte(dummyPasswordHash)
	}
	passwordErr := bcrypt.CompareHashAndPassword(hash, []byte(in.Password))
	if err != nil || passwordErr != nil {
		writeError(w, failure(401, "INVALID_CREDENTIALS", "邮箱或密码错误"))
		return
	}
	user.EmailVerified = user.EmailVerifiedAt != nil
	var projects []Project
	token, digest, err := tokenPair()
	if err != nil {
		writeError(w, err)
		return
	}
	var restricted bool
	err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = insertLoginSessionSQL(ctx, tx, user.ID, workspace.ID, hash, version, digest); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_account_controls WHERE user_id=$1 AND state<>'OPEN')", user.ID).Scan(&restricted); err != nil {
			return err
		}
		if !restricted {
			projects, err = accountProjectsSQL(ctx, tx, workspace.ID)
			if err != nil {
				return err
			}
			if len(projects) == 0 {
				return failure(503, "PROJECTS_UNAVAILABLE", "工作区项目不可用")
			}
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	setSession(w, r, token)
	p := &principal{AccountRestricted: restricted, User: &user, Workspace: workspace, Projects: projects}
	if len(projects) > 0 {
		p.Project = projects[0]
	}
	writeJSON(w, 200, a.identityJSON(p))
}

// The user row serializes password changes with session creation. Expensive bcrypt
// is already finished; this short transaction must still compare its snapshot.
func insertLoginSessionSQL(ctx context.Context, tx pgx.Tx, userID, workspaceID string, verifiedHash []byte, verifiedVersion int64, digest []byte) error {
	var hash []byte
	var version int64
	if err := tx.QueryRow(ctx, "SELECT password_hash,credential_version FROM studio_users WHERE id=$1 FOR UPDATE", userID).Scan(&hash, &version); err != nil {
		return err
	}
	if version != verifiedVersion || !bytes.Equal(hash, verifiedHash) {
		return failure(401, "INVALID_CREDENTIALS", "邮箱或密码错误")
	}
	if _, err := tx.Exec(ctx, "DELETE FROM studio_sessions WHERE user_id=$1 AND (expires_at<=now() OR token_hash IN (SELECT token_hash FROM studio_sessions WHERE user_id=$1 ORDER BY created_at DESC,id DESC OFFSET 9))", userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO studio_sessions(token_hash,user_id,workspace_id,expires_at,credential_version) VALUES($1,$2,$3,$4,$5)", digest, userID, workspaceID, time.Now().Add(sessionTTL), version); err != nil {
		return err
	}
	return insertSecurityEventSQL(ctx, tx, userID, "account.login")
}

func (a *App) authLogout(w http.ResponseWriter, r *http.Request) {
	if a.store != nil {
		if cookie, err := r.Cookie(sessionCookie); err == nil {
			digest := sha256.Sum256([]byte(cookie.Value))
			if err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
				tx, e := conn.Begin(ctx)
				if e != nil {
					return e
				}
				defer tx.Rollback(ctx)
				var userID string
				e = tx.QueryRow(ctx, "SELECT user_id FROM studio_sessions WHERE token_hash=$1", digest[:]).Scan(&userID)
				if errors.Is(e, pgx.ErrNoRows) {
					return nil
				}
				if e != nil {
					return e
				}
				if _, e = readPasswordSnapshot(ctx, tx, userID, true); e != nil {
					return e
				}
				deleted, e := tx.Exec(ctx, "DELETE FROM studio_sessions WHERE token_hash=$1", digest[:])
				if e != nil {
					return e
				}
				if deleted.RowsAffected() == 0 {
					return tx.Commit(ctx)
				}
				if e = insertSecurityEventSQL(ctx, tx, userID, "account.logout"); e != nil {
					return e
				}
				return tx.Commit(ctx)
			}); err != nil {
				writeError(w, err)
				return
			}
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) postProject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, err)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !validName(in.Name) {
		writeError(w, failure(422, "INVALID_PROJECT_NAME", "项目名称需要1至80字符"))
		return
	}
	p := principalFrom(r)
	project := Project{ID: newID("prj"), WorkspaceID: p.Workspace.ID, Name: in.Name, CreatedAt: time.Now().UTC()}
	audit := newAuditEvent(project.WorkspaceID, project.ID, auditActorID(r), "project.created", "project", project.ID, AuditDetails{})
	if a.store != nil {
		if err := a.store.saveWithAudit(func(ctx context.Context, tx pgx.Tx) error {
			return insertWorkspaceProjectSQL(ctx, tx, project)
		}, []AuditEvent{audit}); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 201, project)
		return
	}
	a.mu.Lock()
	count := 0
	for _, project := range a.projects {
		if project.WorkspaceID == p.Workspace.ID {
			count++
		}
	}
	if count >= maxWorkspaceProjects {
		a.mu.Unlock()
		writeError(w, failure(409, "PROJECT_LIMIT", "当前工作区最多100个项目"))
		return
	}
	a.projects[project.ID] = project
	a.appendMemoryAuditLocked(audit)
	a.mu.Unlock()
	writeJSON(w, 201, project)
}

func insertWorkspaceProjectSQL(ctx context.Context, tx pgx.Tx, p Project) error {
	// Lock the stable parent before counting under READ COMMITTED. Competing
	// inserts see the winner's project once they acquire the same lock.
	var workspace string
	if err := tx.QueryRow(ctx, "SELECT id FROM studio_workspaces WHERE id=$1 FOR NO KEY UPDATE", p.WorkspaceID).Scan(&workspace); err != nil {
		return err
	}
	var full bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_projects WHERE workspace_id=$1 OFFSET $2)", workspace, maxWorkspaceProjects-1).Scan(&full); err != nil {
		return err
	}
	if full {
		return failure(409, "PROJECT_LIMIT", "当前工作区最多100个项目")
	}
	_, err := tx.Exec(ctx, "INSERT INTO studio_projects(id,workspace_id,name,created_at) VALUES($1,$2,$3,$4)", p.ID, workspace, p.Name, p.CreatedAt)
	return err
}

type loginWindow struct {
	Count int
	Until time.Time
}
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]loginWindow
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{entries: make(map[string]loginWindow)} }
func (l *loginLimiter) allow(key string, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for k, v := range l.entries {
		if now.After(v.Until) {
			delete(l.entries, k)
		}
	}
	v, exists := l.entries[key]
	if !exists {
		if len(l.entries) >= 2048 {
			return false
		}
		v.Until = now.Add(15 * time.Minute)
	}
	if v.Count >= limit {
		return false
	}
	v.Count++
	l.entries[key] = v
	return true
}
func (a *App) authAdmission(r *http.Request, operation, email string) bool {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	limit := 30
	if operation == "register" {
		limit = 10
	}
	digest := sha256.Sum256([]byte(email))
	key := operation + ":" + base64.RawURLEncoding.EncodeToString(digest[:])
	return a.limiter.allow(operation+":ip:"+ip, limit) && a.limiter.allow(key, 10)
}
