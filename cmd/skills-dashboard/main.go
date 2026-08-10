// Command skills-dashboard serves a local monitoring dashboard for an EKS
// workload, reading everything it shows from AWS CloudWatch.
//
// The binary carries the web UI inside it, so running it is the whole install.
package main

import (
	"bufio"
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
	"strings"
	"syscall"
	"time"

	"github.com/jhyeok1023/skills-dashboard/internal/api"
	"github.com/jhyeok1023/skills-dashboard/internal/awsx"
	"github.com/jhyeok1023/skills-dashboard/internal/config"
	"github.com/jhyeok1023/skills-dashboard/internal/web"
)

func main() {
	if err := run(); err != nil {
		reportFatal(err)
		os.Exit(1)
	}
}

// reportFatal says why the dashboard is not running, in a way that survives the
// way it was started.
//
// Double-clicked in Explorer, the process gets a console of its own and Windows
// tears the window down with the process — so the reason scrolls past and
// disappears, and the dashboard looks like it simply refuses to open. When this
// process is the console's only occupant, the message waits for a keypress.
// Started from a terminal, nothing is held: the text is already on screen and
// the shell prompt should come straight back.
func reportFatal(err error) {
	fmt.Fprintf(os.Stderr, "\n대시보드를 시작하지 못했습니다.\n\n  %v\n\n", err)
	if !ownsConsole() {
		return
	}
	fmt.Fprintln(os.Stderr, "엔터를 누르면 창이 닫힙니다.")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func run() error {
	var (
		addr    = flag.String("addr", "127.0.0.1", "address to listen on")
		port    = flag.Int("port", 8080, "port to listen on")
		envFile = flag.String("env", "", "path to the .env file holding AWS credentials (default: next to the binary, then ~/.skills-dashboard)")
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
		return fmt.Errorf("read the config at %s: %w", cfgPath, err)
	}
	cfg := store.Get()

	// Anything the stored config had to give up is said twice: here for whoever
	// is watching a terminal, and through /api/meta for the settings page,
	// which is where the value has to be chosen again.
	for _, note := range store.Notices() {
		logger.Warn("config was repaired on load", "detail", note, "file", cfgPath)
	}

	svc := &api.Service{
		Store:         store,
		Logger:        logger,
		ConfigNotices: store.Notices(),
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

	// A path given on the command line has to exist. Anything else is the
	// operator asking for a file that is not there, and continuing without
	// credentials would answer that with a message about the wrong subject.
	envPath, tried, err := config.ResolveEnvFile(*envFile)
	if err != nil {
		return fmt.Errorf("read the .env at %s: %w", *envFile, err)
	}
	if envPath != "" {
		logger.Info("reading credentials", "envFile", envPath)
	} else {
		logger.Warn("no .env found; falling back to the process environment", "tried", tried)
	}
	svc.EnvFile = envPath

	// Credentials are read once at start. A failure is carried rather than
	// fatal: the UI still comes up and the settings page explains what to fix,
	// which beats a process that exits before the operator can see why.
	creds, err := config.LoadCredentials(envPath)
	if err != nil {
		svc.CredentialError = err
	} else if err := creds.Validate(); err != nil {
		// With no file anywhere, the missing keys are only half the story: the
		// other half is where one was expected to be.
		if envPath == "" {
			err = fmt.Errorf("%w (.env 를 다음에서 찾지 못했습니다: %s)", err, strings.Join(tried, ", "))
		}
		svc.CredentialError = err
	} else {
		clients, err := awsx.New(context.Background(), creds, cfg.WAFRegion)
		if err != nil {
			svc.CredentialError = err
		} else {
			svc.Clients = clients
			svc.Insights.API = clients.Logs

			// WAF logs need their own runner. A CLOUDFRONT-scoped web ACL
			// publishes only into us-east-1, so querying its log group through
			// the working-region client fails on a group that is not there.
			// When the regions coincide the runner is shared, which keeps the
			// concurrency limit a single pool rather than two of the same size.
			if clients.WAFRegion == clients.Region {
				svc.InsightsGlobal = svc.Insights
			} else {
				svc.InsightsGlobal = &awsx.InsightsRunner{
					Concurrency: cfg.Limits.InsightsConcurrency,
					Timeout:     time.Duration(cfg.Limits.QueryTimeoutSeconds) * time.Second,
					API:         clients.LogsGlobal,
				}
			}

			// The credentials decide the region; config.json only records it.
			// Leaving a stale region in the file is what let the dashboard
			// reason about one region while calling another.
			if cfg.Region != clients.Region {
				logger.Info("recording the credentials' region in the config",
					"was", cfg.Region, "now", clients.Region)
				cfg.Region = clients.Region
				if err := store.Set(cfg); err != nil {
					logger.Warn("could not save the region to the config", "error", err)
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			identity, err := awsx.WhoAmI(ctx, clients.STS, clients.Region)
			cancel()
			if err != nil {
				svc.CredentialError = fmt.Errorf("자격증명 확인 실패: %w", err)
			} else {
				identity.WAFRegion = clients.WAFRegion
				svc.Identity = identity
				logger.Info("credentials accepted", "account", identity.Account, "region", identity.Region,
					"wafRegion", identity.WAFRegion, "key", creds.Redacted())
			}
		}
	}
	if svc.CredentialError != nil {
		logger.Warn("AWS is unavailable; the UI will explain what to configure",
			"error", svc.CredentialError, "envFile", envPath)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", svc.Handler())
	mux.Handle("/", web.Handler())

	ln, err := listen(*addr, *port, portWasChosen(), logger)
	if err != nil {
		return err
	}

	listenAddr := ln.Addr().String()
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

// portFallbacks is how many ports past the default are tried before giving up.
const portFallbacks = 5

// portWasChosen reports whether -port was given on the command line, as opposed
// to being left at its default.
func portWasChosen() bool {
	chosen := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "port" {
			chosen = true
		}
	})
	return chosen
}

// listen binds the dashboard's socket.
//
// A port already in use used to end the process, and on Windows that means a
// double-clicked console window vanishes with the reason inside it. So the
// default port is only a starting point: the next few are tried and the one
// that worked is logged. A port the operator named explicitly is honoured — if
// they said 8080 they want 8080 — and the failure says what to do about it.
func listen(addr string, port int, chosen bool, logger *slog.Logger) (net.Listener, error) {
	last := port
	if !chosen {
		last = port + portFallbacks
	}
	for p := port; p <= last; p++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(addr, fmt.Sprint(p)))
		if err == nil {
			if p != port {
				logger.Warn("the requested port was busy; using the next free one",
					"requested", port, "using", p)
			}
			return ln, nil
		}
		if p == last {
			if chosen {
				return nil, fmt.Errorf(
					"%d 포트를 열 수 없습니다 (%v). 이미 사용 중이라면 --port 로 다른 포트를 지정하세요", p, err)
			}
			return nil, fmt.Errorf(
				"%d..%d 포트가 모두 사용 중입니다 (%v). --port 로 다른 포트를 지정하세요", port, last, err)
		}
	}
	// Unreachable: the loop always returns on its last iteration.
	return nil, fmt.Errorf("listen on %s", addr)
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
