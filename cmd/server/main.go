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
	"os"
	"strings"
	"time"

	configaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cmd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/githubauth"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/inputs"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/safemode"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/telemetry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
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
	serverOptions := []api.ServerOption{api.WithRequestLoggerFactory(func(*config.Config, string) logging.RequestLogger { return nil })}
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

// runPlan is what one run does: serve, or run one command and exit.
type runPlan struct {
	profile   string
	cfg       *config.Config
	telemetry telemetry.Settings
	// telemetryProbe runs the telemetry probe instead of serving.
	telemetryProbe bool
	// validate stops once the inputs are valid, before any other work.
	validate   bool
	localModel bool
	command    func(*config.Config)
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
	cfg, errParse := config.ParseConfigBytes(doc)
	if errParse != nil {
		return nil, fmt.Errorf("parse configuration: %w", errParse)
	}
	authDir, errAuthDir := util.ResolveAuthDir(cfg.AuthDir)
	if errAuthDir != nil {
		return nil, errAuthDir
	}
	cfg.AuthDir = authDir
	command, errCommand := selectCommand(eff)
	if errCommand != nil {
		return nil, errCommand
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
	return &runPlan{profile: eff.Profile, cfg: cfg, telemetry: settings, telemetryProbe: eff.Bool("telemetry-probe"),
		validate: eff.Bool("validate"), localModel: eff.Bool("local-model"), command: command}, nil
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
	if len(requested) > 1 {
		return nil, fmt.Errorf("choose one command, not %s", strings.Join(requested, ", "))
	}
	return command, nil
}
