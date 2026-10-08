package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shu915/better-auth-multi-platform-api/internal/auth"
	"github.com/shu915/better-auth-multi-platform-api/internal/db"
	"github.com/shu915/better-auth-multi-platform-api/internal/middleware"
	"github.com/shu915/better-auth-multi-platform-api/internal/profile"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// newMux builds the routes. authn wraps the endpoints that require a signed-in user;
// those return personal data, so their responses (including 401s) are also marked no-store.
func newMux(authn func(http.Handler) http.Handler, profiles profileStore) *http.ServeMux {
	protect := func(h http.Handler) http.Handler { return middleware.NoStore(authn(h)) }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("GET /me", protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := middleware.UserID(r.Context())
		if !ok { // authn did not run: a wiring bug, never trust the request
			internalError(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"user_id": userID})
	})))
	mux.Handle("GET /me/profile", protect(getProfile(profiles)))
	mux.Handle("PUT /me/profile", protect(putProfile(profiles)))
	mux.Handle("DELETE /me", protect(deleteMe(profiles)))
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json response: %v", err)
	}
}

// config is everything read from the environment.
type config struct {
	Port        string
	Auth        auth.Config
	CORSOrigins []string
	DatabaseURL string
}

// loadConfig reads the environment. With APP_ENV=production the settings that
// identify the web app must be given explicitly instead of silently defaulting to localhost.
// Auth must match the web app's Better Auth config: Issuer = BETTER_AUTH_URL, Audience = JWT_AUDIENCE.
func loadConfig(env func(string) string) (config, error) {
	get := func(key, fallback string) string {
		if v := env(key); v != "" {
			return v
		}
		return fallback
	}
	appEnv := env("APP_ENV")
	switch appEnv {
	case "", "development", "production":
	default: // a typo such as "prod" must not silently disable the production checks
		return config{}, fmt.Errorf(`APP_ENV must be "development" or "production", got %q`, appEnv)
	}
	port := get("PORT", "8080")
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return config{}, fmt.Errorf("PORT must be a number from 1 to 65535, got %q", port)
	}
	if appEnv == "production" {
		for _, key := range []string{"AUTH_ISSUER", "AUTH_AUDIENCE", "CORS_ALLOWED_ORIGINS", "DATABASE_URL"} {
			if env(key) == "" {
				return config{}, fmt.Errorf("%s is required when APP_ENV=production", key)
			}
		}
	}
	corsOrigins := middleware.ParseOrigins(get("CORS_ALLOWED_ORIGINS", "http://localhost:3000"))
	databaseURL := get("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/app?sslmode=disable")
	if appEnv == "production" {
		if len(corsOrigins) == 0 { // e.g. "," passes the non-empty check above but allows no origin
			return config{}, errors.New("CORS_ALLOWED_ORIGINS must list at least one origin")
		}
		if err := db.RequireTLS(databaseURL); err != nil {
			return config{}, err
		}
	}
	issuer := get("AUTH_ISSUER", "http://localhost:3000")
	jwksURL := get("AUTH_JWKS_URL", issuer+"/api/auth/jwks")
	if appEnv == "production" && !strings.HasPrefix(jwksURL, "https://") {
		// The verifier allows plain http for localhost so development works; production must not.
		return config{}, errors.New("AUTH_JWKS_URL must be https when APP_ENV=production")
	}
	return config{
		Port: port,
		Auth: auth.Config{
			Issuer:   issuer,
			Audience: get("AUTH_AUDIENCE", "better-auth-multi-platform-api"),
			JWKSURL:  jwksURL,
		},
		CORSOrigins: corsOrigins,
		// The default is the dummy credentials of the dev DB in docker-compose.yml, reachable from a host-run server.
		DatabaseURL: databaseURL,
	}, nil
}

// handler wires the middleware chain: CORS outermost so preflights and 401s carry CORS headers.
func handler(verifier middleware.TokenVerifier, corsOrigins []string, profiles profileStore) http.Handler {
	// Recover is outermost so a panic anywhere below, CORS included, still becomes a 500.
	return middleware.Recover(middleware.CORS(corsOrigins)(
		middleware.Timeout(requestTimeout)(newMux(middleware.Authenticate(verifier), profiles)),
	))
}

// requestTimeout bounds one request, including its database queries and a JWKS refetch. It is
// shorter than the server's WriteTimeout (15s) so the handler can still answer.
const requestTimeout = 10 * time.Second

func run() error {
	// Fargate sends SIGTERM on task stop; drain in-flight requests before exiting.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	verifier, err := auth.NewVerifier(ctx, cfg.Auth)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler(verifier, cfg.CORSOrigins, profile.NewStore(pool)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("listening on :%s", cfg.Port)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		log.Print("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
