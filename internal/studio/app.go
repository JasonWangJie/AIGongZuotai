package studio

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"aigongzuotai/internal/localeip"
	"aigongzuotai/internal/videoexport"

	"github.com/jackc/pgx/v5"
)

type Config struct {
	queryTiming                                                                                             *mediaQueryTiming
	LocaleTrustedProxies                                                                                    string
	RunDeltaBatching                                                                                        bool
	LongConversationHistory                                                                                 bool
	RunReplayReadConnections                                                                                int
	OrderCatalogFile                                                                                        string
	VideoExport                                                                                             videoexport.Config
	MediaDelivery                                                                                           MediaDeliveryConfig
	MediaScheduling                                                                                         MediaSchedulingConfig
	ChatScheduling                                                                                          ChatSchedulingConfig
	ChatChildren                                                                                            ChatChildConfig
	MediaModeration                                                                                         MediaModerationConfig
	Moderation                                                                                              ModerationConfig
	OperationsEnabled                                                                                       bool
	BillingMode, BillingPriceFile                                                                           string
	Mail                                                                                                    MailConfig
	mailSender                                                                                              mailSender // In-package TLS SMTP fixtures only; no runtime switch.
	RunLeaseDuration                                                                                        time.Duration
	TaskLeaseDuration                                                                                       time.Duration
	AssetStorage                                                                                            AssetStorageConfig
	ReferenceDir                                                                                            string
	ModelCatalogFile                                                                                        string
	Admission                                                                                               AdmissionLimits
	MetricsToken                                                                                            string
	mediaProvider                                                                                           MediaProvider // Deterministic in-package fixtures; never configured by an environment flag.
	DatabaseURL, AuthMode                                                                                   string
	AsyncImageBaseURL, AsyncImageAPIKey, AsyncImageModel, AsyncVideoModel, AsyncImageCredentialID, AssetDir string
	AsyncVideoSeconds                                                                                       int
	AsyncVideoResolution                                                                                    string
	AllowedAssetHosts                                                                                       []string
	Mode, BaseURL, APIKey, ChatModel, WebDir                                                                string
	RunWorkers, TaskWorkers, QueueSize, MaxRecords, ReplaySize, SubscriberBuffer                            int
	DemoTokenDelay, DemoTaskDuration, PollInterval, Heartbeat, RunTimeout                                   time.Duration
}

func (c *Config) defaults() {
	if c.BillingMode == "" {
		c.BillingMode = "disabled"
	}
	if c.RunLeaseDuration == 0 {
		c.RunLeaseDuration = 30 * time.Second
	}
	if c.TaskLeaseDuration == 0 {
		c.TaskLeaseDuration = 30 * time.Second
	}
	if c.ReferenceDir == "" {
		c.ReferenceDir = ".runtime/references"
	}
	c.Admission.defaults()
	if c.AuthMode == "" {
		c.AuthMode = "disabled"
	}
	if c.Mode == "" {
		c.Mode = "demo"
	}
	if c.WebDir == "" {
		c.WebDir = "apps/workbench/dist"
	}
	if c.RunWorkers <= 0 {
		c.RunWorkers = 4
	}
	if c.TaskWorkers <= 0 {
		c.TaskWorkers = 4
	}
	if c.QueueSize <= 0 {
		c.QueueSize = 32
	}
	if c.MaxRecords <= 0 {
		c.MaxRecords = 512
	}
	if c.ReplaySize <= 0 {
		c.ReplaySize = 256
	}
	if c.SubscriberBuffer <= 0 {
		c.SubscriberBuffer = 64
	}
	if c.DemoTokenDelay <= 0 {
		c.DemoTokenDelay = 55 * time.Millisecond
	}
	if c.DemoTaskDuration <= 0 {
		c.DemoTaskDuration = 6 * time.Second
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 500 * time.Millisecond
	}
	if c.Heartbeat <= 0 {
		c.Heartbeat = 15 * time.Second
	}
	if c.RunTimeout <= 0 {
		c.RunTimeout = 10 * time.Minute
	}
}

type runRecord struct {
	leaseWatch     *runLeaseWatch
	streamWrite    chan struct{} // Start, append or checkpoint in progress outside App.mu.
	streamCancel   chan struct{} // A cancel waiting for that write takes precedence over another append.
	ChatExecution  *chatExecution
	UpstreamModel  string
	CheckpointSeq  uint64
	LastCheckpoint time.Time
	Run            Run
	Events         []event
	EventStart     int
	Output         strings.Builder
	Subscribers    map[chan event]bool
	streamReaders  int // Counts open SSE handlers, including a slow reader whose channel was closed.
	Cancel         context.CancelFunc
}
type taskRecord struct {
	sourceVideo       *timelineVideoAsset // admission only; durable provenance lives in studio_task_video_sources
	ScheduleReconcile bool
	Schedule          *mediaTaskSchedule
	ScheduleOutcome   *mediaScheduleOutcome
	ScheduleClaim     *mediaScheduleCandidate
	ReviewDispatch    *MediaReviewDispatch
	Delivery          *MediaDeliveryRecord
	MediaModeration   *MediaModerationRecord
	Lease             *TaskLease
	LeaseComplete     bool
	LeaseStatus       string
	Submission        *MediaSubmission
	Task              Task
	Input             TaskInput
	Handle            MediaHandle
	NextPoll          time.Time
	InFlight          bool
}
type idemRecord struct {
	Hash [32]byte
	ID   string
}

// App owns all in-memory resources and a fixed number of execution workers.
// HTTP subscribers never own or restart generation; disconnecting is harmless.
type App struct {
	locales              localeip.Resolver
	orderCatalog         *orderCatalog
	timelineIO           *videoTimelineIO
	workflowActions      workflowActionLocks
	recoveryStats        recoveryStats
	activeReviews        map[string]mediaReviewCancellation
	delivery             *mediaDeliveryCipher
	mediaModeration      *mediaModerationEngine
	moderation           *moderationEngine
	pricing              *pricingCatalog
	mail                 *mailDelivery
	conversations        map[string]Conversation
	chatMessages         map[string]chatNode
	taskLeases           *taskLeasePool
	storageLease         *StorageLease
	assets               AssetStorage
	library              map[string]libraryRecord
	libraryRevisions     map[string][]LibraryRevision
	libraryIdem          map[string]libraryReplay
	libraryRevisionCount int
	references           *ReferenceStore
	referenceItems       map[string]referenceRecord
	referenceIdem        map[string]idemRecord
	editPrepareSlots     chan struct{}
	catalog              *modelCatalog
	obs                  observability
	config               Config
	ctx                  context.Context
	cancel               context.CancelFunc
	wg                   sync.WaitGroup
	mu                   sync.Mutex
	runs                 map[string]*runRecord
	tasks                map[string]*taskRecord
	taskPinsMu           sync.Mutex // Lock order when both are needed: mu, then taskPinsMu.
	activeTaskClaims     map[string]int
	idem                 map[string]idemRecord
	workspaceSubscribers map[chan event]subscriptionScope
	projects             map[string]Project
	store                *postgresStore
	limiter              *loginLimiter
	bcryptSlots          chan struct{}
	workspaceSeq         uint64
	chat                 ChatProvider
	chatChildren         *chatChildPool
	runDeltaBatch        *runDeltaCoordinator
	media                MediaProvider
	execution            *executionScheduler
	mediaExecution       *mediaExecutionLanes
	mediaScheduler       *mediaScheduler
	streamSlots          chan struct{}
	streamSessions       *streamSessionMonitor
	auditEvents          []AuditEvent
	auditStart           int
}

