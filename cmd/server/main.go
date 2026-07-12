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

	"github.com/m600x/ai-subscription-gateway/internal/anthropic"
	"github.com/m600x/ai-subscription-gateway/internal/codex"
	"github.com/m600x/ai-subscription-gateway/internal/config"
	"github.com/m600x/ai-subscription-gateway/internal/provider"
	"github.com/m600x/ai-subscription-gateway/internal/registry"
	"github.com/m600x/ai-subscription-gateway/internal/server"
	"github.com/m600x/ai-subscription-gateway/internal/tokenstore"
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

	reg, src, err := registry.Resolve(cfg.ModelsInline, cfg.ModelsConfigPath)
	if src.Warning != nil {
		// MODELS env was set but rejected; we fell back to the file.
		slog.Error("MODELS env is invalid; falling back to the models file", "err", src.Warning, "path", cfg.ModelsConfigPath)
	}
	if err != nil {
		slog.Error("model registry error", "err", err)
		os.Exit(1)
	}
	slog.Info("model registry loaded", "source", src.Name, "models", reg.Len())

	// In non-stateless mode, persist tokens to disk so a restart resumes with
	// the latest (rotated) OpenAI refresh token. The persisted file takes
	// priority over the environment for that token (the env value goes obsolete
	// as the token rotates over the gateway's life); the env value is kept as a
	// fallback. Merging into cfg here means provider enablement below also
	// reflects a token that lives only in the file.
	var store *tokenstore.Store
	var openaiRefreshFallback string
	if !cfg.Stateless {
		store, err = tokenstore.Open(cfg.TokensFile)
		if err != nil {
			slog.Error("token file error", "path", cfg.TokensFile, "err", err)
			os.Exit(1)
		}
		res := store.Resolve(cfg.OAuthToken, cfg.OpenAIRefreshToken)
		if res.OpenAIRefreshToken != cfg.OpenAIRefreshToken && res.OpenAIRefreshToken != "" {
			slog.Info("using persisted OpenAI refresh token from file (env value ignored as potentially stale)", "path", cfg.TokensFile)
		}
		cfg.OAuthToken = res.AnthropicOAuthToken
		cfg.OpenAIRefreshToken = res.OpenAIRefreshToken
		openaiRefreshFallback = res.OpenAIRefreshFallback
		if err := store.Seed(cfg.OAuthToken, cfg.OpenAIRefreshToken); err != nil {
			slog.Error("failed to write token file", "path", cfg.TokensFile, "err", err)
			os.Exit(1)
		}
		slog.Info("stateless disabled; persisting tokens", "path", cfg.TokensFile)
	}

	enabled := cfg.EnabledProviders()
	providers := map[string]provider.Provider{}

	if cfg.AnthropicEnabled() {
		providers[registry.ProviderAnthropic] = anthropic.NewProvider(cfg)
	}
	if cfg.OpenAIEnabled() {
		cp := codex.NewProvider(cfg)
		if store != nil {
			// Primary is the (file-priority) token cfg now holds; fall back to
			// the env token for a deliberate re-login, and persist rotations.
			cp.UseRefreshTokens(cfg.OpenAIRefreshToken, openaiRefreshFallback)
			cp.SetPersist(store.SetOpenAIRefresh)
		}
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
	fmt.Printf("  OPENAI_TOKEN=%s\n", res.RefreshToken)
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
