// Package main is the entry point of the CLI Proxy API server, which serves
// OpenAI, Gemini, and Claude compatible APIs backed by CLI model credentials.
// It is the only code that reads process inputs: it resolves them through
// the inputs schema and passes typed configuration on.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	configaccess "github.com/AsheTheWings/cliapiproxy/internal/access/config_access"
	"github.com/AsheTheWings/cliapiproxy/internal/api"
	"github.com/AsheTheWings/cliapiproxy/internal/buildinfo"
	"github.com/AsheTheWings/cliapiproxy/internal/cmd"
	"github.com/AsheTheWings/cliapiproxy/internal/config"
	"github.com/AsheTheWings/cliapiproxy/internal/generations"
	"github.com/AsheTheWings/cliapiproxy/internal/generations/schema"
	"github.com/AsheTheWings/cliapiproxy/internal/generations/snapshots"
	"github.com/AsheTheWings/cliapiproxy/internal/githubauth"
	"github.com/AsheTheWings/cliapiproxy/internal/inputs"
	"github.com/AsheTheWings/cliapiproxy/internal/keystate"
	"github.com/AsheTheWings/cliapiproxy/internal/logging"
	"github.com/AsheTheWings/cliapiproxy/internal/misc"
	"github.com/AsheTheWings/cliapiproxy/internal/redisqueue"
	"github.com/AsheTheWings/cliapiproxy/internal/registry"
	"github.com/AsheTheWings/cliapiproxy/internal/runtime/executor/helps"
	"github.com/AsheTheWings/cliapiproxy/internal/safemode"
	"github.com/AsheTheWings/cliapiproxy/internal/telemetry"
	_ "github.com/AsheTheWings/cliapiproxy/internal/translator"
	"github.com/AsheTheWings/cliapiproxy/internal/util"
	sdkAuth "github.com/AsheTheWings/cliapiproxy/sdk/auth"
	coreauth "github.com/AsheTheWings/cliapiproxy/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Build information, stamped at build time.
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

func init() {
	logging.SetupBaseLogger()
	buildinfo.Version = Version
	buildinfo.Commit = Commit
	buildinfo.BuildDate = BuildDate
}

func main() {
	os.Exit(run())
}

func run() int {
	process := inputs.Process{Args: os.Args[1:], Environ: os.Environ()}
	// The environment is consumed here, so neither project code nor a
	// dependency reads a process input the schema has not validated.
	os.Clearenv()
	plan, errResolve := resolveRun(process, inputs.OSFiles{})
	if errors.Is(errResolve, flag.ErrHelp) {
		if schema, errSchema := inputs.Load(); errSchema == nil {
			fmt.Print(schema.Usage("CLIProxyAPI"))
			return 0
		}
	}
	if errResolve != nil {
		fmt.Fprintln(os.Stderr, errResolve)
		return 2
	}
	if plan.validate {
		fmt.Println("valid")
		return 0
	}
	if plan.migrate || plan.migrateStatus {
		return runMigrations(plan)
	}
	cfg := plan.cfg

	providers, errTelemetry := telemetry.Setup(context.Background(), plan.telemetry)
	if errTelemetry != nil {
		fmt.Fprintf(os.Stderr, "failed to set up telemetry: %v\n", errTelemetry)
		return 1
	}
	defer func() {
		ctxShutdown, cancel := context.WithTimeout(context.Background(), telemetryShutdownTimeout)
		defer cancel()
		_ = providers.Shutdown(ctxShutdown)
	}()
	logging.DiscardLocalOutput()
	telemetry.InstallLogEvents(log.StandardLogger())

	if plan.telemetryProbe {
		// The correlation identifier is the command's result; it names the
		// probe's records at their destination.
		fmt.Println(telemetry.Probe(context.Background()))
		return 0
	}

	redisqueue.SetUsageStatisticsEnabled(cfg.UsageStatisticsEnabled)
	redisqueue.SetRetentionSeconds(cfg.RedisUsageQueueRetentionSeconds)
	coreauth.SetQuotaCooldownDisabled(cfg.DisableCooling)
	coreauth.SetTransientErrorCooldownSeconds(cfg.TransientErrorCooldownSeconds)
	log.Infof("CLIProxyAPI Version: %s, Commit: %s, BuiltAt: %s, Profile: %s", buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate, plan.profile)
	util.SetLogLevel(cfg)
	githubauth.SetToken(cfg.GitHubToken)
	sdkAuth.RegisterTokenStore(sdkAuth.NewFileTokenStore())
	configaccess.Register(&cfg.SDKConfig)

	if plan.command != nil {
		plan.command(cfg)
		return 0
	}
	// Request and error-request files would be a second diagnostics sink.
	serverOptions := []api.ServerOption{
		api.WithRequestLoggerFactory(func(*config.Config, string) logging.RequestLogger { return nil }),
		api.WithKeyStore(plan.keys),
	}
	if plan.store.Enabled() {
		ctxStart, cancel := context.WithTimeout(context.Background(), storeCheckTimeout)
		store, errStart := generations.Start(ctxStart, plan.store, generations.WriterOptions{
			PayloadLimit:   plan.payloadLimit,
			QueueBytes:     plan.queueBytes,
			ClientKeyLabel: plan.keys.ClientKeyLabel,
		})
		cancel()
		if errStart != nil {
			fmt.Fprintf(os.Stderr, "store: %v\n", errStart)
			return 1
		}
		// Queued generations are written before telemetry shuts down.
		defer store.Close()
		serverOptions = append(serverOptions, api.WithAuthManagerHook(func(manager *coreauth.Manager) {
			client := func(ctx context.Context, auth *coreauth.Auth) *http.Client {
				return helps.NewProxyAwareHTTPClient(ctx, cfg, auth, snapshots.PollTimeout)
			}
			if errPoll := store.StartPolling(manager.List, client); errPoll != nil {
				log.WithError(errPoll).Error("usage polling did not start")
			}
		}))
	}
	if safemode.HasExampleAPIKeys(cfg.APIKeys) {
		matches := safemode.ExampleAPIKeys(cfg.APIKeys)
		log.WithField("api_keys", strings.Join(matches, ",")).Error("unsafe example API key configured; proxy API endpoints disabled until api-keys is updated")
		serverOptions = append(serverOptions, api.WithExampleAPIKeySafeMode())
	}
	if plan.localModel {
		log.Info("Local model mode: using embedded catalogs unless an explicit catalog source is configured")
	}
	misc.StartAntigravityVersionUpdater(context.Background())
	registry.SetLocalModelCatalogs(plan.localModel)
	cmd.StartServiceWithPluginHost(cfg, "", "", nil, serverOptions...)
	return 0
}

