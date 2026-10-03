package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

const (
	testIssuer   = "http://web.test"
	testAudience = "api.test"
)

type keyPair struct{ private jwk.Key }

func newKeyPair(t *testing.T, kid string) keyPair {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := jwk.Import(priv)
	if err != nil {
		t.Fatal(err)
	}
	must(t, key.Set(jwk.KeyIDKey, kid))
	must(t, key.Set(jwk.AlgorithmKey, jwa.EdDSA()))
	return keyPair{private: key}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// jwksServer serves the public halves of whatever keys currently() returns.
func jwksServer(t *testing.T, currently func() []keyPair) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		set := jwk.NewSet()
		for _, kp := range currently() {
			pub, err := jwk.PublicKeyOf(kp.private)
			if err != nil {
				t.Error(err)
				return
			}
			must(t, set.AddKey(pub))
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(set); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type claims struct {
	issuer, audience, subject string
	expires                   time.Time
}

func goodClaims() claims {
	return claims{testIssuer, testAudience, "user-1", time.Now().Add(5 * time.Minute)}
}

func sign(t *testing.T, kp keyPair, c claims) string {
	t.Helper()
	b := jwt.NewBuilder().Issuer(c.issuer).Audience([]string{c.audience}).IssuedAt(time.Now()).Expiration(c.expires)
	if c.subject != "" {
		b = b.Subject(c.subject)
	}
	tok, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), kp.private))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

func signHS256(t *testing.T, c claims) string {
	t.Helper()
	tok, err := jwt.NewBuilder().Issuer(c.issuer).Audience([]string{c.audience}).Subject(c.subject).Expiration(c.expires).Build()
	if err != nil {
		t.Fatal(err)
	}
	key, err := jwk.Import([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	must(t, key.Set(jwk.KeyIDKey, "k1"))
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.HS256(), key))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

func newTestVerifier(t *testing.T, url string) *Verifier {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	v, err := NewVerifier(ctx, Config{JWKSURL: url, Issuer: testIssuer, Audience: testAudience})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVerify(t *testing.T) {
	kp := newKeyPair(t, "k1")
	other := newKeyPair(t, "k1") // same kid, different key: signature must not verify
	srv := jwksServer(t, func() []keyPair { return []keyPair{kp} })
	v := newTestVerifier(t, srv.URL)

	expired := goodClaims()
	expired.expires = time.Now().Add(-time.Hour)
	wrongAud := goodClaims()
	wrongAud.audience = "someone-else"
	wrongIss := goodClaims()
	wrongIss.issuer = "http://evil.test"
	noSub := goodClaims()
	noSub.subject = ""

	tests := []struct {
		name    string
		token   string
		wantSub string
		wantErr bool
	}{
		{"valid", sign(t, kp, goodClaims()), "user-1", false},
		{"expired", sign(t, kp, expired), "", true},
		{"wrong audience", sign(t, kp, wrongAud), "", true},
		{"wrong issuer", sign(t, kp, wrongIss), "", true},
		{"missing subject", sign(t, kp, noSub), "", true},
		{"wrong signing key", sign(t, other, goodClaims()), "", true},
		{"garbage", "not-a-jwt", "", true},
		{"HS256 signed (algorithm confusion)", signHS256(t, goodClaims()), "", true},
		{"alg none", "eyJhbGciOiJub25lIiwia2lkIjoiazEifQ.eyJzdWIiOiJ1c2VyLTEifQ.", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub, err := v.Verify(context.Background(), tt.token)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if sub != tt.wantSub {
				t.Errorf("sub = %q, want %q", sub, tt.wantSub)
			}
		})
	}
}

func TestVerifyRefetchesAfterKeyRotation(t *testing.T) {
	old := newKeyPair(t, "old")
	rotated := newKeyPair(t, "new")
	var mu sync.Mutex // the JWKS server reads current from another goroutine
	current := []keyPair{old}
	srv := jwksServer(t, func() []keyPair { mu.Lock(); defer mu.Unlock(); return current })
	v := newTestVerifier(t, srv.URL)

	if _, err := v.Verify(context.Background(), sign(t, old, goodClaims())); err != nil {
		t.Fatalf("old key: %v", err)
	}

	mu.Lock()
	current = []keyPair{old, rotated}
	mu.Unlock()
	if _, err := v.Verify(context.Background(), sign(t, rotated, goodClaims())); err != nil {
		t.Fatalf("rotated key should be picked up by a refetch: %v", err)
	}
}

func TestNewVerifierRequiresConfig(t *testing.T) {
	if _, err := NewVerifier(context.Background(), Config{}); err == nil {
		t.Fatal("expected an error for empty config")
	}
}

func TestForgedKidsDoNotHammerJWKS(t *testing.T) {
	kp := newKeyPair(t, "k1")
	var hits atomic.Int32
	inner := jwksServer(t, func() []keyPair { return []keyPair{kp} })
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		inner.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(counting.Close)
	v := newTestVerifier(t, counting.URL)

	if _, err := v.Verify(context.Background(), sign(t, kp, goodClaims())); err != nil {
		t.Fatal(err)
	}
	before := hits.Load()
	for i := 0; i < 20; i++ {
		forged := newKeyPair(t, fmt.Sprintf("forged-%d", i))
		if _, err := v.Verify(context.Background(), sign(t, forged, goodClaims())); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("forged kid %d: err = %v, want ErrInvalidToken", i, err)
		}
	}
	// 20 forged kids may cause at most one forced refetch within the cooldown.
	if got := hits.Load() - before; got > 1 {
		t.Fatalf("JWKS fetched %d extra times for 20 forged kids, want <= 1", got)
	}
}

