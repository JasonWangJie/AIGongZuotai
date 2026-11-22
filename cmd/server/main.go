package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aigongzuotai/internal/studio"
	"aigongzuotai/internal/videoexport"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "chat-execution-child" {
		if len(os.Args) != 2 || studio.RunChatExecutionChild(os.Stdin, os.Stdout) != nil {
			os.Exit(1)
		}
		return
	}
	address := os.Getenv("APP_ADDR")
	if address == "" {
		address = "127.0.0.1:8080"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		slog.Error("APP_ADDR must use a literal loopback IP; public deployment is not enabled")
		os.Exit(1)
	}
	videoSeconds := 0
	if raw := os.Getenv("ASYNC_VIDEO_SECONDS"); raw != "" {
		videoSeconds, err = strconv.Atoi(raw)
		if err != nil {
			slog.Error("ASYNC_VIDEO_SECONDS must be an integer")
			os.Exit(1)
		}
	}
	var assetHosts []string
	for _, host := range strings.Split(os.Getenv("ASSET_ALLOWED_HOSTS"), ",") {
		if host = strings.TrimSpace(host); host != "" {
			assetHosts = append(assetHosts, host)
		}
	}
	admission, err := admissionFromEnv(os.LookupEnv)
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(1)
	}
	chatScheduling, err := chatSchedulingFromEnv(os.LookupEnv)
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(1)
	}
	chatChildren, err := chatChildrenFromEnv(os.LookupEnv)
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(1)
	}
	deltaBatching, err := enabledFromEnv("RUN_DELTA_BATCHING_ENABLED", os.LookupEnv)
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(1)
	}
	runTimeout, err := chatRunTimeoutFromEnv(os.LookupEnv)
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(1)
	}
	mediaScheduling, err := mediaSchedulingFromEnv(os.LookupEnv)
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(1)
	}
	assetStorage, err := assetStorageFromEnv(os.LookupEnv)
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(1)
	}
	taskLease, err := taskLeaseFromEnv(os.LookupEnv)
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(1)
	}
	mail, err := mailFromEnv(os.LookupEnv)
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(1)
	}
	longHistory, err := enabledFromEnv("LONG_CONVERSATION_HISTORY_ENABLED", os.LookupEnv)
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(1)
	}
	replayConnections, err := replayConnectionsFromEnv(os.LookupEnv)
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(1)
	}
	operations, err := enabledFromEnv("OPERATIONS_ENABLED", os.LookupEnv)
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(1)
	}
	moderation, err := moderationFromEnv(os.LookupEnv)
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(1)
	}
	mediaModeration, err := mediaModerationFromEnv(os.LookupEnv)
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(1)
	}
	app, err := studio.New(studio.Config{Mode: os.Getenv("PROVIDER_MODE"), BaseURL: os.Getenv("NEWAPI_BASE_URL"), APIKey: os.Getenv("NEWAPI_API_KEY"), ChatModel: os.Getenv("NEWAPI_CHAT_MODEL"), WebDir: os.Getenv("APP_WEB_DIR"),
		ChatScheduling:           chatScheduling,
		LocaleTrustedProxies:     os.Getenv("LOCALE_TRUSTED_PROXIES"),
		ChatChildren:             chatChildren,
		RunDeltaBatching:         deltaBatching,
		RunTimeout:               runTimeout,
		MediaScheduling:          mediaScheduling,
		MediaDelivery:            studio.MediaDeliveryConfig{EncryptionKey: os.Getenv("MEDIA_DELIVERY_KEY")},
		MediaModeration:          mediaModeration,
		Moderation:               moderation,
		OperationsEnabled:        operations,
		LongConversationHistory:  longHistory,
		RunReplayReadConnections: replayConnections,
		VideoExport:              videoexport.Config{FFmpegPath: os.Getenv("VIDEO_EXPORT_FFMPEG"), FFprobePath: os.Getenv("VIDEO_EXPORT_FFPROBE"), TempDir: os.Getenv("VIDEO_EXPORT_TEMP_DIR")},
		OrderCatalogFile:         os.Getenv("ORDER_CATALOG_FILE"),
		BillingMode:              os.Getenv("BILLING_MODE"), BillingPriceFile: os.Getenv("BILLING_PRICE_FILE"), Mail: mail, Admission: admission, AssetStorage: assetStorage, TaskLeaseDuration: taskLease, MetricsToken: os.Getenv("METRICS_TOKEN"), ModelCatalogFile: os.Getenv("MODEL_CATALOG_FILE"), ReferenceDir: os.Getenv("REFERENCE_DIR"),
		DatabaseURL: os.Getenv("DATABASE_URL"), AuthMode: os.Getenv("AUTH_MODE"), AsyncImageBaseURL: os.Getenv("ASYNC_IMAGE_BASE_URL"), AsyncImageAPIKey: os.Getenv("ASYNC_IMAGE_API_KEY"), AsyncImageModel: os.Getenv("ASYNC_IMAGE_MODEL"), AsyncVideoModel: os.Getenv("ASYNC_VIDEO_MODEL"), AsyncImageCredentialID: os.Getenv("ASYNC_IMAGE_CREDENTIAL_ID"), AssetDir: os.Getenv("ASSET_DIR"), AllowedAssetHosts: assetHosts, AsyncVideoSeconds: videoSeconds, AsyncVideoResolution: os.Getenv("ASYNC_VIDEO_RESOLUTION")})
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(1)
	}
	server := &http.Server{Addr: address, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		app.Close()
		slog.Error("cannot listen", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverError := make(chan error, 1)
	go func() { serverError <- server.Serve(listener) }()
	slog.Info("local workbench started", "address", address, "runtime", app.String(), "warning", "single writer; loopback only; not a public deployment")
	select {
	case <-ctx.Done():
	case err = <-serverError:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server stopped", "error", err)
		}
	}
	app.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		slog.Warn("HTTP drain timed out")
	}
}

