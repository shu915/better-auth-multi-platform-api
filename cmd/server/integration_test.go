package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/shu915/better-auth-multi-platform-api/internal/auth"
	"github.com/shu915/better-auth-multi-platform-api/internal/profile"
	"github.com/shu915/better-auth-multi-platform-api/internal/testdb"
)

// These go through the whole chain with nothing faked on our side: the HTTP handlers, the real
// JWT verifier (against a JWKS server that serves the test key), the real profile store and a
// real Postgres. Only the web app's token issuing is played by signing with a test key.

const (
	itIssuer   = "http://web.test"
	itAudience = "api.test"
)

type itKey struct{ private jwk.Key }

func newITKey(t *testing.T) itKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := jwk.Import(priv)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{jwk.KeyIDKey: "it-key", jwk.AlgorithmKey: jwa.EdDSA()} {
		if err := key.Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	return itKey{private: key}
}

func (k itKey) sign(t *testing.T, subject, audience string, expires time.Time) string {
	t.Helper()
	b := jwt.NewBuilder().Issuer(itIssuer).Audience([]string{audience}).IssuedAt(time.Now()).Expiration(expires)
	if subject != "" {
		b = b.Subject(subject)
	}
	tok, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), k.private))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

type itEnv struct {
	server *httptest.Server
	key    itKey
	store  *profile.Store
}

