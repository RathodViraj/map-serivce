package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mapservice"
	"mapservice/db"
)

func main() {
	loadDotEnv()
	config := loadConfig()

	redisClient, redisErr := db.NewClient(db.Config{
		Addr:        config.RedisAddr,
		Password:    config.RedisPassword,
		DB:          config.RedisDB,
		DialTimeout: config.RedisDialTimeout,
	})
	var cache mapservice.Cache
	if redisErr != nil {
		log.Printf(`{"level":"warn","message":"redis cache disabled","error":%q}`, redisErr.Error())
	} else {
		cache = redisClient
		defer func() {
			_ = redisClient.Close()
		}()
	}

	httpClient := &http.Client{Timeout: config.HTTPTimeout}
	repository := mapservice.NewRepository(httpClient, config.OverpassURL, config.RetryCount, config.RetryDelay)
	service := mapservice.NewService(repository, cache, config.CacheTTL, config.RequestTimeout)
	handler := mapservice.NewHandler(service)

	mux := http.NewServeMux()
	mux.Handle("/nearby", handler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, mapservice.APIResponse{Success: true, Message: "ok"})
	})
	mux.Handle("/", frontendHandler())

	wrapped := mapservice.CORSMiddleware(config.CORSOrigin)(mapservice.LoggingMiddleware(mux))
	server := &http.Server{
		Addr:              config.ListenAddr,
		Handler:           wrapped,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf(`{"level":"info","message":"server starting","addr":%q}`, config.ListenAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf(`{"level":"error","message":"server failed","error":%q}`, err.Error())
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf(`{"level":"error","message":"shutdown failed","error":%q}`, err.Error())
	}
}

func loadConfig() mapservice.Config {
	return mapservice.Config{
		ListenAddr:       normalizeListenAddr(getEnv("PORT", mapservice.DefaultListenAddr)),
		OverpassURL:      getEnv("OVERPASS_URL", mapservice.DefaultOverpassURL),
		RequestTimeout:   getDurationEnv("REQUEST_TIMEOUT", mapservice.DefaultRequestTimeout),
		HTTPTimeout:      getDurationEnv("HTTP_TIMEOUT", mapservice.DefaultHTTPTimeout),
		CacheTTL:         getDurationEnv("CACHE_TTL", mapservice.DefaultCacheTTL),
		RetryCount:       getIntEnv("RETRY_COUNT", mapservice.DefaultRetryCount),
		RetryDelay:       getDurationEnv("RETRY_DELAY", mapservice.DefaultRetryDelay),
		RedisAddr:        getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:    os.Getenv("REDIS_PASSWORD"),
		RedisDB:          getIntEnv("REDIS_DB", 0),
		RedisDialTimeout: getDurationEnv("REDIS_DIAL_TIMEOUT", 5*time.Second),
		CORSOrigin:       getEnv("CORS_ORIGIN", mapservice.DefaultCORSOrigin),
	}
}

func loadDotEnv() {
	for _, candidate := range []string{".env", filepath.Join("..", ".env")} {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "export ")
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			value = strings.Trim(value, `"`)
			value = strings.Trim(value, "'")
			if key != "" {
				_ = os.Setenv(key, value)
			}
		}
		return
	}
}

func normalizeListenAddr(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return mapservice.DefaultListenAddr
	}
	if !strings.Contains(value, ":") {
		return ":" + value
	}
	return value
}

func getEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func getIntEnv(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getDurationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf(`{"level":"error","message":"json encode failed","error":%q}`, err.Error())
	}
}

func frontendHandler() http.Handler {
	for _, candidate := range []string{"frontend", filepath.Join("..", "frontend")} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return http.FileServer(http.Dir(candidate))
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, mapservice.APIResponse{
			Success: false,
			Error:   &mapservice.APIError{Code: "frontend_not_found", Message: "frontend directory is missing"},
		})
	})
}