// Unset fields are defaulted together by Config; explicit invalid values must
// never quietly disable scheduling protection. Errors contain no raw values.
func chatSchedulingFromEnv(lookup func(string) (string, bool)) (studio.ChatSchedulingConfig, error) {
	var config studio.ChatSchedulingConfig
	for _, setting := range []struct {
		name   string
		target *int
		max    int
	}{
		{"CHAT_WORKERS", &config.Workers, 64},
		{"CHAT_QUEUE_SIZE", &config.QueueSize, 4096},
		{"CHAT_WORKSPACE_CONCURRENCY", &config.WorkspaceConcurrency, 64},
		{"CHAT_MODEL_CONCURRENCY", &config.ModelConcurrency, 64},
		{"CHAT_WORKSPACE_QUEUE_SIZE", &config.WorkspaceQueueSize, 4096},
	} {
		raw, present := lookup(setting.name)
		if !present {
			continue
		}
		valid := len(raw) > 0 && len(raw) <= 4
		for _, ch := range raw {
			valid = valid && ch >= '0' && ch <= '9'
		}
		value, err := strconv.Atoi(raw)
		if !valid || err != nil || value < 1 || value > setting.max {
			return studio.ChatSchedulingConfig{}, fmt.Errorf("%s must be decimal digits between 1 and %d", setting.name, setting.max)
		}
		*setting.target = value
	}
	return config, nil
}

func moderationFromEnv(lookup func(string) (string, bool)) (studio.ModerationConfig, error) {
	get := func(name string) string { value, _ := lookup(name); return value }
	mode, present := lookup("MODERATION_MODE")
	if !present {
		mode = "disabled"
	}
	if mode != "disabled" && mode != "rules" && mode != "http" {
		return studio.ModerationConfig{}, errors.New("MODERATION_MODE must be disabled, rules or http")
	}
	return studio.ModerationConfig{Mode: mode, PolicyFile: get("MODERATION_POLICY_FILE"), Endpoint: get("MODERATION_ENDPOINT"), APIKey: get("MODERATION_API_KEY"), CredentialID: get("MODERATION_CREDENTIAL_ID")}, nil
}

func mediaModerationFromEnv(lookup func(string) (string, bool)) (studio.MediaModerationConfig, error) {
	get := func(name string) string { value, _ := lookup(name); return value }
	mode, present := lookup("MEDIA_MODERATION_MODE")
	if !present {
		mode = "disabled"
	}
	if mode != "disabled" && mode != "fixtures" && mode != "http" {
		return studio.MediaModerationConfig{}, errors.New("MEDIA_MODERATION_MODE must be disabled, fixtures or http")
	}
	return studio.MediaModerationConfig{Mode: mode, PolicyFile: get("MEDIA_MODERATION_POLICY_FILE"), Endpoint: get("MEDIA_MODERATION_ENDPOINT"), APIKey: get("MEDIA_MODERATION_API_KEY"), CredentialID: get("MEDIA_MODERATION_CREDENTIAL_ID")}, nil
}