func New(c Config) (*App, error) {
	c.defaults()
	locales, err := localeip.New(c.LocaleTrustedProxies)
	if err != nil {
		return nil, err
	}
	if err := c.ChatChildren.validate(c); err != nil {
		return nil, err
	}
	if c.Admission.GlobalStreams > 128 && (c.AuthMode != "required" || c.DatabaseURL == "" || c.RunReplayReadConnections < 1) {
		return nil, errors.New("expanded SSE admission requires authentication, PostgreSQL and Run replay readers")
	}
	if c.RunDeltaBatching && c.DatabaseURL == "" {
		return nil, errors.New("Run delta batching requires PostgreSQL")
	}
	if c.RunReplayReadConnections < 0 || c.RunReplayReadConnections > maxRunReplayConnections || c.RunReplayReadConnections > 0 && c.DatabaseURL == "" {
		return nil, errors.New("separate Run replay readers require PostgreSQL and a connection count between 1 and 4 (0 disables)")
	}
	if c.LongConversationHistory && c.DatabaseURL == "" {
		return nil, errors.New("long conversation history requires PostgreSQL")
	}
	orders, err := loadOrderCatalog(c.OrderCatalogFile, c.AuthMode, c.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if c.RunLeaseDuration < 3*time.Second || c.RunLeaseDuration > 5*time.Minute {
		return nil, errors.New("run lease duration must be between 3 seconds and 5 minutes")
	}
	if err := c.ChatScheduling.normalize(c.RunWorkers, c.QueueSize); err != nil {
		return nil, err
	}
	c.RunWorkers = c.ChatScheduling.Workers
	if err := c.MediaScheduling.normalize(c.TaskWorkers); err != nil {
		return nil, err
	}
	if c.MediaScheduling.QueryDBConnections > 0 && c.DatabaseURL == "" {
		return nil, errors.New("separate query database connections require PostgreSQL")
	}
	c.TaskWorkers = c.MediaScheduling.SubmitWorkers + c.MediaScheduling.QueryWorkers + c.MediaScheduling.DeliveryWorkers + c.MediaScheduling.ReviewWorkers
	moderation, err := newModerationEngine(c.Moderation, c.Mode)
	if err != nil {
		return nil, err
	}
	if moderation != nil && (c.DatabaseURL == "" || c.AuthMode != "required") {
		return nil, errors.New("moderation requires PostgreSQL and AUTH_MODE=required")
	}
	mediaModeration, err := newMediaModerationEngine(c.MediaModeration, c.Mode)
	if err != nil {
		return nil, err
	}
	if mediaModeration != nil && (moderation == nil || c.DatabaseURL == "" || c.AuthMode != "required") {
		return nil, errors.New("media moderation requires text moderation, PostgreSQL and AUTH_MODE=required")
	}
	if c.OperationsEnabled && (c.DatabaseURL == "" || c.AuthMode != "required") {
		return nil, errors.New("OPERATIONS_ENABLED requires PostgreSQL and AUTH_MODE=required")
	}
	if c.BillingMode != "disabled" && c.BillingMode != "credits" {
		return nil, errors.New("BILLING_MODE must be disabled or credits")
	}
	if c.BillingMode == "credits" && (c.DatabaseURL == "" || c.AuthMode != "required") {
		return nil, errors.New("BILLING_MODE=credits requires PostgreSQL and AUTH_MODE=required")
	}
	pricing, err := loadPricingCatalog(c)
	if err != nil {
		return nil, err
	}
	if err := validateMailConfig(c.Mail); err != nil {
		return nil, err
	}
	if c.TaskLeaseDuration < 5*time.Second || c.TaskLeaseDuration > 5*time.Minute {
		return nil, errors.New("TASK_LEASE_SECONDS must be between 5 and 300")
	}
	if err := validateAssetSeparation(c.WebDir, c.ReferenceDir); err != nil {
		return nil, errors.New("REFERENCE_DIR must be separate from the frontend directory")
	}
	if err := validateAssetSeparation("apps/workbench/public", c.ReferenceDir); err != nil {
		return nil, errors.New("REFERENCE_DIR must be separate from the Vite public directory")
	}
	if err := validateAssetSeparation(c.ReferenceDir, c.AssetDir); err != nil {
		return nil, errors.New("REFERENCE_DIR and ASSET_DIR must be separate directory trees")
	}
	if err := c.Admission.validate(); err != nil {
		return nil, err
	}
	if err := validateObservabilityConfig(c); err != nil {
		return nil, err
	}
	if c.AuthMode != "disabled" && c.AuthMode != "required" {
		return nil, errors.New("AUTH_MODE must be disabled or required")
	}
	if c.AuthMode == "required" && c.DatabaseURL == "" {
		return nil, errors.New("AUTH_MODE=required requires DATABASE_URL")
	}
	if c.Mail.Enabled && (c.AuthMode != "required" || c.DatabaseURL == "") {
		return nil, errors.New("account mail requires AUTH_MODE=required and PostgreSQL")
	}
	if c.Mail.Enabled && c.Mail.OutboxKey != "" {
		decoded, _ := base64.StdEncoding.Strict().DecodeString(c.Mail.OutboxKey)
		for _, secret := range []string{c.APIKey, c.AsyncImageAPIKey, c.MetricsToken} {
			if secret != "" && (secret == c.Mail.OutboxKey || secret == string(decoded)) {
				return nil, errors.New("account mail encryption key must be independent from provider and metrics credentials")
			}
		}
	}
	if c.AsyncImageBaseURL != "" || c.AssetDir != "" || c.DatabaseURL != "" {
		if err := validateAssetSeparation(c.WebDir, c.AssetDir); err != nil {
			return nil, err
		}
		if err := validateAssetSeparation("apps/workbench/public", c.AssetDir); err != nil {
			return nil, errors.New("ASSET_DIR must be outside the Vite public directory")
		}
	}
	if c.Mode != "demo" && c.Mode != "live" {
		return nil, errors.New("PROVIDER_MODE must be demo or live")
	}
	if c.Mode == "live" && c.BaseURL == "" && c.AsyncImageBaseURL == "" && c.mediaProvider == nil {
		return nil, errors.New("PROVIDER_MODE=live requires at least one configured gateway")
	}
	catalog, err := loadModelCatalog(c)
	if err != nil {
		return nil, err
	}
	assetDir := c.AssetDir
	if assetDir == "" {
		assetDir = ".runtime/assets"
	}
	lease, err := AcquireStorageLease(c.ReferenceDir, assetDir)
	if err != nil {
		return nil, err
	}
	references, err := OpenReferenceStore(c.ReferenceDir)
	if err != nil {
		_ = lease.Close()
		return nil, errors.New("private reference store could not be opened")
	}
	assets, err := NewAssetStorage(c.AssetStorage, c.AssetDir)
	if err != nil {
		_ = references.Close()
		_ = lease.Close()
		return nil, err
	}
	var timelineIO *videoTimelineIO
	initialized := false
	defer func() {
		if !initialized {
			if timelineIO != nil {
				timelineIO.close()
			}
			_ = references.Close()
			_ = assets.Close()
			_ = lease.Close()
		}
	}()
	timelineIO, err = newVideoTimelineIO(c)
	if err != nil {
		return nil, err
	}
	var chat ChatProvider = demoChat{delay: c.DemoTokenDelay}
	var media MediaProvider = demoMedia{duration: c.DemoTaskDuration}
	if c.Mode == "demo" && mediaModeration != nil && c.mediaProvider == nil {
		media, err = newModeratedDemoMedia(c.DemoTaskDuration, assets)
		if err != nil {
			return nil, err
		}
	}
	if c.Mode == "live" {
		chat, media = nil, nil
		if c.BaseURL != "" || c.APIKey != "" {
			provider, err := newOpenAIChatGatewayWithConcurrency(c.BaseURL, c.APIKey, c.ChatScheduling.Workers)
			if err != nil {
				return nil, err
			}
			chat = provider
		}
		if c.AsyncImageBaseURL != "" {
			media, err = NewAsyncMediaProvider(AsyncMediaConfig{SubmitConcurrency: c.MediaScheduling.SubmitWorkers, QueryConcurrency: c.MediaScheduling.QueryWorkers, DeliveryConcurrency: c.MediaScheduling.DeliveryWorkers, AssetStorage: c.AssetStorage, BodyStore: references, AllowNoDefaultModel: true, BaseURL: c.AsyncImageBaseURL, APIKey: c.AsyncImageAPIKey, ImageModel: c.AsyncImageModel, VideoModel: c.AsyncVideoModel, CredentialID: c.AsyncImageCredentialID, AssetDir: c.AssetDir, VideoSeconds: c.AsyncVideoSeconds, VideoResolution: c.AsyncVideoResolution, AllowedAssetHosts: c.AllowedAssetHosts})
			if err != nil {
				return nil, err
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	if c.mediaProvider != nil {
		media = c.mediaProvider
		if native, ok := media.(*AsyncMediaProvider); ok && native.bodies == nil {
			native.bodies = references
		}
	}
	_, phased := media.(MediaDeliveryProvider)
	delivery, deliveryErr := newMediaDeliveryCipher(c.MediaDelivery.EncryptionKey, phased, c.DatabaseURL == "")
	if deliveryErr == nil {
		deliveryErr = validateMediaDeliveryKeyIndependence(c)
	}
	if deliveryErr != nil {
		cancel()
		if closer, ok := media.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		return nil, deliveryErr
	}
	a := &App{config: c, ctx: ctx, cancel: cancel, runs: make(map[string]*runRecord), tasks: make(map[string]*taskRecord), idem: make(map[string]idemRecord), workspaceSubscribers: make(map[chan event]subscriptionScope), projects: make(map[string]Project), limiter: newLoginLimiter(), bcryptSlots: make(chan struct{}, 4), chat: chat, media: media, execution: newExecutionScheduler(c.ChatScheduling), mediaExecution: newMediaExecutionLanes(c.MediaScheduling), streamSlots: make(chan struct{}, c.Admission.GlobalStreams)}
	a.locales = locales
	a.timelineIO = timelineIO
	a.orderCatalog = orders
	a.delivery = delivery
	a.mediaScheduler = newMediaScheduler(c.MediaScheduling)
	a.moderation = moderation
	a.mediaModeration = mediaModeration
	a.catalog = catalog
	a.pricing = pricing
	a.conversations = make(map[string]Conversation)
	a.chatMessages = make(map[string]chatNode)
	a.storageLease = lease
	a.assets = assets
	a.library = make(map[string]libraryRecord)
	a.libraryRevisions = make(map[string][]LibraryRevision)
	a.libraryIdem = make(map[string]libraryReplay)
	a.references = references
	a.referenceItems = make(map[string]referenceRecord)
	a.referenceIdem = make(map[string]idemRecord)
	a.editPrepareSlots = make(chan struct{}, 2)
	if c.DatabaseURL == "" {
		a.projects["prj_demo"] = Project{ID: "prj_demo", WorkspaceID: "ws_demo", Name: "未命名创意项目", CreatedAt: time.Now().UTC()}
	}
	if c.DatabaseURL != "" {
		store, err := openPostgres(c.DatabaseURL)
		if err != nil {
			if closer, ok := media.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
			cancel()
			return nil, err
		}
		a.store = store
		store.onFailure = cancel
		if err = store.do(func(ctx context.Context, conn *pgx.Conn) error {
			if err := BindStorageOwnership(ctx, conn, c.ReferenceDir, assetDir); err != nil {
				return err
			}
			if a.delivery != nil {
				id, err := storageOwnerID(ctx, conn)
				if err != nil {
					return err
				}
				a.delivery.databaseID = id
			}
			return validateMediaDeliveryStore(ctx, conn, a.delivery, phased)
		}); err != nil {
			store.close()
			if closer, ok := media.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
			cancel()
			return nil, err
		}
		a.mail, err = newMailDelivery(c.Mail, store)
		if err == nil && a.mail != nil {
			if c.mailSender != nil {
				a.mail.sender = c.mailSender
			}
			// Validate the existing queue before fencing leases or persisting
			// recovery. Queue maintenance and delivery still wait for restore.
			err = store.do(func(ctx context.Context, conn *pgx.Conn) error {
				return a.mail.checkPendingKey(ctx, conn)
			})
		}
		if err == nil && c.RunReplayReadConnections > 0 {
			err = store.openRunReplayPool(a.ctx, c.RunReplayReadConnections)
		}
		if err == nil {
			store.feedReads, err = store.newVerifiedReadPool(a.ctx, feedReadConnections)
		}
		if err == nil && c.Admission.GlobalStreams > 128 {
			store.streamAuth, err = store.newVerifiedReadPool(a.ctx, 1)
		}
		if err == nil {
			err = a.initTaskLeases()
		}
		if err == nil {
			err = store.restore(a)
		}
		if err == nil && a.mail != nil {
			err = a.mail.prepare()
		}
		if err == nil && c.ChatChildren.Processes > 0 {
			a.chatChildren, err = startChatChildPool(a.ctx, c)
		}
		if err != nil {
			a.taskLeases.close()
			store.close()
			if closer, ok := media.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
			cancel()
			return nil, err
		}
	} else if err := CheckMemoryStorageOwnership(c.ReferenceDir, assetDir); err != nil {
		if closer, ok := media.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		cancel()
		return nil, err
	}
	if c.Admission.GlobalStreams > 128 {
		a.streamSessions = newStreamSessionMonitor(a.ctx, min(c.Heartbeat, 5*time.Second), a.store.validateStreamSessions)
		a.wg.Go(a.streamSessions.run)
	}
	if c.RunDeltaBatching {
		a.runDeltaBatch = newRunDeltaCoordinator(a.ctx, a.store.appendRunDeltaBatch)
		a.wg.Go(a.runDeltaBatch.run)
	}
	if a.chatChildren != nil {
		a.wg.Add(1)
		go a.runManagedChatScheduler()
	} else {
		for range c.RunWorkers {
			a.wg.Add(1)
			go a.runWorker()
		}
	}
	for range c.MediaScheduling.SubmitWorkers {
		a.wg.Add(1)
		go a.mediaTaskWorker(mediaSubmitLane)
	}
	if c.MediaScheduling.QueryWorkers > 32 {
		a.wg.Go(a.dispatchLargeQueryLane)
	} else {
		for range c.MediaScheduling.QueryWorkers {
			a.wg.Add(1)
			go a.mediaTaskWorker(mediaQueryLane)
		}
	}
	for range c.MediaScheduling.DeliveryWorkers {
		a.wg.Add(1)
		go a.mediaTaskWorker(mediaDeliveryLane)
	}
	if a.store != nil {
		for range c.MediaScheduling.ReviewWorkers {
			a.wg.Add(1)
			go a.mediaTaskWorker(mediaReviewLane)
		}
	}
	if a.store != nil {
		a.wg.Add(1)
		go a.checkpointRuns()
		a.wg.Add(1)
		go a.dispatchOutbox()
	}
	if a.mail != nil {
		a.wg.Add(1)
		go func() { defer a.wg.Done(); a.mail.run(a.ctx) }()
	}
	initialized = true
	return a, nil
}

func (a *App) Close() {
	a.cancel()
	if a.chatChildren != nil {
		a.chatChildren.close()
	}
	if a.timelineIO != nil {
		a.timelineIO.close()
	}
	a.wg.Wait()
	a.taskLeases.close()
	a.mu.Lock()
	for _, r := range a.runs {
		if !terminal(r.Run.Status) {
			err := a.finishRunLocked(r, "INTERRUPTED")
			// The outbox worker has stopped. Only a confirmed backlog rollback
			// and real projection progress authorize retrying the interruption.
			for isEventBacklog(err) {
				n, projectErr := a.store.projectOutboxBatch()
				if projectErr != nil || n == 0 {
					break
				}
				err = a.finishRunLocked(r, "INTERRUPTED")
			}
		}
	}
	a.mu.Unlock()
	if live, ok := a.chat.(*openAIChat); ok {
		live.client.CloseIdleConnections()
	}
	if closer, ok := a.media.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
	if a.store != nil {
		a.store.close()
	}
	if a.references != nil {
		_ = a.references.Close()
	}
	if a.assets != nil {
		_ = a.assets.Close()
	}
	if a.storageLease != nil {
		_ = a.storageLease.Close()
	}
}

func newID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure random source unavailable")
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

type operationError struct {
	status        int
	code, message string
}

func (e *operationError) Error() string { return e.message }
func failure(status int, code, message string) error {
	return &operationError{status: status, code: code, message: message}
}

func payloadHash(v any) [32]byte { b, _ := json.Marshal(v); return sha256.Sum256(b) }

func (a *App) createRun(input RunInput, key string, actorIDs ...string) (Run, bool, error) {
	return a.createRunContext(a.ctx, input, key, actorIDs...)
}

func (a *App) createRunContext(ctx context.Context, input RunInput, key string, actorIDs ...string) (Run, bool, error) {
	if err := authorizeWorkflowAttempt(ctx, input.WorkflowAttemptID, "text"); err != nil {
		return Run{}, false, err
	}
	approval := &runModerationAdmission{}
	run, replayed, err := a.createRunApproved(ctx, input, key, approval, actorIDs...)
	if !isModerationPreparation(err) {
		return run, replayed, err
	}
	approval.check(ctx, a.moderation)
	return a.createRunApproved(ctx, input, key, approval, actorIDs...)
}

func (a *App) createRunApproved(ctx context.Context, input RunInput, key string, approval *runModerationAdmission, actorIDs ...string) (Run, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil {
		return Run{}, false, failure(503, "SHUTTING_DOWN", "服务正在停止")
	}
	project, ok, err := a.projectLocked(input.ProjectID)
	if err != nil {
		return Run{}, false, err
	}
	if !ok {
		return Run{}, false, failure(422, "PROJECT_NOT_FOUND", "项目不存在")
	}
	index := project.WorkspaceID + ":runs:" + key
	hash := payloadHash(input)
	old, found, lookupErr := a.findIdempotencyLocked(index)
	if lookupErr != nil {
		return Run{}, false, lookupErr
	}
	if found {
		if old.Hash != hash {
			return Run{}, false, failure(409, "IDEMPOTENCY_CONFLICT", "相同幂等键已经用于其他请求")
		}
		record, err := a.getRunLocked(old.ID)
		if err != nil {
			return Run{}, false, err
		}
		return copyRun(record.Run), true, nil
	}
	if err := a.checkGenerationAccessLocked(project.WorkspaceID); err != nil {
		return Run{}, false, err
	}
	model, err := a.resolveModel(input.ModelID, "chat", input.ModelVersion, input.Input)
	if err != nil {
		return Run{}, false, err
	}
	if err := a.validateBillingAdmission(input.QuoteID, true); err != nil {
		return Run{}, false, err
	}
	var billingExecution *chatExecution
	if a.pricing != nil || a.moderation != nil {
		request := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: input.Input}}, MaxOutputTokens: model.Model.Capabilities.MaxOutputTokens, OutputLimitField: model.ChatOutputLimitField}
		if err := ValidateChatRequest(request, model.Model.Capabilities.ContextWindowTokens); err != nil {
			return Run{}, false, err
		}
		billingExecution = &chatExecution{UpstreamModel: model.UpstreamModel, Request: request}
	}
	if a.moderation != nil {
		if err := approval.prepare(billingExecution.Request); err != nil {
			return Run{}, false, err
		}
	}
	if err := a.checkAdmissionLocked(project.WorkspaceID, input.ModelID, "chat"); err != nil {
		return Run{}, false, err
	}
	if a.store == nil && len(a.runs) >= a.config.MaxRecords {
		return Run{}, false, failure(503, "LOCAL_STORE_FULL", "本地演示运行记录已达上限；重启将清空内存")
	}
	if err := a.execution.checkLocked(project.WorkspaceID); err != nil {
		return Run{}, false, err
	}
	now := time.Now().UTC()
	run := Run{ID: newID("run"), ProviderMode: a.config.Mode, WorkspaceID: project.WorkspaceID, ProjectID: input.ProjectID, ModelID: input.ModelID, Prompt: input.Input, Status: "QUEUED", CreatedAt: now, UpdatedAt: now}
	run.ModelName, run.ModelVersion, run.ModelCapabilities = model.Model.Name, model.Model.Version, copyCapabilities(&model.Model.Capabilities)
	run.BillingQuoteID, run.BillingRequestHash = input.QuoteID, ledgerRunRequestHash(input)
	approval.attach(&run)
	audit := newAuditEvent(run.WorkspaceID, run.ProjectID, firstActor(actorIDs), "run.accepted", "run", run.ID, AuditDetails{ToStatus: run.Status, ModelID: run.ModelID})
	if a.store != nil {
		if err := a.store.createGuardedRun(&run, index, hash, workflowChildFrom(ctx), accountRequestPrincipal(ctx), audit); err != nil {
			return Run{}, false, err
		}
	}
	a.runs[run.ID] = &runRecord{Run: run, ChatExecution: billingExecution, UpstreamModel: model.UpstreamModel, Subscribers: make(map[chan event]bool)}
	a.execution.enqueueLocked(executionJob{ID: run.ID, WorkspaceID: run.WorkspaceID, ModelKey: model.UpstreamModel, EnqueuedAt: time.Now()})
	a.appendMemoryAuditLocked(audit)
	if a.store == nil {
		a.idem[index] = idemRecord{Hash: hash, ID: run.ID}
	}
	return copyRun(run), false, nil
}

func (a *App) createTask(input TaskInput, key string, actorIDs ...string) (Task, bool, error) {
	return a.createTaskContext(a.ctx, input, key, actorIDs...)
}

func (a *App) createTaskContext(ctx context.Context, input TaskInput, key string, actorIDs ...string) (Task, bool, error) {
	if err := authorizeWorkflowAttempt(ctx, input.WorkflowAttemptID, input.Kind); err != nil {
		return Task{}, false, err
	}
	approval := &taskMediaModerationAdmission{}
	task, replayed, err := a.createTaskApproved(ctx, input, key, approval, actorIDs...)
	if !isMediaModerationPreparation(err) {
		return task, replayed, err
	}
	approval.check(ctx, a)
	return a.createTaskApproved(ctx, input, key, approval, actorIDs...)
}

func (a *App) createTaskApproved(ctx context.Context, input TaskInput, key string, approval *taskMediaModerationAdmission, actorIDs ...string) (Task, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil {
		return Task{}, false, failure(503, "SHUTTING_DOWN", "服务正在停止")
	}
	project, ok, err := a.projectLocked(input.ProjectID)
	if err != nil {
		return Task{}, false, err
	}
	if !ok {
		return Task{}, false, failure(422, "PROJECT_NOT_FOUND", "项目不存在")
	}
	index := project.WorkspaceID + ":tasks:" + key
	panel := panelVideoGuardFrom(ctx)
	if panel != nil {
		index = panel.taskIndex()
	}
	hash := payloadHash(input)
	old, found, lookupErr := a.findIdempotencyLocked(index)
	if lookupErr != nil {
		return Task{}, false, lookupErr
	}
	if found {
		record, err := a.getTaskLocked(old.ID)
		if err != nil {
			return Task{}, false, err
		}
		compareHash := hash
		if record.Task.ModelVersion == "" && input.QuoteID == "" {
			compareHash = payloadHash(legacyTaskInput(input))
		}
		if old.Hash != compareHash {
			return Task{}, false, failure(409, "IDEMPOTENCY_CONFLICT", "相同幂等键已经用于其他请求")
		}
		if sourceVideoOperation(record.Task.Operation) {
			receipt, e := a.sourceVideoReceipt(accountRequestPrincipal(ctx), input.ProjectID, key)
			return receipt, e == nil, e
		}
		return copyTask(record.Task), true, nil
	}
	if err := a.checkGenerationAccessLocked(project.WorkspaceID); err != nil {
		return Task{}, false, err
	}
	if a.moderation != nil && a.mediaModeration == nil {
		return Task{}, false, failure(422, "MODERATION_MEDIA_UNSUPPORTED", "当前审核策略只支持文本，新媒体生成尚未开放")
	}
	if err := a.validateBillingAdmission(input.QuoteID, false); err != nil {
		return Task{}, false, err
	}
	billingHash := panel.quoteHash(input)
	operation, err := taskOperation(input)
	if err != nil {
		return Task{}, false, err
	}
	model, err := a.resolveModel(input.ModelID, input.Kind, input.ModelVersion, input.Inputs.Prompt)
	if err != nil {
		return Task{}, false, err
	}
	if input.Kind != "image" && input.Kind != "video" {
		return Task{}, false, failure(422, "MODEL_NOT_FOUND", "媒体任务需要图片或视频模型")
	}
	var refs []referenceRecord
	var firstFrame *AssetInfo
	var sourceVideo *timelineVideoAsset
	if sourceVideoOperation(operation) {
		sourceVideo, err = a.resolveSourceVideo(accountRequestPrincipal(ctx), input, model.Model)
		if err != nil {
			return Task{}, false, err
		}
	}
	if operation == "first_frame" {
		guard := workflowChildFrom(ctx)
		if guard == nil || guard.kind != "video" || guard.id != input.Inputs.WorkflowID || guard.scope != (subscriptionScope{project.WorkspaceID, input.ProjectID}) {
			return Task{}, false, failure(409, "WORKFLOW_STEP_REQUIRED", "请从已确认图片的工作流提交视频节点")
		}
		firstFrame, err = a.firstFrameInfo(guard.scope, input, model.Model)
		if err != nil {
			return Task{}, false, err
		}
	}
	if operation == "edit" {
		if !model.Model.Capabilities.ImageEditSupported || len(input.Inputs.ReferenceAssetIDs) > model.Model.Capabilities.MaxReferenceImages {
			return Task{}, false, failure(422, "IMAGE_EDIT_UNAVAILABLE", "当前模型不支持此数量的参考图编辑")
		}
		refs, err = a.resolveReferencesLocked(project.WorkspaceID, input.ProjectID, input.Inputs.ReferenceAssetIDs)
		if err != nil {
			return Task{}, false, err
		}
	}
	parameters, err := resolveTaskParameters(input.Parameters, model.Model)
	if err != nil {
		return Task{}, false, err
	}
	if a.mediaModeration != nil {
		approval.workspaceID = project.WorkspaceID
		approval.firstFrame = firstFrame
		if sourceVideo != nil {
			approval.videoSource = &sourceVideo.info
		}
		if err := approval.prepare(input, parameters, model, refs); err != nil {
			return Task{}, false, err
		}
	}
	if err := a.checkMemoryLibraryCapacityLocked(parameters.Count); err != nil {
		return Task{}, false, err
	}
	if a.media == nil {
		return Task{}, false, failure(501, "PROVIDER_NOT_CONFIGURED", "真实媒体接口尚未配置；需先完成二开网关契约联调")
	}
	if err := a.checkAdmissionLocked(project.WorkspaceID, input.ModelID, input.Kind); err != nil {
		return Task{}, false, err
	}
	if a.store == nil && len(a.tasks) >= a.config.MaxRecords {
		return Task{}, false, failure(503, "LOCAL_STORE_FULL", "本地演示任务记录已达上限；重启将清空内存")
	}
	now := time.Now().UTC()
	task := Task{ID: newID("task"), ProviderMode: a.config.Mode, WorkspaceID: project.WorkspaceID, ProjectID: input.ProjectID, Kind: input.Kind, ModelID: input.ModelID, Prompt: input.Inputs.Prompt, Status: "QUEUED", Version: 1, CreatedAt: now, UpdatedAt: now, Artifacts: []Artifact{}}
	task.Operation = operation
	if sourceVideo != nil {
		ref := *input.Inputs.SourceVideo
		task.SourceVideo = &ref
		task.ExtensionDirection = input.Inputs.ExtensionDirection
	}
	if firstFrame != nil {
		source := *input.Inputs.FirstFrame
		task.FirstFrame = &source
	}
	task.BillingQuoteID, task.BillingRequestHash = input.QuoteID, billingHash
	task.ReferenceAssetIDs = append([]string(nil), input.Inputs.ReferenceAssetIDs...)
	task.ModelName, task.ModelVersion, task.ModelCapabilities, task.Parameters = model.Model.Name, model.Model.Version, copyCapabilities(&model.Model.Capabilities), &parameters
	input.Parameters = parameters // The idempotency hash above belongs to the original request, never this resolved execution copy.
	input.Inputs.ReferenceAssetIDs = append([]string(nil), input.Inputs.ReferenceAssetIDs...)
	record := &taskRecord{Task: task, Input: input, NextPoll: now, sourceVideo: sourceVideo}
	approval.attach(record)
	task = record.Task
	if operation == "edit" || operation == "first_frame" || sourceVideo != nil {
		var prepareErr error
		a.mu.Unlock()
		func() {
			defer a.mu.Lock()
			if sourceVideo != nil {
				record.Submission, prepareErr = a.prepareSourceVideo(ctx, input, model.UpstreamModel, sourceVideo.info)
			} else if firstFrame != nil {
				record.Submission, prepareErr = a.prepareFirstFrame(ctx, input, model.UpstreamModel, *firstFrame)
			} else {
				record.Submission, prepareErr = a.prepareImageEdit(ctx, input, model.UpstreamModel, refs)
			}
		}()
		// Another submission with the same key can finish while disk I/O is in
		// progress. Its durable answer takes precedence over current conditions.
		old, found, err := a.findIdempotencyLocked(index)
		if err != nil {
			return Task{}, false, err
		}
		if found {
			if old.Hash != hash {
				return Task{}, false, failure(409, "IDEMPOTENCY_CONFLICT", "相同幂等键已经用于其他请求")
			}
			existing, err := a.getTaskLocked(old.ID)
			if err != nil {
				return Task{}, false, err
			}
			if sourceVideoOperation(existing.Task.Operation) {
				receipt, e := a.sourceVideoReceipt(accountRequestPrincipal(ctx), input.ProjectID, key)
				return receipt, e == nil, e
			}
			return copyTask(existing.Task), true, nil
		}
		if err := a.checkGenerationAccessLocked(project.WorkspaceID); err != nil {
			return Task{}, false, err
		}
		if a.ctx.Err() != nil || ctx.Err() != nil {
			return Task{}, false, failure(503, "TASK_PREPARATION_INTERRUPTED", "任务准备已中断；请保留原请求和幂等键核对")
		}
		if prepareErr != nil {
			return Task{}, false, prepareErr
		}
		if operation == "edit" {
			if _, err := a.resolveReferencesLocked(project.WorkspaceID, input.ProjectID, input.Inputs.ReferenceAssetIDs); err != nil {
				return Task{}, false, err
			}
		}
		if err := a.checkAdmissionLocked(project.WorkspaceID, input.ModelID, input.Kind); err != nil {
			return Task{}, false, err
		}
		if err := a.checkMemoryLibraryCapacityLocked(parameters.Count); err != nil {
			return Task{}, false, err
		}
		if a.store == nil && len(a.tasks) >= a.config.MaxRecords {
			return Task{}, false, failure(503, "LOCAL_STORE_FULL", "本地任务缓存已达上限")
		}
		now = time.Now().UTC()
		record.Task.CreatedAt = now
		record.Task.UpdatedAt = now
		record.NextPoll = now
		task = record.Task
	} else if provider, ok := a.media.(interface {
		PrepareModel(TaskInput, string) (MediaSubmission, error)
	}); ok {
		submission, err := provider.PrepareModel(input, model.UpstreamModel)
		if err != nil {
			return Task{}, false, failure(422, "INVALID_MODEL_PARAMETERS", "所选参数不能用于当前媒体模型")
		}
		record.Submission = &submission
	}
	audit := newAuditEvent(task.WorkspaceID, task.ProjectID, firstActor(actorIDs), "task.accepted", "task", task.ID, AuditDetails{ToStatus: task.Status, ModelID: task.ModelID, Kind: task.Kind})
	if a.store != nil {
		if err := a.store.createSourcedTask(record, index, hash, workflowChildFrom(ctx), panel, accountRequestPrincipal(ctx), audit); err != nil {
			return Task{}, false, err
		}
	}
	task = record.Task
	a.cacheIdleTaskLocked(record)
	a.appendMemoryAuditLocked(audit)
	if a.store == nil {
		a.idem[index] = idemRecord{Hash: hash, ID: task.ID}
	}
	a.publishTaskLocked(task)
	return copyTask(task), false, nil
}

func (a *App) listRuns() []Run {
	a.mu.Lock()
	defer a.mu.Unlock()
	items := make([]Run, 0, len(a.runs))
	for _, r := range a.runs {
		items = append(items, copyRun(r.Run))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return items
}
func (a *App) listTasks() []Task {
	a.mu.Lock()
	defer a.mu.Unlock()
	items := make([]Task, 0, len(a.tasks))
	for _, t := range a.tasks {
		items = append(items, copyTask(t.Task))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return items
}

func (a *App) runWorker() {
	defer a.wg.Done()
	for {
		a.mu.Lock()
		if a.ctx.Err() != nil {
			a.mu.Unlock()
			return
		}
		job, ok := a.execution.pickLocked(time.Now())
		record := a.runs[job.ID]
		wake := a.execution.wake
		a.mu.Unlock()
		if ok {
			a.executePickedRun(job, record)
			continue
		}
		select {
		case <-a.ctx.Done():
			return
		case <-wake:
		}
	}
}

func (a *App) executePickedRun(job executionJob, record *runRecord) {
	a.executeScheduledRun(job, record, nil)
}

func (a *App) executeScheduledRun(job executionJob, record *runRecord, slot *chatChildSlot) {
	defer func() {
		if slot != nil {
			slot.release()
		}
		a.mu.Lock()
		a.execution.releaseLocked(job.ID)
		a.releaseRunCacheLocked(record)
		a.mu.Unlock()
	}()
	a.executeRunOn(job.ID, slot)
}

const maxRunOutputBytes = 128 << 10

// Every published delta is nonempty and consumes at least one output byte.
// On crash recovery this reserved gap exceeds all possibly uncheckpointed
// deltas, so a browser that saw newer volatile events accepts the snapshot.
const recoverySequenceGap = maxRunOutputBytes + 2

func (a *App) executeRun(id string) {
	a.executeRunOn(id, nil)
}

func (a *App) executeRunOn(id string, slot *chatChildSlot) {
	a.mu.Lock()
	r := a.runs[id]
	if r == nil {
		a.mu.Unlock()
		return
	}
	if r.Run.Status != "QUEUED" || r.streamWrite != nil || a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	var lease runLease
	var owner string
	if slot != nil {
		owner = slot.owner()
	} else if a.store != nil {
		owner = newID("worker")
	}
	for {
		next := r.Run
		next.Status = "RUNNING"
		next.UpdatedAt = time.Now().UTC()
		var saveErr error
		if a.store != nil {
			r.streamWrite = make(chan struct{})
			a.mu.Unlock()
			lease, saveErr = a.store.claimRunLease(&next, owner, a.config.RunLeaseDuration)
			a.mu.Lock()
			close(r.streamWrite)
			r.streamWrite = nil
		}
		if saveErr == nil {
			r.Run = next
			for r.streamCancel != nil {
				a.waitRunWriteLocked(r.streamCancel)
			}
			if terminal(r.Run.Status) || a.ctx.Err() != nil {
				a.mu.Unlock()
				return
			}
			break
		}
		a.mu.Unlock()
		if !isEventBacklog(saveErr) || !waitTaskPoll(a.ctx, 250*time.Millisecond) {
			return
		}
		a.mu.Lock()
		if terminal(r.Run.Status) || a.ctx.Err() != nil {
			a.mu.Unlock()
			return
		}
	}
	ctx, cancel := context.WithTimeout(a.ctx, a.config.RunTimeout)
	r.Cancel = cancel
	var watch *runLeaseWatch
	if a.store != nil {
		watch = a.watchRunLease(r, lease, cancel)
		r.leaseWatch = watch
	}
	r.LastCheckpoint = time.Now()
	prompt := r.Run.Prompt
	upstreamModel := r.UpstreamModel
	execution := r.ChatExecution
	billed := r.Run.Billing != nil
	moderated := r.Run.Moderation != nil
	policy := r.Run.ModerationPolicy
	a.mu.Unlock()
	if watch != nil {
		defer a.stopRunLeaseWatch(r, watch)
		// Cancel, shutdown, or a generation switch may have occurred between
		// the start commit and this worker obtaining its local execution state.
		if _, err := a.store.renewRunLease(lease, a.config.RunLeaseDuration); err != nil {
			watch.reject(err)
			cancel()
			return
		}
	}
	if ctx.Err() != nil {
		cancel()
		return
	}
	var candidateOutput strings.Builder
	emit := func(delta string) error {
		if delta == "" {
			return nil
		}
		if strings.ContainsRune(delta, 0) || !utf8.ValidString(delta) {
			return errors.New("provider emitted invalid text")
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if terminal(r.Run.Status) || ctx.Err() != nil {
			return context.Canceled
		}
		if moderated {
			if candidateOutput.Len()+len(delta) > maxRunOutputBytes {
				return errors.New("run output exceeded local size limit")
			}
			candidateOutput.WriteString(delta)
			return nil
		}
		a.waitRunStreamIdleLocked(r)
		if ctx.Err() != nil || terminal(r.Run.Status) {
			return context.Canceled
		}
		if len(r.Run.Output)+len(delta) > maxRunOutputBytes {
			return errors.New("run output exceeded local size limit")
		}
		at := time.Now().UTC()
		if a.store != nil {
			expected := r.Run.Seq
			r.streamWrite = make(chan struct{})
			a.mu.Unlock()
			var err error
			if a.runDeltaBatch != nil {
				err = a.runDeltaBatch.append(ctx, runDeltaAppend{Lease: lease, Expected: expected, Delta: delta, At: at})
			} else {
				err = a.store.appendLeasedRunDelta(lease, expected, delta, at)
			}
			watch.reject(err)
			a.mu.Lock()
			close(r.streamWrite)
			r.streamWrite = nil
			if err != nil {
				return err
			}
		}
		r.Output.WriteString(delta)
		r.Run.Output = r.Output.String()
		r.Run.UpdatedAt = at
		r.Run.Seq++
		a.publishRunLocked(r, "message.delta", map[string]any{"run_id": id, "seq": r.Run.Seq, "delta": delta})
		return nil
	}
	var err error
	var usage *ChatUsage
	if slot != nil {
		value, childErr := slot.stream(ctx, chatChildIdentity{RunID: id, Owner: lease.Owner, ExecutionID: lease.ExecutionID, Generation: lease.Generation, Epoch: lease.Epoch}, execution.UpstreamModel, execution.Request, billed, emit)
		err = childErr
		if billed {
			usage = &value
		}
	} else if billed {
		if provider, ok := a.chat.(ChatUsageProvider); ok && execution != nil {
			value, e := provider.StreamMessagesUsage(ctx, execution.UpstreamModel, execution.Request, emit)
			usage, err = &value, e
		} else {
			err = errors.New("metered chat provider or execution snapshot unavailable")
		}
	} else if execution != nil {
		if provider, ok := a.chat.(interface {
			StreamMessages(context.Context, string, ChatRequest, func(string) error) error
		}); ok {
			err = provider.StreamMessages(ctx, execution.UpstreamModel, execution.Request, emit)
		} else {
			err = errors.New("chat provider does not support immutable conversation context")
		}
	} else if provider, ok := a.chat.(interface {
		StreamModel(context.Context, string, string, func(string) error) error
	}); ok {
		err = provider.StreamModel(ctx, upstreamModel, prompt, emit)
	} else if a.chat != nil {
		err = a.chat.Stream(ctx, prompt, emit)
	} else {
		err = errors.New("chat provider is unavailable")
	}
	decision := ModerationDecision{State: "REVIEW", ReasonCode: "execution_incomplete"}
	if moderated && err == nil && watch != nil && ctx.Err() == nil {
		// Check the database clock before starting another external operation;
		// a provider may return after the last renewal has expired.
		_, err = a.store.renewRunLease(lease, a.config.RunLeaseDuration)
		watch.reject(err)
	}
	if moderated && err == nil {
		if a.moderation != nil && policy != nil && ctx.Err() == nil {
			decision = a.moderation.CheckOutput(ctx, policy, candidateOutput.String())
		} else {
			decision.ReasonCode = "policy_unavailable"
		}
	}
	if moderated && ctx.Err() != nil {
		decision = ModerationDecision{State: "REVIEW", ReasonCode: "execution_incomplete"}
	}
	cancel()
	a.mu.Lock()
	a.waitRunStreamIdleLocked(r)
	r.Cancel = nil
	if terminal(r.Run.Status) {
		a.mu.Unlock()
		return
	}
	next := copyRun(r.Run)
	next.BillingUsage = usage
	status := "COMPLETED"
	if err != nil {
		status = "FAILED"
		next.Error = &APIError{Code: "PROVIDER_STREAM_FAILED", Message: "生成流未完整完成，未自动重新请求模型"}
	}
	if moderated {
		next.Moderation = copyRunModeration(next.Moderation)
		next.Moderation.Version++
		next.Moderation.State, next.Moderation.ReasonCode = decision.State, decision.ReasonCode
		if decision.State == "ALLOWED" && err == nil {
			next.Output = candidateOutput.String()
		} else {
			next.Output = ""
			status = "FAILED"
			code, message := "MODERATION_UNAVAILABLE", "输出未完成审核，内容保持隔离"
			if decision.State == "BLOCKED" {
				code, message = "MODERATION_OUTPUT_BLOCKED", "输出未通过审核，内容保持隔离"
			}
			next.Error = &APIError{Code: code, Message: message}
		}
	}
	if a.ctx.Err() != nil {
		status = "INTERRUPTED"
		// The candidate was never public. Shutdown must not promote it even if
		// an external checker concurrently returned an allow decision.
		if moderated {
			next.Moderation = copyRunModeration(r.Run.Moderation)
			quarantineInterruptedRun(&next, "execution_incomplete")
		}
		next.Error = &APIError{Code: "PROCESS_STOPPED", Message: "服务停止；保留当前检查点，未自动重新请求模型"}
	}
	childLost := slot != nil && errors.Is(err, errChatChildInterrupted)
	if childLost && a.ctx.Err() == nil {
		status = "INTERRUPTED"
		next.Error = &APIError{Code: "CHAT_CHILD_INTERRUPTED", Message: "模型执行进程已断开，已保留确认的输出"}
	}
	for {
		// An interrupted child has no authoritative terminal output or usage.
		// The control transaction rebuilds from the committed public prefix.
		saveErr := a.saveFinishedRunLocked(r, next, status, childLost)
		a.mu.Unlock()
		// The model has already completed. Retain its result and retry only
		// the transaction; invoking Stream again could duplicate billing.
		if !isEventBacklog(saveErr) || !waitTaskPoll(a.ctx, 250*time.Millisecond) {
			return
		}
		a.mu.Lock()
		a.waitRunStreamIdleLocked(r)
	}
}

func (a *App) finishRunLocked(r *runRecord, status string, events ...AuditEvent) error {
	next := copyRun(r.Run)
	if next.Moderation != nil && next.Moderation.State == "PENDING" {
		reason := "execution_incomplete"
		if status == "CANCELLED" {
			reason = "cancelled"
		}
		quarantineInterruptedRun(&next, reason)
	}
	return a.saveFinishedRunLocked(r, next, status, true, events...)
}

func (a *App) finishRunCandidateLocked(r *runRecord, next Run, status string, events ...AuditEvent) error {
	return a.saveFinishedRunLocked(r, next, status, false, events...)
}

func (a *App) saveFinishedRunLocked(r *runRecord, next Run, status string, control bool, events ...AuditEvent) error {
	if terminal(r.Run.Status) {
		return nil
	}
	next.Status = status
	next.UpdatedAt = time.Now().UTC()
	next.Seq = r.Run.Seq + 1
	if a.store != nil {
		var err error
		switch {
		case control:
			err = a.store.saveControlRun(&next, events...)
		case r.leaseWatch != nil:
			err = a.store.saveLeasedRun(&next, r.leaseWatch.lease, r.Run.Seq, events...)
			r.leaseWatch.reject(err)
		default:
			err = a.store.saveRunCandidate(&next, events...)
		}
		if err != nil {
			return err
		}
	}
	r.Run = next
	a.appendMemoryAuditLocked(events...)
	r.CheckpointSeq = next.Seq
	a.publishFinishedRunLocked(r)
	return nil
}

// Match the persisted terminal event. Moderated runs expose only snapshots.
func (a *App) publishFinishedRunLocked(r *runRecord) {
	if r.Run.Moderation != nil {
		a.publishRunLocked(r, "run.snapshot", copyRun(r.Run))
		return
	}
	name := map[string]string{"COMPLETED": "run.completed", "CANCELLED": "run.cancelled", "FAILED": "run.failed", "INTERRUPTED": "run.interrupted"}[r.Run.Status]
	a.publishRunLocked(r, name, map[string]any{"run_id": r.Run.ID, "seq": r.Run.Seq, "status": r.Run.Status})
}

func (a *App) publishRunLocked(r *runRecord, name string, data any) {
	payload, _ := json.Marshal(data)
	e := event{Seq: r.Run.Seq, Name: name, Data: payload}
	if len(r.Events) < a.config.ReplaySize {
		r.Events = append(r.Events, e)
	} else {
		r.Events[r.EventStart] = e
		r.EventStart = (r.EventStart + 1) % len(r.Events)
	}
	for ch := range r.Subscribers {
		select {
		case ch <- e:
		default:
			a.obs.slowConsumers.Add(1)
			delete(r.Subscribers, ch)
			close(ch)
		}
	}
}

func (a *App) publishTaskLocked(task Task) {
	a.workspaceSeq++
	data, _ := json.Marshal(map[string]any{"task": task})
	e := event{Seq: a.workspaceSeq, Name: "task.updated", Data: data}
	for ch, scope := range a.workspaceSubscribers {
		if scope.WorkspaceID != task.WorkspaceID || scope.ProjectID != "" && scope.ProjectID != task.ProjectID {
			continue
		}
		select {
		case ch <- e:
		default:
			a.obs.slowConsumers.Add(1)
			delete(a.workspaceSubscribers, ch)
			close(ch)
		}
	}
}

// App.mu protects the record; neither a database write nor a waiting cancel
// needs that lock to remain held while this caller waits.
func (a *App) waitRunWriteLocked(done <-chan struct{}) {
	a.mu.Unlock()
	<-done
	a.mu.Lock()
}

func (a *App) waitRunStreamIdleLocked(r *runRecord) {
	for r.streamWrite != nil || r.streamCancel != nil {
		if r.streamCancel != nil {
			a.waitRunWriteLocked(r.streamCancel)
		} else {
			a.waitRunWriteLocked(r.streamWrite)
		}
	}
}

func (a *App) cancelRun(id string, actorIDs ...string) (Run, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, err := a.getRunLocked(id)
	if err != nil {
		return Run{}, err
	}
	for r.streamCancel != nil {
		a.waitRunWriteLocked(r.streamCancel)
	}
	if r.streamWrite != nil {
		r.streamCancel = make(chan struct{})
		defer func() {
			close(r.streamCancel)
			r.streamCancel = nil
		}()
		a.waitRunWriteLocked(r.streamWrite)
	}
	if !terminal(r.Run.Status) {
		audit := newAuditEvent(r.Run.WorkspaceID, r.Run.ProjectID, firstActor(actorIDs), "run.cancelled", "run", r.Run.ID, AuditDetails{FromStatus: r.Run.Status, ToStatus: "CANCELLED"})
		if err := a.finishRunLocked(r, "CANCELLED", audit); err != nil {
			return Run{}, err
		}
		a.execution.cancelLocked(id)
		a.releaseRunCacheLocked(r)
		if r.Cancel != nil {
			r.Cancel()
		}
	}
	return copyRun(r.Run), nil
}

func (a *App) cancelTask(id string, actorIDs ...string) (Task, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, err := a.getTaskLocked(id)
	if err != nil {
		return Task{}, err
	}
	if !terminal(t.Task.Status) {
		if a.config.Mode == "live" || t.Task.ProviderMode == "live" {
			return Task{}, failure(409, "CANCEL_UNSUPPORTED", "当前媒体供应商未提供取消确认接口；任务保持原状态继续查询")
		}
		audit := newAuditEvent(t.Task.WorkspaceID, t.Task.ProjectID, firstActor(actorIDs), "task.cancelled", "task", t.Task.ID, AuditDetails{FromStatus: t.Task.Status, ToStatus: "CANCELLED", Kind: t.Task.Kind})
		candidate := *cloneLeasedRecord(t)
		candidate.Lease, candidate.LeaseComplete, candidate.LeaseStatus = nil, false, ""
		candidate.Task.Status = "CANCELLED"
		quarantineMediaTask(&candidate, "cancelled")
		candidate.Task.UpdatedAt = time.Now().UTC()
		candidate.Task.Version++
		candidate.Task.CanReconcile = false
		if a.store != nil {
			if err := a.store.saveTask(&candidate, audit); err != nil {
				return Task{}, err
			}
		} else if candidate.Delivery != nil && candidate.Delivery.State == "READY" {
			candidate.Delivery.State = "PARKED"
			candidate.Delivery.Version++
		}
		t.Task = candidate.Task
		t.Delivery = cloneMediaDelivery(candidate.Delivery)
		t.ReviewDispatch = cloneMediaReviewDispatch(candidate.ReviewDispatch)
		a.cancelMediaReviewLocked(id)
		t.Lease, t.LeaseComplete, t.LeaseStatus, t.InFlight = nil, false, "", false
		a.appendMemoryAuditLocked(audit)
		a.publishTaskLocked(t.Task)
	}
	return copyTask(t.Task), nil
}

func (a *App) executeTaskStep(id string) {
	if a.store != nil {
		a.executePersistentTaskStep(id)
		return
	}
	if !a.executeMemoryTaskLane(mediaSubmitLane, id) {
		if !a.executeMemoryTaskLane(mediaQueryLane, id) {
			a.executeMemoryTaskLane(mediaDeliveryLane, id)
		}
	}
}

func (a *App) executeMemoryTaskRecord(id string, t *taskRecord, attempt string) {
	a.mu.Lock()
	if a.tasks[id] != t || terminal(t.Task.Status) || a.ctx.Err() != nil || a.mediaExecution.memoryAttempts[id] != attempt {
		a.mu.Unlock()
		return
	}
	handle, input := t.Handle, t.Input
	if handle.ID == "" && t.Task.Status == "SUBMITTING" {
		t.Task.Status = "SUBMIT_UNKNOWN"
		t.Task.Error = &APIError{Code: "SUBMISSION_UNKNOWN", Message: "上次提交没有确定受理结果，禁止自动重新提交"}
		t.Task.Version++
		t.Task.UpdatedAt = time.Now().UTC()
		t.Task.CanReconcile = false
		a.publishTaskLocked(t.Task)
		a.mu.Unlock()
		return
	}
	if handle.ID == "" {
		if prepared, ok := a.media.(interface {
			Prepare(TaskInput) (MediaSubmission, error)
		}); ok && t.Submission == nil {
			submission, err := prepared.Prepare(input)
			if err != nil {
				t.InFlight = false
				t.Task.Status = "FAILED"
				t.Task.Error = &APIError{Code: "INVALID_MEDIA_REQUEST", Message: err.Error()}
				t.Task.Version++
				t.Task.UpdatedAt = time.Now().UTC()
				a.publishTaskLocked(t.Task)
				a.mu.Unlock()
				return
			}
			t.Submission = &submission
		}
		t.Task.Status = "SUBMITTING"
		t.Task.Version++
		t.Task.UpdatedAt = time.Now().UTC()
		a.publishTaskLocked(t.Task)
	}
	submission := t.Submission
	version := t.Task.Version
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	defer cancel()
	var result MediaResult
	var err error
	var deliveryQuery *MediaQueryResult
	if handle.ID == "" {
		if prepared, ok := a.media.(interface {
			SubmitPrepared(context.Context, MediaSubmission) (MediaHandle, error)
		}); ok && submission != nil {
			handle, err = prepared.SubmitPrepared(ctx, *submission)
		} else {
			handle, err = a.media.Submit(ctx, input)
		}
		result = MediaResult{Status: "QUEUED", RetryAfter: handle.RetryAfter, Observation: handle.Observation}
	} else if provider, ok := a.media.(MediaDeliveryProvider); ok {
		queried, queryErr := provider.QueryStatus(ctx, handle)
		result, err = queried.Result, queryErr
		if err == nil && (result.Status == "SUCCEEDED" || result.Status == "PARTIAL") {
			if validMediaDeliverySources(handle, queried) {
				deliveryQuery = &queried
			} else {
				err = mediaDeliveryFailure()
			}
		} else if err == nil && (len(queried.Items) != 0 || len(result.Artifacts) != 0) {
			err = mediaDeliveryFailure()
		}
	} else {
		result, err = a.media.Query(ctx, handle)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tasks[id] != t || terminal(t.Task.Status) || t.Task.Version != version || a.mediaExecution.memoryAttempts[id] != attempt {
		return
	}
	if deliveryQuery != nil {
		a.observeMediaStep(t, result, nil)
		r, e := a.delivery.seal(t, *deliveryQuery)
		if e != nil {
			err = mediaDeliveryFailure()
		} else {
			r.Version = 1
			t.Delivery = r
			t.Task.Status = "INGESTING"
			t.Task.Progress = 99
			t.Task.Artifacts = []Artifact{}
			t.Task.CanReconcile = false
			t.Task.Error = nil
			t.Task.Version++
			t.Task.UpdatedAt = time.Now().UTC()
			t.NextPoll = time.Now()
			a.applyMemoryMediaSchedule(t)
			a.publishTaskLocked(t.Task)
			return
		}
	}
	if err != nil && a.ctx.Err() != nil {
		// Process shutdown or writer loss is not a provider failure. Preserve the
		// durable handle for polling after restart; a handleless SUBMITTING record
		// remains ambiguous and will recover as SUBMIT_UNKNOWN, never resubmit.
		return
	}
	if err != nil {
		var providerError *MediaProviderError
		if errors.As(err, &providerError) {
			t.Task.Error = &APIError{Code: providerError.Code, Message: providerError.Message}
			switch {
			case providerError.SubmissionUnknown:
				t.Task.Status = "SUBMIT_UNKNOWN"
			case providerError.ReconcileRequired:
				t.Task.Status = "RECONCILE_REQUIRED"
			case providerError.Retryable && handle.ID != "":
				t.Task.Status = "PROVIDER_STATE_UNKNOWN"
			default:
				t.Task.Status = "FAILED"
			}
		} else if handle.ID == "" && a.config.Mode == "live" {
			t.Task.Status = "SUBMIT_UNKNOWN"
			t.Task.Error = &APIError{Code: "SUBMISSION_UNKNOWN", Message: "提交结果未知，禁止自动重提"}
		} else {
			t.Task.Status = "FAILED"
			t.Task.Error = &APIError{Code: "PROVIDER_FAILED", Message: "媒体任务执行失败"}
		}
	} else {
		t.Handle = handle
		t.Task.Error = result.Error
		t.Task.Status = result.Status
		t.Task.Progress = result.Progress
		if len(result.Artifacts) > 0 {
			t.Task.Artifacts = result.Artifacts
		}
	}
	t.Task.Version++
	t.Task.UpdatedAt = time.Now().UTC()
	t.Task.CanReconcile = a.canReconcileTask(t)
	interval := a.config.PollInterval
	if a.config.Mode == "live" && interval < 3*time.Second {
		interval = 3 * time.Second
	}
	if result.RetryAfter > interval {
		interval = result.RetryAfter
	}
	var providerError *MediaProviderError
	if errors.As(err, &providerError) && providerError.RetryAfter > interval {
		interval = providerError.RetryAfter
	}
	t.NextPoll = time.Now().Add(interval)
	a.observeMediaStep(t, result, err)
	applyMediaScheduleExceeded(t)
	a.applyMemoryMediaSchedule(t)
	a.registerMemoryTaskLibraryLocked(t.Task)
	a.publishTaskLocked(t.Task)
}

func (a *App) String() string {
	return fmt.Sprintf("mode=%s storage=%s auth=%s workers=%d/%d", a.config.Mode, a.storageMode(), a.config.AuthMode, a.config.RunWorkers, a.config.TaskWorkers)
}

func (a *App) storageMode() string {
	if a.store != nil {
		return "postgres"
	}
	return "memory"
}

// A fixed timer checkpoints even a stalled stream's final received delta.
// It never creates a database write for every token.
func (a *App) checkpointRuns() {
	defer a.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			if err := a.store.health(); err != nil {
				return
			}
			a.mu.Lock()
			var changed []*runRecord
			for _, r := range a.runs {
				if !terminal(r.Run.Status) && r.Run.Seq > r.CheckpointSeq {
					changed = append(changed, r)
				}
			}
			a.mu.Unlock()
			for _, r := range changed {
				if err := a.checkpointRun(r); err != nil {
					break
				}
			}
		}
	}
}

// Conversation history and project feeds consume the periodic business
// checkpoint. The per-run write barrier serializes it with newer deltas and
// cancellation, while unrelated work can still acquire App.mu.
func (a *App) checkpointRun(r *runRecord) error {
	a.mu.Lock()
	if a.ctx.Err() != nil || a.runs[r.Run.ID] != r || terminal(r.Run.Status) ||
		r.Run.Seq <= r.CheckpointSeq || r.streamWrite != nil || r.streamCancel != nil {
		a.mu.Unlock()
		return nil
	}
	next := copyRun(r.Run)
	watch := r.leaseWatch
	r.streamWrite = make(chan struct{})
	a.mu.Unlock()
	var err error
	if watch != nil {
		err = a.store.saveLeasedRun(&next, watch.lease, next.Seq)
		watch.reject(err)
	} else {
		err = a.store.saveRun(next)
	}
	a.mu.Lock()
	close(r.streamWrite)
	r.streamWrite = nil
	if err == nil {
		r.CheckpointSeq = next.Seq
		r.LastCheckpoint = time.Now()
	}
	a.mu.Unlock()
	return err
}

type subscriptionScope struct{ WorkspaceID, ProjectID string }

func (a *App) canReconcileTask(t *taskRecord) bool {
	if _, err := mediaFrozenChannel(t, mediaLaneForTask(t)); err != nil {
		return false
	}
	if t.Delivery != nil {
		return false
	}
	if t.Task.Moderation != nil && t.Task.Moderation.State != "PENDING" {
		return false
	}
	if t.MediaModeration != nil && len(t.MediaModeration.Candidates) > 0 {
		return false
	}
	return a.media != nil && t.Handle.ID != "" && t.Task.ProviderMode == a.config.Mode && (t.Task.Status == "RECONCILE_REQUIRED" || t.Task.Status == "SUBMIT_UNKNOWN" || t.Task.Status == "PROVIDER_STATE_UNKNOWN")
}
func (a *App) reconcileTask(id string, actorIDs ...string) (Task, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, err := a.getTaskLocked(id)
	if err != nil {
		return Task{}, err
	}
	if !a.canReconcileTask(t) {
		return Task{}, failure(409, "RECONCILE_UNAVAILABLE", "此任务没有可安全查询的原受理记录，不能重新提交生成")
	}
	if t.Task.ReconcileAfter != nil && time.Now().Before(*t.Task.ReconcileAfter) {
		return Task{}, failure(429, "RECONCILE_COOLDOWN", "请等待原核对冷却结束后再试")
	}
	audit := newAuditEvent(t.Task.WorkspaceID, t.Task.ProjectID, firstActor(actorIDs), "task.reconcile_requested", "task", t.Task.ID, AuditDetails{FromStatus: t.Task.Status, ToStatus: "QUEUED", Kind: t.Task.Kind})
	after := time.Now().UTC().Add(15 * time.Second)
	candidate := *t
	candidate.ScheduleReconcile = true
	candidate.Lease, candidate.LeaseComplete, candidate.LeaseStatus = nil, false, ""
	candidate.Task.ReconcileAfter = &after
	candidate.Task.CanReconcile = false
	candidate.Task.Status = "QUEUED"
	candidate.Task.Error = nil
	candidate.Task.Version++
	candidate.Task.UpdatedAt = time.Now().UTC()
	candidate.NextPoll = time.Now()
	if a.store != nil {
		if err := a.store.saveTask(&candidate, audit); err != nil {
			return Task{}, err
		}
	} else if err := a.checkMemoryReconcileSchedule(&candidate); err != nil {
		return Task{}, err
	}
	t.Task = candidate.Task
	t.Schedule = cloneMediaTaskSchedule(candidate.Schedule)
	t.NextPoll = candidate.NextPoll
	t.Lease, t.LeaseComplete, t.LeaseStatus, t.InFlight = nil, false, "", false
	a.appendMemoryAuditLocked(audit)
	a.publishTaskLocked(t.Task)
	return copyTask(t.Task), nil
}