// telemetryShutdownTimeout bounds the final flush of every signal.
const telemetryShutdownTimeout = 10 * time.Second

// storeCheckTimeout bounds connecting to the store and checking its schema
// at startup.
const storeCheckTimeout = 30 * time.Second

// migrationTimeout bounds one -migrate or -migrate-status run.
const migrationTimeout = 10 * time.Minute

// runMigrations applies the store's pending migrations, or reports their
// state, and returns the exit code.
func runMigrations(plan *runPlan) int {
	ctx, cancel := context.WithTimeout(context.Background(), migrationTimeout)
	defer cancel()
	pool, err := generations.Open(ctx, plan.store)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer pool.Close()
	migrations, err := schema.Discover(schema.Migrations())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if plan.migrateStatus {
		report, errStatus := schema.Status(ctx, pool, migrations)
		if errStatus != nil {
			fmt.Fprintln(os.Stderr, errStatus)
			return 1
		}
		for _, line := range report.Lines() {
			fmt.Println(line)
		}
		if !report.Clean() {
			return 3
		}
		return 0
	}
	applied, err := schema.Apply(ctx, pool, migrations)
	for _, m := range applied {
		fmt.Printf("applied %s\n", m.Name)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(applied) == 0 {
		fmt.Println("no pending migrations")
	}
	return 0
}

// runPlan is what one run does: serve, or run one command and exit.
type runPlan struct {
	profile string
	cfg     *config.Config
	keys    *keystate.Store
	store   generations.Settings
	// payloadLimit and queueBytes bound capture's payloads and write queue.
	payloadLimit int
	queueBytes   int64
	telemetry    telemetry.Settings
	// telemetryProbe runs the telemetry probe instead of serving.
	telemetryProbe bool
	// validate stops once the inputs are valid, before any other work.
	validate bool
	// migrate applies the store's pending migrations; migrateStatus reports
	// them.
	migrate       bool
	migrateStatus bool
	localModel    bool
	command       func(*config.Config)
}

func resolveRun(process inputs.Process, files inputs.Files) (*runPlan, error) {
	schema, errSchema := inputs.Load()
	if errSchema != nil {
		return nil, errSchema
	}
	eff, errResolve := inputs.Resolve(schema, process, files)
	if errResolve != nil {
		return nil, errResolve
	}
	doc, errDocument := eff.ConfigDocument()
	if errDocument != nil {
		return nil, fmt.Errorf("render configuration: %w", errDocument)
	}
	statePath := eff.String("keys.state-file")
	state, errState := readKeyState(files, statePath)
	if errState != nil {
		return nil, errState
	}
	keyStore, errStore := keystate.New(statePath, doc, state)
	if errStore != nil {
		return nil, errStore
	}
	cfg, errConfig := keyStore.Config()
	if errConfig != nil {
		return nil, errConfig
	}
	command, errCommand := selectCommand(eff)
	if errCommand != nil {
		return nil, errCommand
	}
	store := generations.Settings{
		Host:     eff.String("store.host"),
		Port:     int(eff.Int("store.port")),
		Database: eff.String("store.database"),
		User:     eff.String("store.user"),
		Password: eff.String("store.password"),
	}
	if !store.Enabled() && eff.Profile != "development" {
		return nil, errors.New("store.host is empty; only the development profile runs without the store")
	}
	if (eff.Bool("migrate") || eff.Bool("migrate-status")) && !store.Enabled() {
		return nil, errors.New("-migrate and -migrate-status need the store; set store.host")
	}
	headers, errHeaders := telemetry.ParseHeaders(eff.String("telemetry.otlp-headers"))
	if errHeaders != nil {
		return nil, fmt.Errorf("telemetry.otlp-headers: %w", errHeaders)
	}
	settings := telemetry.Settings{
		Traces:         eff.String("OTEL_TRACES_EXPORTER"),
		Metrics:        eff.String("OTEL_METRICS_EXPORTER"),
		Logs:           eff.String("OTEL_LOGS_EXPORTER"),
		Endpoint:       eff.String("OTEL_EXPORTER_OTLP_ENDPOINT"),
		Headers:        headers,
		ServiceVersion: serviceVersion(),
		Environment:    eff.String("CLIPROXY_DEPLOYMENT_ENVIRONMENT"),
		Console:        os.Stdout,
	}
	return &runPlan{profile: eff.Profile, cfg: cfg, keys: keyStore, store: store, telemetry: settings, telemetryProbe: eff.Bool("telemetry-probe"),
		payloadLimit: int(eff.Int("store.payload-limit-bytes")), queueBytes: eff.Int("store.queue-bytes"),
		validate: eff.Bool("validate"), migrate: eff.Bool("migrate"), migrateStatus: eff.Bool("migrate-status"),
		localModel: eff.Bool("local-model"), command: command}, nil
}

// readKeyState reads the key state file, which holds keys and so must be
// readable by its owner alone; an absent file is the empty state.
func readKeyState(files inputs.Files, path string) ([]byte, error) {
	data, mode, errRead := files.ReadFile(path)
	if errors.Is(errRead, fs.ErrNotExist) {
		return nil, nil
	}
	if errRead != nil {
		return nil, fmt.Errorf("keys.state-file %s: %w", path, errRead)
	}
	if mode&0o077 != 0 {
		return nil, fmt.Errorf("keys.state-file %s: readable by others; want mode 0600", path)
	}
	return data, nil
}

// serviceVersion is the build's version with its revision as build metadata,
// so each build reports a version of its own.
func serviceVersion() string {
	if buildinfo.Commit == "" || buildinfo.Commit == "none" {
		return buildinfo.Version
	}
	return buildinfo.Version + "+" + buildinfo.Commit
}

// selectCommand returns the one command the flags request, or nil to serve.
func selectCommand(eff *inputs.Effective) (func(*config.Config), error) {
	options := &cmd.LoginOptions{
		NoBrowser:    eff.Bool("no-browser"),
		CallbackPort: int(eff.Int("oauth-callback-port")),
	}
	logins := []struct {
		flag string
		run  func(*config.Config, *cmd.LoginOptions)
	}{
		{"codex-login", cmd.DoCodexLogin},
		{"codex-device-login", cmd.DoCodexDeviceLogin},
		{"claude-login", cmd.DoClaudeLogin},
		{"antigravity-login", cmd.DoAntigravityLogin},
		{"kimi-login", cmd.DoKimiLogin},
		{"kimi-ai-login", cmd.DoKimiAILogin},
		{"xai-login", cmd.DoXAILogin},
		{"devin-login", cmd.DoDevinLogin},
		{"meta-login", cmd.DoMetaLogin},
	}
	var requested []string
	var command func(*config.Config)
	for _, login := range logins {
		if eff.Bool(login.flag) {
			requested = append(requested, "-"+login.flag)
			command = func(cfg *config.Config) { login.run(cfg, options) }
		}
	}
	vertexImport := eff.String("vertex-import")
	vertexPrefix := eff.String("vertex-import-prefix")
	if vertexImport != "" {
		requested = append(requested, "-vertex-import")
		command = func(cfg *config.Config) { cmd.DoVertexImport(cfg, vertexImport, vertexPrefix) }
	} else if vertexPrefix != "" {
		return nil, fmt.Errorf("-vertex-import-prefix applies only with -vertex-import")
	}
	if siwcBind := eff.String("siwc-bind"); siwcBind != "" {
		requested = append(requested, "-siwc-bind")
		command = func(cfg *config.Config) { cmd.DoSIWCBind(cfg, siwcBind, options) }
	}
	if eff.Bool("telemetry-probe") {
		requested = append(requested, "-telemetry-probe")
	}
	if eff.Bool("validate") {
		requested = append(requested, "-validate")
	}
	for _, flag := range []string{"migrate", "migrate-status"} {
		if eff.Bool(flag) {
			requested = append(requested, "-"+flag)
		}
	}
	if len(requested) > 1 {
		return nil, fmt.Errorf("choose one command, not %s", strings.Join(requested, ", "))
	}
	return command, nil
}
