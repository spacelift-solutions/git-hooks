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
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	githubTokenEnv        = "GH_TOKEN"
	githubActionsTokenEnv = "GITHUB_TOKEN"
	appIDEnv              = "TF_VAR_github_app_id"
	installationIDEnv     = "TF_VAR_github_app_installation_id"
	appPEMPathEnv         = "GITHUB_APP_PEM_FILE_PATH"
)

// AuthOptions provides the dependencies used to resolve a GitHub token.
type AuthOptions struct {
	APIURL     string
	HTTPClient *http.Client
	Getenv     func(string) string
	ReadFile   func(string) ([]byte, error)
	Now        func() time.Time
}

// ResolveToken uses an existing token when available, otherwise it creates a
// short-lived GitHub App JWT and exchanges it for an installation token.
func ResolveToken(ctx context.Context, options AuthOptions) (string, error) {
	getenv := options.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	if token := getenv(githubTokenEnv); token != "" {
		return token, nil
	}
	if token := getenv(githubActionsTokenEnv); token != "" {
		return token, nil
	}

	appID := getenv(appIDEnv)
	installationID := getenv(installationIDEnv)
	pemPath := getenv(appPEMPathEnv)
	if appID == "" || installationID == "" || pemPath == "" {
		return "", fmt.Errorf(
			"authentication required: set %s or %s, or provide %s, %s, and %s",
			githubTokenEnv,
			githubActionsTokenEnv,
			appIDEnv,
			installationIDEnv,
			appPEMPathEnv,
		)
	}

	readFile := options.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	privateKeyPEM, err := readFile(pemPath)
	if err != nil {
		return "", fmt.Errorf("read GitHub App private key %q: %w", pemPath, err)
	}
	privateKey, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return "", fmt.Errorf("parse GitHub App private key %q: %w", pemPath, err)
	}

	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	appJWT, err := signAppJWT(appID, privateKey, now())
	if err != nil {
		return "", fmt.Errorf("create GitHub App JWT: %w", err)
	}

	var response struct {
		Token string `json:"token"`
	}
	client := NewClient(options.APIURL, appJWT, options.HTTPClient)
	path := "/app/installations/" + url.PathEscape(installationID) + "/access_tokens"
	if err := client.Do(ctx, http.MethodPost, path, nil, &response); err != nil {
		return "", fmt.Errorf("mint GitHub App installation token: %w", err)
	}
	if response.Token == "" {
		return "", fmt.Errorf("mint GitHub App installation token: GitHub returned an empty token")
	}
	return response.Token, nil
}

func parsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("PEM block not found")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("expected an RSA PKCS#1 or PKCS#8 private key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("expected an RSA private key")
	}
	return key, nil
}

func signAppJWT(appID string, privateKey *rsa.PrivateKey, now time.Time) (string, error) {
	header, err := json.Marshal(struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}{
		Algorithm: "RS256",
		Type:      "JWT",
	})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}{
		IssuedAt:  now.Add(-time.Minute).Unix(),
		ExpiresAt: now.Add(9 * time.Minute).Unix(),
		Issuer:    strings.TrimSpace(appID),
	})
	if err != nil {
		return "", err
	}

	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
