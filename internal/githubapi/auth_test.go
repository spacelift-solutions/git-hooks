package githubapi

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResolveTokenPrefersEnvironmentTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "GH_TOKEN has priority",
			env: map[string]string{
				githubTokenEnv:        "gh-token",
				githubActionsTokenEnv: "actions-token",
			},
			want: "gh-token",
		},
		{
			name: "GITHUB_TOKEN is the fallback",
			env: map[string]string{
				githubActionsTokenEnv: "actions-token",
			},
			want: "actions-token",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			token, err := ResolveToken(context.Background(), AuthOptions{
				Getenv: mapGetenv(test.env),
				ReadFile: func(string) ([]byte, error) {
					t.Fatal("private key should not be read when a token is set")
					return nil, nil
				},
			})
			if err != nil {
				t.Fatalf("ResolveToken() error = %v", err)
			}
			if token != test.want {
				t.Fatalf("ResolveToken() = %q, want %q", token, test.want)
			}
		})
	}
}

func TestResolveTokenRequiresCompleteAuthentication(t *testing.T) {
	t.Parallel()

	_, err := ResolveToken(context.Background(), AuthOptions{
		Getenv: mapGetenv(map[string]string{
			appIDEnv: "123",
		}),
	})
	if err == nil {
		t.Fatal("ResolveToken() unexpectedly succeeded")
	}
	for _, name := range []string{
		githubTokenEnv,
		githubActionsTokenEnv,
		appIDEnv,
		installationIDEnv,
		appPEMPathEnv,
	} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not mention %s", err, name)
		}
	}
}

func TestResolveTokenMintsGitHubAppInstallationToken(t *testing.T) {
	t.Parallel()

	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})
	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", request.Method)
		}
		if request.URL.Path != "/app/installations/456/access_tokens" {
			t.Errorf("path = %s, want installation token path", request.URL.Path)
		}
		if request.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("Accept = %q", request.Header.Get("Accept"))
		}

		authorization := request.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(authorization, prefix) {
			t.Fatalf("Authorization = %q, want Bearer JWT", authorization)
		}
		assertAppJWT(t, strings.TrimPrefix(authorization, prefix), privateKey.PublicKey, now)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"token":"installation-token"}`))
	}))
	defer server.Close()

	token, err := ResolveToken(context.Background(), AuthOptions{
		APIURL:     server.URL,
		HTTPClient: server.Client(),
		Getenv: mapGetenv(map[string]string{
			appIDEnv:          "123",
			installationIDEnv: "456",
			appPEMPathEnv:     "/secret/app.pem",
		}),
		ReadFile: func(path string) ([]byte, error) {
			if path != "/secret/app.pem" {
				t.Fatalf("private key path = %q", path)
			}
			return privateKeyPEM, nil
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("ResolveToken() error = %v", err)
	}
	if token != "installation-token" {
		t.Fatalf("ResolveToken() = %q", token)
	}
}

func TestParsePrivateKeyAcceptsPKCS8(t *testing.T) {
	t.Parallel()

	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	parsed, err := parsePrivateKey(pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: encoded,
	}))
	if err != nil {
		t.Fatalf("parsePrivateKey() error = %v", err)
	}
	if parsed.PublicKey.N.Cmp(privateKey.PublicKey.N) != 0 {
		t.Fatal("parsed key does not match input")
	}
}

func assertAppJWT(
	t *testing.T,
	token string,
	publicKey rsa.PublicKey,
	now time.Time,
) {
	t.Helper()

	segments := strings.Split(token, ".")
	if len(segments) != 3 {
		t.Fatalf("JWT has %d segments, want 3", len(segments))
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(segments[0])
	if err != nil {
		t.Fatalf("decode JWT header: %v", err)
	}
	var header map[string]string
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		t.Fatalf("decode JWT header JSON: %v", err)
	}
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Errorf("JWT header = %#v", header)
	}

	claimsBytes, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		t.Fatalf("decode JWT claims: %v", err)
	}
	var claims struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		t.Fatalf("decode JWT claims JSON: %v", err)
	}
	if claims.Issuer != "123" {
		t.Errorf("issuer = %q, want 123", claims.Issuer)
	}
	if claims.IssuedAt != now.Add(-time.Minute).Unix() {
		t.Errorf("issued at = %d", claims.IssuedAt)
	}
	if claims.ExpiresAt != now.Add(9*time.Minute).Unix() {
		t.Errorf("expires at = %d", claims.ExpiresAt)
	}

	signature, err := base64.RawURLEncoding.DecodeString(segments[2])
	if err != nil {
		t.Fatalf("decode JWT signature: %v", err)
	}
	digest := sha256.Sum256([]byte(segments[0] + "." + segments[1]))
	if err := rsa.VerifyPKCS1v15(&publicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Errorf("verify JWT signature: %v", err)
	}
}

func mapGetenv(environment map[string]string) func(string) string {
	return func(name string) string {
		return environment[name]
	}
}
