// Command skills-dashboard serves a local monitoring dashboard for an EKS
// workload, reading everything it shows from AWS CloudWatch.
//
// The binary carries the web UI inside it, so running it is the whole install.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/jhyeok1023/skills-dashboard/internal/api"
	"github.com/jhyeok1023/skills-dashboard/internal/awsx"
	"github.com/jhyeok1023/skills-dashboard/internal/config"
	"github.com/jhyeok1023/skills-dashboard/internal/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		addr    = flag.String("addr", "127.0.0.1", "address to listen on")
		port    = flag.Int("port", 8080, "port to listen on")
		envFile = flag.String("env", config.DefaultEnvFile, "path to the .env file holding AWS credentials")
		open    = flag.Bool("open", true, "open the dashboard in a browser on start")
		verbose = flag.Bool("verbose", false, "log at debug level")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	cfgPath, err := config.Path()
	if err != nil {
		return fmt.Errorf("locate config directory: %w", err)
	}
	store, err := config.NewStore(cfgPath)
	if err != nil {
		return err
	}
	cfg := store.Get()

	svc := &api.Service{
		Store:  store,
		Logger: logger,
		Cache: &awsx.Cache{
			TTL:      time.Duration(cfg.Limits.CacheTTLSeconds) * time.Second,
			ErrorTTL: 5 * time.Second,
		},
		Insights: &awsx.InsightsRunner{
			Concurrency: cfg.Limits.InsightsConcurrency,
			Timeout:     time.Duration(cfg.Limits.QueryTimeoutSeconds) * time.Second,
		},
		Metrics: &awsx.MetricFetcher{},
	}

	// Credentials are read once at start. A failure is carried rather than
	// fatal: the UI still comes up and the settings page explains what to fix,
	// which beats a process that exits before the operator can see why.
	creds, err := config.LoadCredentials(*envFile)
	if err != nil {
		svc.CredentialError = err
	} else if err := creds.Validate(); err != nil {
		svc.CredentialError = err
	} else {
		if cfg.Region != "" && creds.Region == "" {
			creds.Region = cfg.Region
		}
		clients, err := awsx.New(context.Background(), creds, cfg.WAFRegion)
		if err != nil {
			svc.CredentialError = err
		} else {
			svc.Clients = clients
			svc.Insights.API = clients.Logs

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			identity, err := awsx.WhoAmI(ctx, clients.STS, creds.Region)
			cancel()
			if err != nil {
				svc.CredentialError = fmt.Errorf("자격증명 확인 실패: %w", err)
			} else {
				svc.Identity = identity
				logger.Info("credentials accepted", "account", identity.Account, "region", identity.Region,
					"key", creds.Redacted())
			}
		}
	}
	if svc.CredentialError != nil {
		logger.Warn("AWS is unavailable; the UI will explain what to configure",
			"error", svc.CredentialError, "envFile", *envFile)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", svc.Handler())
	mux.Handle("/", web.Handler())

	listenAddr := net.JoinHostPort(*addr, fmt.Sprint(*port))
	srv := &http.Server{
		Addr:    listenAddr,
		Handler: mux,
		// Every timeout is set on purpose. The reference implementation used
		// http.ListenAndServe with none, so nothing bounded how long a handler
		// could hold a slow AWS call open, and stuck requests simply piled up.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listenAddr, err)
	}

	url := fmt.Sprintf("http://%s", listenAddr)
	logger.Info("dashboard is listening", "url", url)
	if *open {
		openBrowser(url, logger)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// openBrowser is best effort. Failing to open a browser is not a reason to
// refuse to serve.
func openBrowser(url string, logger *slog.Logger) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		logger.Debug("could not open a browser", "error", err)
		return
	}
	go func() { _ = cmd.Wait() }()
}
