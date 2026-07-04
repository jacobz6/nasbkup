// NAS Backup Backend - Main Entry Point
// This is the main entry point for the NAS backup backend service.
// It initializes all components, starts the HTTP server, and manages the application lifecycle.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nas-backup/internal/api"
	"github.com/nas-backup/internal/backup"
	"github.com/nas-backup/internal/compress"
	"github.com/nas-backup/internal/config"
	"github.com/nas-backup/internal/crypto"
	"github.com/nas-backup/internal/db"
	"github.com/nas-backup/internal/dedup"
	"github.com/nas-backup/internal/logger"
	"github.com/nas-backup/internal/scanner"
	"github.com/nas-backup/internal/scheduler"
	"github.com/nas-backup/internal/storage"
)

func main() {
	// Parse command-line flags
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize logger
	if err := logger.Init(cfg.Logging.Level, cfg.Logging.FilePath, cfg.Logging.MaxSize, cfg.Logging.MaxFiles); err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Close()

	logger.Info("Starting NAS Backup Backend service...")
	logger.Info("Configuration loaded from: %s", *configPath)

	// Ensure data directories exist
	if err := cfg.EnsureDataDirs(); err != nil {
		logger.Error("Failed to create data directories: %v", err)
		log.Fatalf("Failed to create data directories: %v", err)
	}

	// Open database
	database, err := db.Open(cfg.Database.Path)
	if err != nil {
		logger.Error("Failed to open database: %v", err)
		log.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()
	logger.Info("Database opened at: %s", cfg.Database.Path)

	// Clean up stale "running"/"pending" backup records left over from a
	// previous crash, so the new instance can start backups immediately.
	if cleaned, err := database.BackupRepo.CleanupStaleRunning(); err != nil {
		logger.Error("Failed to cleanup stale running backups: %v", err)
	} else if cleaned > 0 {
		logger.Info("Cleaned up %d stale running/pending backup record(s)", cleaned)
	}

	// Initialize components
	sc := scanner.NewScanner(database.FileRepo, database.ConfigRepo)
	dd := dedup.NewDeduplicator(database.HashRepo)
	comp := compress.NewCompressor(cfg.ToModelsCompressionConfig())
	enc, err := crypto.NewEncryptor(cfg.Backup.Encryption.KeyFilePath)
	if err != nil {
		logger.Error("Failed to initialize encryptor: %v", err)
		log.Fatalf("Failed to initialize encryptor: %v", err)
	}
	stor, err := storage.NewStorageManager(cfg)
	if err != nil {
		logger.Error("Failed to initialize storage manager: %v", err)
		log.Fatalf("Failed to initialize storage manager: %v", err)
	}

	// Ensure rclone config exists and has password for the crypt remote.
	if err := stor.EnsureRcloneConfig(); err != nil {
		logger.Error("Failed to ensure rclone config: %v", err)
		log.Fatalf("Failed to ensure rclone config: %v", err)
	}

	// Initialize backup engine and restorer
	engine := backup.NewEngine(database, sc, dd, comp, enc, stor, cfg)
	restorer := backup.NewRestorer(database, enc, comp, stor, cfg)

	// Initialize scheduler
	sched := scheduler.NewScheduler(engine, database, cfg)
	if cfg.Backup.Schedule.Enabled {
		if err := sched.Start(); err != nil {
			logger.Error("Failed to start scheduler: %v", err)
			log.Fatalf("Failed to start scheduler: %v", err)
		}
		defer sched.Stop()
		logger.Info("Scheduler started with cron expression: %s", cfg.Backup.Schedule.CronExpr)
	}

	// Initialize router and setup HTTP handler
	router := api.NewRouter(engine, restorer, sched, database, cfg)
	handler := router.Setup()

	// Create HTTP server
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  time.Duration(cfg.Server.ReadTimeout) * time.Second,
		WriteTimeout: time.Duration(cfg.Server.WriteTimeout) * time.Second,
	}

	// Start server in goroutine
	go func() {
		logger.Info("HTTP server listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error: %v", err)
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("Shutting down server...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("Server forced to shutdown: %v", err)
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	logger.Info("Server exited gracefully")
}
