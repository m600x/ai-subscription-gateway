// Command server runs the OpenAI-compatible HTTP wrapper backed by one or both
// subscription backends (Anthropic Claude, OpenAI Codex). A provider is served
// only when its credentials are configured.
//
// Subcommands:
//
//	server            run the HTTP server (default)
//	server serve      run the HTTP server
//	server login      run the OpenAI (ChatGPT) OAuth login and print a refresh token
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/m600x/ai-substation/internal/anthropic"
	"github.com/m600x/ai-substation/internal/codex"
	"github.com/m600x/ai-substation/internal/config"
	"github.com/m600x/ai-substation/internal/provider"
	"github.com/m600x/ai-substation/internal/registry"
	"github.com/m600x/ai-substation/internal/server"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "login":
		runLogin()
	case "serve", "server":
		runServe()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (expected: serve | login)\n", cmd)
		os.Exit(2)
	}
}

func runServe() {
	cfg, err := config.Load()
	if err != nil {
		// Logging not configured yet; use the default logger.
		slog.Error("configuration error", "err", err)
		os.Exit(1)
	}
	setupLogging(cfg.LogLevel)

	reg, err := registry.Load(cfg.ModelsConfigPath)
	if err != nil {
		slog.Error("model registry error", "err", err)
		os.Exit(1)
	}

	enabled := cfg.EnabledProviders()
	providers := map[string]provider.Provider{}

	if cfg.AnthropicEnabled() {
		providers[registry.ProviderAnthropic] = anthropic.NewProvider(cfg)
	}
	if cfg.OpenAIEnabled() {
		cp := codex.NewProvider(cfg)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := cp.Prime(ctx); err != nil {
			cancel()
			slog.Error("openai credentials error", "err", err)
			os.Exit(1)
		}
		cancel()
		providers[registry.ProviderOpenAI] = cp
	}

	// Resolve the default model if unset: first registry model whose provider
	// is enabled.
	if cfg.DefaultModel == "" {
		if m, ok := reg.First(enabled); ok {
			cfg.DefaultModel = m.ID
		}
	}

	srv := server.New(cfg, reg, providers, enabled)

	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		names := make([]string, 0, len(providers))
		for name := range providers {
			names = append(names, name)
		}
		slog.Info("listening", "addr", httpServer.Addr, "providers", names, "default_model", cfg.DefaultModel)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	slog.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		slog.Error("shutdown error", "err", err)
	}
}

func runLogin() {
	// Login only needs the OpenAI OAuth settings; build a minimal config from
	// the environment defaults without requiring the serve-time secrets.
	cfg := &config.Config{
		OpenAIAuthIssuer: envOr("OPENAI_AUTH_ISSUER", "https://auth.openai.com"),
		OpenAIClientID:   envOr("OPENAI_CLIENT_ID", config.OpenAIDefaultClientID),
		OpenAIOriginator: envOr("OPENAI_ORIGINATOR", "codex_cli_rs"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	res, err := codex.Login(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "login failed:", err)
		os.Exit(1)
	}
	if res.RefreshToken == "" {
		fmt.Fprintln(os.Stderr, "login succeeded but no refresh token was returned")
		os.Exit(1)
	}
	fmt.Print("\nLogin successful. Set this in your environment to serve OpenAI/Codex models:\n\n")
	fmt.Printf("  OPENAI_REFRESH_TOKEN=%s\n", res.RefreshToken)
	if res.AccountID != "" {
		fmt.Printf("\n(account id: %s)\n", res.AccountID)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func setupLogging(level string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})))
}