func newITEnv(t *testing.T) itEnv {
	t.Helper()
	pool := testdb.Pool(t)
	key := newITKey(t)

	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pub, err := jwk.PublicKeyOf(key.private)
		if err != nil {
			t.Error(err)
			return
		}
		set := jwk.NewSet()
		if err := set.AddKey(pub); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(jwks.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	verifier, err := auth.NewVerifier(ctx, auth.Config{JWKSURL: jwks.URL, Issuer: itIssuer, Audience: itAudience})
	if err != nil {
		t.Fatal(err)
	}
	store := profile.NewStore(pool)
	srv := httptest.NewServer(handler(verifier, testOrigins, store))
	t.Cleanup(srv.Close)
	return itEnv{server: srv, key: key, store: store}
}

// newUser returns a fresh user id and a valid token for it. The row is removed when the test ends.
func (e itEnv) newUser(t *testing.T) (id, token string) {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	id = "it-user-" + hex.EncodeToString(b)
	t.Cleanup(func() { _ = e.store.Delete(context.Background(), id) })
	return id, e.key.sign(t, id, itAudience, time.Now().Add(5*time.Minute))
}

func (e itEnv) do(t *testing.T, method, path, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, e.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return res.StatusCode, sb.String()
}

func (e itEnv) bioOf(t *testing.T, token string) string {
	t.Helper()
	status, body := e.do(t, http.MethodGet, "/me/profile", token, "")
	if status != http.StatusOK {
		t.Fatalf("GET /me/profile: status = %d, body = %s", status, body)
	}
	var p struct {
		Bio string `json:"bio"`
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	return p.Bio
}

func TestProfileLifecycleThroughRealJWTAndDB(t *testing.T) {
	e := newITEnv(t)
	id, token := e.newUser(t)

	if got := e.bioOf(t, token); got != "" {
		t.Fatalf("new user: bio = %q, want empty", got)
	}
	if status, body := e.do(t, http.MethodPut, "/me/profile", token, `{"bio":"hello"}`); status != http.StatusOK || !strings.Contains(body, id) {
		t.Fatalf("PUT: status = %d, body = %s", status, body)
	}
	if got := e.bioOf(t, token); got != "hello" {
		t.Fatalf("after PUT: bio = %q, want hello", got)
	}
	e.do(t, http.MethodPut, "/me/profile", token, `{"bio":"あ🙂"}`)
	if got := e.bioOf(t, token); got != "あ🙂" {
		t.Fatalf("after replacing: bio = %q", got)
	}

	if status, _ := e.do(t, http.MethodDelete, "/me", token, ""); status != http.StatusNoContent {
		t.Fatalf("DELETE /me: status = %d, want 204", status)
	}
	if _, err := e.store.Get(context.Background(), id); err == nil {
		t.Fatal("the profiles row is still in the database after DELETE /me")
	}
	if got := e.bioOf(t, token); got != "" {
		t.Fatalf("after DELETE: bio = %q, want empty", got)
	}
	if status, _ := e.do(t, http.MethodDelete, "/me", token, ""); status != http.StatusNoContent {
		t.Fatalf("second DELETE /me: status = %d, want 204 (idempotent)", status)
	}
}

func TestDeletingOneUserLeavesOthersAlone(t *testing.T) {
	e := newITEnv(t)
	_, alice := e.newUser(t)
	_, bob := e.newUser(t)
	e.do(t, http.MethodPut, "/me/profile", alice, `{"bio":"alice"}`)
	e.do(t, http.MethodPut, "/me/profile", bob, `{"bio":"bob"}`)

	e.do(t, http.MethodDelete, "/me", alice, "")

	if got := e.bioOf(t, bob); got != "bob" {
		t.Fatalf("bob's bio = %q after alice deleted her data, want bob", got)
	}
	if got := e.bioOf(t, alice); got != "" {
		t.Fatalf("alice's bio = %q after deleting, want empty", got)
	}
}

func TestUserIDComesOnlyFromTheToken(t *testing.T) {
	e := newITEnv(t)
	_, alice := e.newUser(t)
	bobID, bob := e.newUser(t)
	e.do(t, http.MethodPut, "/me/profile", bob, `{"bio":"bob's secret"}`)

	// A query parameter or a body field naming another user must not reach their data.
	status, body := e.do(t, http.MethodGet, "/me/profile?user_id="+bobID, alice, "")
	if status != http.StatusOK || strings.Contains(body, "secret") || strings.Contains(body, bobID) {
		t.Fatalf("GET with ?user_id: status = %d, body = %s", status, body)
	}
	if status, _ := e.do(t, http.MethodPut, "/me/profile", alice, `{"bio":"x","user_id":"`+bobID+`"}`); status != http.StatusBadRequest {
		t.Fatalf("PUT with a user_id field: status = %d, want 400", status)
	}
	if status, _ := e.do(t, http.MethodDelete, "/me?user_id="+bobID, alice, ""); status != http.StatusNoContent {
		t.Fatalf("DELETE with ?user_id: status = %d", status)
	}
	if got := e.bioOf(t, bob); got != "bob's secret" {
		t.Fatalf("bob's bio = %q, want it untouched", got)
	}
}

func TestBadTokensChangeNothing(t *testing.T) {
	e := newITEnv(t)
	id, good := e.newUser(t)
	e.do(t, http.MethodPut, "/me/profile", good, `{"bio":"keep"}`)

	other := newITKey(t) // signed by a key the JWKS does not serve
	tokens := map[string]string{
		"none":           "",
		"garbage":        "not-a-jwt",
		"wrong key":      other.sign(t, id, itAudience, time.Now().Add(time.Minute)),
		"wrong audience": e.key.sign(t, id, "someone-else", time.Now().Add(time.Minute)),
		"expired":        e.key.sign(t, id, itAudience, time.Now().Add(-time.Minute)),
		"no subject":     e.key.sign(t, "", itAudience, time.Now().Add(time.Minute)),
	}
	for name, token := range tokens {
		for _, req := range []struct{ method, path, body string }{
			{http.MethodGet, "/me/profile", ""},
			{http.MethodPut, "/me/profile", `{"bio":"changed"}`},
			{http.MethodDelete, "/me", ""},
		} {
			if status, _ := e.do(t, req.method, req.path, token, req.body); status != http.StatusUnauthorized {
				t.Errorf("%s: %s %s: status = %d, want 401", name, req.method, req.path, status)
			}
		}
	}
	if got := e.bioOf(t, good); got != "keep" {
		t.Fatalf("bio = %q after rejected requests, want keep", got)
	}
}

func TestRealStoreEnforcesTheBioRules(t *testing.T) {
	e := newITEnv(t)
	_, token := e.newUser(t)

	max := strings.Repeat("あ", profile.MaxBioLength) // 1000 characters, 3000 bytes
	if status, body := e.do(t, http.MethodPut, "/me/profile", token, `{"bio":"`+max+`"}`); status != http.StatusOK {
		t.Fatalf("1000 characters: status = %d, body = %s", status, body)
	}
	if got := e.bioOf(t, token); got != max {
		t.Fatal("the 1000-character bio did not round-trip through the database")
	}

	for name, body := range map[string]string{
		"1001 characters": `{"bio":"` + max + `a"}`,
		"NUL":             `{"bio":"a\u0000b"}`,
	} {
		if status, _ := e.do(t, http.MethodPut, "/me/profile", token, body); status != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422", name, status)
		}
	}
	if got := e.bioOf(t, token); got != max {
		t.Fatal("a rejected bio changed what is stored")
	}
}
