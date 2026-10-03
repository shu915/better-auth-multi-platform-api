// Package auth verifies JWTs issued by the web app (Better Auth).
// It does not depend on net/http handlers, so it survives a router change.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

// ErrInvalidToken is returned for any token that must not be trusted.
// Any other error from Verify means the check could not be completed
// (for example the JWKS is unreachable); callers should treat that as a 5xx, not a 401.
var ErrInvalidToken = errors.New("invalid token")

const (
	clockSkew = 30 * time.Second
	// An unknown kid triggers at most one forced JWKS refetch per this interval,
	// so a flood of forged kids cannot make us hammer the web app.
	refetchCooldown = 15 * time.Second
	// While the first JWKS fetch has not succeeded yet, retry at most this often.
	coldRetryCooldown = 5 * time.Second

	// Upper bound for one JWKS fetch, so a hung web app cannot pile up requests.
	fetchTimeout = 3 * time.Second
)

type Config struct {
	JWKSURL  string
	Issuer   string
	Audience string
}

type Verifier struct {
	cfg   Config
	cache *jwk.Cache

	group         singleflight.Group // concurrent callers share one JWKS fetch
	mu            sync.Mutex
	lastKidMiss   time.Time // last forced refetch caused by an unknown kid
	lastColdFetch time.Time // last forced fetch while the cache was empty
}

// NewVerifier starts a background JWKS cache. The JWKS is fetched lazily, so the
// API can start even if the web app is not reachable yet. ctx bounds the cache's lifetime.
func NewVerifier(ctx context.Context, cfg Config) (*Verifier, error) {
	if cfg.JWKSURL == "" || cfg.Issuer == "" || cfg.Audience == "" {
		return nil, errors.New("auth: JWKSURL, Issuer and Audience are required")
	}
	if err := checkJWKSURL(cfg.JWKSURL); err != nil {
		return nil, err
	}
	cache, err := jwk.NewCache(ctx, httprc.NewClient())
	if err != nil {
		return nil, fmt.Errorf("auth: create jwks cache: %w", err)
	}
	httpClient := &http.Client{
		Timeout: fetchTimeout,
		// The JWKS URL is fixed configuration; never follow redirects elsewhere.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	err = cache.Register(ctx, cfg.JWKSURL,
		jwk.WithHTTPClient(httpClient),
		jwk.WithWaitReady(false),
		jwk.WithMinInterval(time.Minute),
		jwk.WithMaxInterval(time.Hour),
	)
	if err != nil {
		return nil, fmt.Errorf("auth: register jwks url: %w", err)
	}
	return &Verifier{cfg: cfg, cache: cache}, nil
}

// checkJWKSURL refuses plain http except for local development hosts, because
// a JWKS fetched in the clear could be swapped by a man in the middle.
func checkJWKSURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("auth: invalid JWKS URL %q", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1", "host.docker.internal":
			return nil
		}
	}
	return fmt.Errorf("auth: JWKS URL must be https (http is allowed only for localhost): %q", raw)
}

// Verify checks the signature, issuer, audience and expiry, and returns the user id (sub).
func (v *Verifier) Verify(ctx context.Context, token string) (string, error) {
	msg, err := jws.Parse([]byte(token))
	if err != nil || len(msg.Signatures()) != 1 {
		return "", ErrInvalidToken
	}
	hdr := msg.Signatures()[0].ProtectedHeaders()

	// Allow-list the algorithm; never trust the token to pick it (blocks "none" and HS/RS confusion).
	if alg, ok := hdr.Algorithm(); !ok || alg != jwa.EdDSA() {
		return "", ErrInvalidToken
	}
	kid, ok := hdr.KeyID()
	if !ok || kid == "" {
		return "", ErrInvalidToken
	}

	set, err := v.keySet(ctx, kid)
	if err != nil {
		return "", fmt.Errorf("auth: load jwks: %w", err)
	}

	tok, err := jwt.Parse([]byte(token),
		jwt.WithKeySet(set),
		jwt.WithValidate(true),
		jwt.WithIssuer(v.cfg.Issuer),
		jwt.WithAudience(v.cfg.Audience),
		jwt.WithAcceptableSkew(clockSkew),
	)
	if err != nil {
		return "", ErrInvalidToken
	}

	sub, ok := tok.Subject()
	if !ok || sub == "" {
		return "", ErrInvalidToken
	}
	if _, ok := tok.Expiration(); !ok {
		return "", ErrInvalidToken
	}
	return sub, nil
}

// keySet returns the cached JWKS. If the cache is still empty, or kid is unknown
// (the web app rotated its signing key), it forces a refetch, rate-limited so
// forged kids or an unreachable web app cannot make every request hit the network.
//
// Callers arriving while a fetch is in flight wait for it and share its result, so a
// burst right after startup or a key rotation does not fail while the first fetch runs.
// The mutex only guards the rate-limit bookkeeping and is never held across the network call.
func (v *Verifier) keySet(ctx context.Context, kid string) (jwk.Set, error) {
	set, lookupErr := v.cache.Lookup(ctx, v.cfg.JWKSURL)
	if lookupErr == nil {
		if _, found := set.LookupKeyID(kid); found {
			return set, nil
		}
	}

	res, err, _ := v.group.Do("jwks", func() (any, error) {
		return v.refetch(ctx, set, lookupErr)
	})
	if err != nil {
		return nil, err
	}
	return res.(jwk.Set), nil
}

// refetch runs once per burst. stale/lookupErr describe the cache state the caller saw.
func (v *Verifier) refetch(ctx context.Context, stale jwk.Set, lookupErr error) (jwk.Set, error) {
	v.mu.Lock()
	last, cooldown := &v.lastKidMiss, refetchCooldown
	if lookupErr != nil {
		last, cooldown = &v.lastColdFetch, coldRetryCooldown
	}
	if time.Since(*last) < cooldown {
		v.mu.Unlock()
		// Refetched very recently. Someone may have refreshed the cache since our lookup,
		// so read it again instead of judging the token against a stale copy.
		if fresh, err := v.cache.Lookup(ctx, v.cfg.JWKSURL); err == nil {
			return fresh, nil
		}
		return stale, lookupErr
	}
	*last = time.Now()
	v.mu.Unlock()

	// Detach from the leader's request: one client hanging up must not fail everyone waiting.
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
	defer cancel()
	fresh, err := v.cache.Refresh(fetchCtx, v.cfg.JWKSURL)
	if err != nil {
		if lookupErr == nil {
			// Keep serving the old keys; an unknown kid then fails as an invalid token.
			return stale, nil
		}
		return nil, err
	}
	return fresh, nil
}
