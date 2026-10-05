// Command server is the core service. It owns domain authority, policy and
// the public edge; this skeleton serves liveness and readiness only.
//
// "server healthcheck [--url URL]" is the container healthcheck.
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
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"

	"core/internal/config"
	"core/internal/health"
	"core/internal/observability"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(os.Args[2:]))
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func healthcheck(args []string) int {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	url := fs.String("url", "http://127.0.0.1:8180/healthz", "URL that must answer 200")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(*url)
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run() error {
	cfg, err := config.Load(os.Getenv, config.EnvSecrets{Getenv: os.Getenv})
	if err != nil {
		return err
	}
	logger := observability.NewLogger(os.Stdout, slog.LevelInfo, cfg.SecretValues())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	poolCfg, err := pgxpool.ParseConfig("")
	if err != nil {
		return err
	}
	poolCfg.ConnConfig.Host = cfg.DB.Host
	poolCfg.ConnConfig.Port = uint16(cfg.DB.Port)
	poolCfg.ConnConfig.Database = cfg.DB.Name
	poolCfg.ConnConfig.User = cfg.DB.User
	poolCfg.ConnConfig.Password = cfg.DB.Password
	poolCfg.ConnConfig.ConnectTimeout = 2 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return errors.New("cannot configure database pool")
	}
	defer pool.Close()

	s3Client := s3.New(s3.Options{
		BaseEndpoint: aws.String(cfg.ObjectStore.Endpoint),
		Region:       cfg.ObjectStore.Region,
		UsePathStyle: cfg.ObjectStore.ForcePathStyle,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.ObjectStore.AccessKey, cfg.ObjectStore.SecretKey, ""),
	})

	checks := []health.Check{
		health.Postgres(pool),
		health.Redis(net.JoinHostPort(cfg.Redis.Host, strconv.Itoa(cfg.Redis.Port)), cfg.Redis.Password),
		health.ObjectStore(s3Client, cfg.ObjectStore.Bucket),
	}
	if cfg.Intelligence.Enabled() {
		checks = append(checks, health.Intelligence(cfg.Intelligence.URL))
	}
	h := health.Handler{ProductName: cfg.ProductName, Checks: checks}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.Liveness)
	mux.HandleFunc("GET /readyz", h.Readiness)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           observability.Middleware(logger, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("core listening", slog.String("addr", cfg.ListenAddr), slog.String("app_env", cfg.AppEnv))

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