func enabledFromEnv(name string, lookup func(string) (string, bool)) (bool, error) {
	raw, present := lookup(name)
	if !present || raw == "false" {
		return false, nil
	}
	if raw == "true" {
		return true, nil
	}
	return false, fmt.Errorf("%s must be true or false", name)
}

func replayConnectionsFromEnv(lookup func(string) (string, bool)) (int, error) {
	value, present := lookup("RUN_REPLAY_READ_CONNECTIONS")
	if !present {
		return 0, nil
	}
	if len(value) != 1 || value[0] < '0' || value[0] > '4' {
		return 0, errors.New("RUN_REPLAY_READ_CONNECTIONS must be a decimal digit between 0 and 4")
	}
	return int(value[0] - '0'), nil
}

func taskLeaseFromEnv(lookup func(string) (string, bool)) (time.Duration, error) {
	raw, present := lookup("TASK_LEASE_SECONDS")
	if !present {
		return 0, nil // Config supplies the bounded default.
	}
	valid := len(raw) > 0 && len(raw) <= 3
	for _, c := range raw {
		valid = valid && c >= '0' && c <= '9'
	}
	seconds, err := strconv.Atoi(raw)
	if !valid || err != nil || seconds < 5 || seconds > 300 {
		return 0, errors.New("TASK_LEASE_SECONDS must be decimal seconds between 5 and 300")
	}
	return time.Duration(seconds) * time.Second, nil
}

func chatRunTimeoutFromEnv(lookup func(string) (string, bool)) (time.Duration, error) {
	raw, present := lookup("CHAT_RUN_TIMEOUT_SECONDS")
	if !present {
		return 0, nil // RunTimeout defaults to ten minutes in studio.Config.
	}
	seconds, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || len(raw) > 4 || seconds < 1 || seconds > 3600 {
		return 0, errors.New("CHAT_RUN_TIMEOUT_SECONDS must be decimal seconds between 1 and 3600")
	}
	return time.Duration(seconds) * time.Second, nil
}

func assetStorageFromEnv(lookup func(string) (string, bool)) (studio.AssetStorageConfig, error) {
	get := func(name string) string { value, _ := lookup(name); return value }
	config := studio.AssetStorageConfig{Backend: get("ASSET_STORAGE_BACKEND"), Endpoint: get("S3_ENDPOINT"), Region: get("S3_REGION"), Bucket: get("S3_BUCKET"), Prefix: get("S3_PREFIX"), AccessKeyID: get("S3_ACCESS_KEY_ID"), SecretAccessKey: get("S3_SECRET_ACCESS_KEY"), SessionToken: get("S3_SESSION_TOKEN")}
	if value, present := lookup("S3_FORCE_PATH_STYLE"); present {
		if value != "true" && value != "false" {
			return config, errors.New("S3_FORCE_PATH_STYLE must be true or false when set")
		}
		config.ForcePathStyle = value == "true"
	}
	return config, nil
}

// Missing variables use Config defaults; present-but-empty, zero, malformed or
// excessive values reject startup rather than silently disabling protection.
func admissionFromEnv(lookup func(string) (string, bool)) (studio.AdmissionLimits, error) {
	var limits studio.AdmissionLimits
	for _, setting := range []struct {
		name   string
		target *int
		max    int
	}{
		{"ADMISSION_WORKSPACE_RUNS", &limits.WorkspaceRuns, 4096},
		{"ADMISSION_MODEL_RUNS", &limits.ModelRuns, 4096},
		{"ADMISSION_WORKSPACE_TASKS", &limits.WorkspaceTasks, 4096},
		{"ADMISSION_MODEL_TASKS", &limits.ModelTasks, 4096},
		{"ADMISSION_WORKSPACE_STREAMS", &limits.WorkspaceStreams, 128},
		{"ADMISSION_GLOBAL_STREAMS", &limits.GlobalStreams, 256},
	} {
		if raw, present := lookup(setting.name); present {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > setting.max {
				return limits, fmt.Errorf("%s must be an integer between 1 and %d", setting.name, setting.max)
			}
			*setting.target = value
		}
	}
	return limits, nil
}
