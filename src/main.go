package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var logger *slog.Logger

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if b, err := strconv.ParseBool(value); err == nil {
			return b
		}
	}
	return defaultValue
}

func main() {
	// Read configuration from environment variables
	port := getEnv("LISTEN_PORT", "7777")
	serverID := getEnvInt("SERVER_ID", -1)
	serverFallback := getEnvBool("SERVER_FALLBACK", false)
	refreshInterval := getEnvInt("REFRESH_INTERVAL", 3600)

	// Configure log level from environment variable
	logLevel := slog.LevelInfo
	if level := os.Getenv("LOG_LEVEL"); level != "" {
		switch strings.ToUpper(level) {
		case "DEBUG":
			logLevel = slog.LevelDebug
		case "INFO":
			logLevel = slog.LevelInfo
		case "WARN", "WARNING":
			logLevel = slog.LevelWarn
		case "ERROR":
			logLevel = slog.LevelError
		}
	}

	logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))

	if refreshInterval <= 0 {
		logger.Error("refresh_interval must be greater than 0")
		os.Exit(1)
	}

	exporter, err := NewExporter(serverID, serverFallback)
	if err != nil {
		panic(err)
	}

	r := prometheus.NewRegistry()
	r.MustRegister(exporter)

	http.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		client := http.Client{
			Timeout: 3 * time.Second,
		}
		_, err := client.Get("https://clients3.google.com/generate_204")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, "No Internet Connection")
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "OK")
		}
	})

	http.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		if !exporter.Ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	http.HandleFunc("GET /metrics", func(w http.ResponseWriter, req *http.Request) {
		if !exporter.Ready() {
			http.Error(w, "metrics not ready", http.StatusServiceUnavailable)
			return
		}
		promhttp.HandlerFor(r, promhttp.HandlerOpts{}).ServeHTTP(w, req)
	})

	// Configure HTTP server with timeouts
	server := &http.Server{
		Addr:         ":" + port,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	logger.Info("Starting Speedtest Exporter", "port", port, "refresh_interval_seconds", refreshInterval)

	go func() {
		if err := server.ListenAndServe(); err != nil {
			logger.Error("HTTP server error", "error", err)
			os.Exit(1)
		}
	}()

	// Run speedtests on a schedule
	go func() {
		ctx := context.Background()

		// Initial refresh without blocking the HTTP server
		if err := exporter.Refresh(ctx); err != nil {
			logger.Error("Speedtest failed", "error", err)
		}

		ticker := time.NewTicker(time.Duration(refreshInterval) * time.Second)
		defer ticker.Stop()
		for {
			<-ticker.C
			if err := exporter.Refresh(ctx); err != nil {
				logger.Error("Speedtest failed", "error", err)
			}
		}
	}()

	// Wait for interrupt signal to gracefully shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	<-sigChan
	logger.Info("Shutting down gracefully")
}
