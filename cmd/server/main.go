// Command server runs the drviercar road-test operations API.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/vance1852/drviercar/internal/audit"
	"github.com/vance1852/drviercar/internal/clock"
	"github.com/vance1852/drviercar/internal/config"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/httpapi"
	"github.com/vance1852/drviercar/internal/idem"
	"github.com/vance1852/drviercar/internal/logging"
	"github.com/vance1852/drviercar/internal/service/auth"
	"github.com/vance1852/drviercar/internal/service/dataloop"
	"github.com/vance1852/drviercar/internal/service/fleet"
	sqlitestore "github.com/vance1852/drviercar/internal/storage/sqlite"
	"github.com/vance1852/drviercar/internal/worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "server exited: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	settings, err := config.Load()
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, settings.LogLevel)
	defer logger.Close()

	if directory := filepath.Dir(settings.DatabasePath); directory != "" && directory != "." {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("创建数据目录失败: %w", err)
		}
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	options := sqlitestore.DefaultOptions(settings.DatabasePath)
	options.MaxOpenConns = settings.MaxOpenConns
	store, err := sqlitestore.Open(rootCtx, options)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	timeSource := clock.System{}
	recorder := audit.NewRecorder(timeSource)
	idempotency := idem.NewManager(timeSource, 48*time.Hour)

	authService := auth.NewService(store, timeSource, recorder, auth.Config{SessionTTL: settings.SessionTTL})
	fleetService := fleet.NewService(fleet.Dependencies{
		Store:       store,
		Clock:       timeSource,
		Recorder:    recorder,
		Idempotency: idempotency,
		Logger:      logger,
	})
	dataService := dataloop.NewService(dataloop.Dependencies{
		Store:    store,
		Clock:    timeSource,
		Recorder: recorder,
		Logger:   logger,
	})

	if err := bootstrapAdmin(rootCtx, authService, settings, logger); err != nil {
		return err
	}

	dispatcher := worker.NewDispatcher(store, timeSource, logger, worker.Config{
		Interval:    settings.WorkerInterval,
		BatchSize:   settings.WorkerBatchSize,
		BaseBackoff: settings.WorkerBaseBackoff,
	})
	worker.NewMaintenance(store, timeSource, recorder).RegisterAll(dispatcher)
	dispatcher.Start(rootCtx)
	defer dispatcher.Stop()

	router := httpapi.NewRouter(httpapi.Dependencies{
		Auth:           authService,
		Fleet:          fleetService,
		DataLoop:       dataService,
		Store:          store,
		Logger:         logger,
		Clock:          timeSource,
		RequestTimeout: settings.RequestTimeout,
	})

	server := &http.Server{
		Addr:              settings.Addr,
		Handler:           router.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info(rootCtx, "http server listening", map[string]any{
			"addr":     settings.Addr,
			"database": settings.DatabasePath,
		})
		if listenErr := server.ListenAndServe(); listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			serverErrors <- listenErr
			return
		}
		serverErrors <- nil
	}()

	select {
	case listenErr := <-serverErrors:
		return listenErr
	case <-rootCtx.Done():
		logger.Info(context.Background(), "shutdown signal received", nil)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), settings.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("优雅关闭失败: %w", err)
	}
	dispatcher.Stop()
	return nil
}

// bootstrapAdmin creates the initial fleet administrator when the environment
// requests it and the account does not exist yet.
func bootstrapAdmin(
	ctx context.Context,
	authService *auth.Service,
	settings config.Config,
	logger *logging.Logger,
) error {
	if settings.BootstrapAdmin == "" {
		return nil
	}
	_, err := authService.Register(ctx, auth.RegisterInput{
		Username:    settings.BootstrapAdmin,
		DisplayName: "bootstrap fleet administrator",
		Password:    settings.BootstrapSecret,
		Role:        domain.RoleFleetAdmin,
	})
	if err == nil {
		logger.Info(ctx, "bootstrap administrator created", map[string]any{
			"username": settings.BootstrapAdmin,
		})
		return nil
	}
	logger.Info(ctx, "bootstrap administrator already present", map[string]any{
		"username": settings.BootstrapAdmin,
	})
	return nil
}