func TestUnreachableJWKSIsNotAnInvalidToken(t *testing.T) {
	kp := newKeyPair(t, "k1")
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing is listening any more
	v := newTestVerifier(t, url)

	_, err := v.Verify(context.Background(), sign(t, kp, goodClaims()))
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrInvalidToken) {
		t.Fatalf("an unreachable JWKS must not look like a bad token: %v", err)
	}
}

func TestJWKSURLMustBeHTTPSExceptLocal(t *testing.T) {
	ok := []string{"https://web.example.com/api/auth/jwks", "http://localhost:3000/x", "http://127.0.0.1:3000/x", "http://host.docker.internal:3000/x"}
	bad := []string{"http://web.example.com/api/auth/jwks", "ftp://x/y", "not a url", "https:///nohost"}
	for _, u := range ok {
		if err := checkJWKSURL(u); err != nil {
			t.Errorf("%q rejected: %v", u, err)
		}
	}
	for _, u := range bad {
		if err := checkJWKSURL(u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

func verifyConcurrently(t *testing.T, v *Verifier, token string, n int) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := v.Verify(context.Background(), token); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent verify failed: %v", err)
	}
}

func TestBurstOnColdStartAllSucceed(t *testing.T) {
	kp := newKeyPair(t, "k1")
	srv := jwksServer(t, func() []keyPair { return []keyPair{kp} })
	v := newTestVerifier(t, srv.URL)
	verifyConcurrently(t, v, sign(t, kp, goodClaims()), 20)
}

func TestBurstAfterKeyRotationAllSucceed(t *testing.T) {
	old := newKeyPair(t, "old")
	rotated := newKeyPair(t, "new")
	var mu sync.Mutex
	current := []keyPair{old}
	srv := jwksServer(t, func() []keyPair { mu.Lock(); defer mu.Unlock(); return current })
	v := newTestVerifier(t, srv.URL)

	if _, err := v.Verify(context.Background(), sign(t, old, goodClaims())); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	current = []keyPair{old, rotated}
	mu.Unlock()
	verifyConcurrently(t, v, sign(t, rotated, goodClaims()), 20)
}
