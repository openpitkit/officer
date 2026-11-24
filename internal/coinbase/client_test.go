// Copyright The Pit Project Owners. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Please see https://officer.openpit.dev and the OWNERS file for details.

package coinbase

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/secret"
)

type testIdentity struct {
	credentials Credentials
	pem         string
	redact      *strings.Replacer
}

func newIdentity(t *testing.T) testIdentity {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal("generate test key failed")
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal("encode test key failed")
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	raw := credentialsJSON(t, keyPEM)
	credentials, err := ParseCredentials(raw)
	if err != nil {
		t.Fatal("valid credentials refused")
	}
	_, redact, err := secret.CredentialRedactor(raw)
	if err != nil {
		t.Fatal("create test redactor failed")
	}
	return testIdentity{credentials, keyPEM, redact}
}

func credentialsJSON(t *testing.T, privateKey string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"apiKeyId":     "organizations/test-organization/apiKeys/test-key",
		"apiKeySecret": privateKey,
	})
	if err != nil {
		t.Fatal("encode credential input failed")
	}
	return string(raw)
}

func TestParseCredentials(t *testing.T) {
	identity := newIdentity(t)
	if identity.credentials.APIKeyID != "organizations/test-organization/apiKeys/test-key" ||
		identity.credentials.PrivateKey.Curve != elliptic.P256() {
		t.Fatal("valid CDP credentials changed")
	}
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal("generate Ed25519 key failed")
	}
	edDER, err := x509.MarshalPKCS8PrivateKey(ed)
	if err != nil {
		t.Fatal("encode Ed25519 key failed")
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(identity.credentials.PrivateKey)
	if err != nil {
		t.Fatal("encode PKCS8 key failed")
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal("generate RSA key failed")
	}
	block, _ := pem.Decode([]byte(identity.pem))
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal("generate P-384 key failed")
	}
	p384DER, err := x509.MarshalECPrivateKey(p384)
	if err != nil {
		t.Fatal("encode P-384 key failed")
	}
	for _, tc := range []struct {
		name string
		pem  string
	}{
		{"pkcs8_ed25519_in_sec1_block", string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: edDER}))},
		{"sec1_p384", string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: p384DER}))},
		{"pkcs8", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))},
		{"rsa", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}))},
		{"encrypted", string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: block.Bytes,
			Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-256-CBC,00"}}))},
		{"two_blocks", identity.pem + identity.pem},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCredentials(credentialsJSON(t, tc.pem))
			if !errors.Is(err, domain.ErrInvalid) || domain.ValidationPointer(err) != "/apiKeySecret" ||
				domain.ValidationConstraint(err) != "format" ||
				err.Error() != "coinbase: apiKeySecret must be a PEM EC P-256 private key" {
				t.Fatal("expected unchanged apikey validation error at /apiKeySecret")
			}
			assertSecretFree(t, err, identity, tc.pem)
		})
	}
}

func assertSecretFree(t *testing.T, err error, identity testIdentity, extra ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	rawKey, keyErr := identity.credentials.PrivateKey.Bytes()
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	scalar := new(big.Int).SetBytes(rawKey)
	secrets := append([]string{identity.credentials.APIKeyID, identity.pem,
		strings.TrimSpace(identity.pem), scalar.Text(16), scalar.Text(10)}, extra...)
	var check func(error)
	check = func(layer error) {
		if layer == nil {
			return
		}
		for _, value := range secrets {
			if value != "" && strings.Contains(layer.Error(), value) {
				t.Error("secret leaked in error chain")
			}
		}
		if _, ok := layer.(*url.Error); ok {
			t.Error("URL error layer retained")
		}
		if joined, ok := layer.(interface{ Unwrap() []error }); ok {
			for _, inner := range joined.Unwrap() {
				check(inner)
			}
		} else {
			check(errors.Unwrap(layer))
		}
	}
	check(err)
}

func verifyJWT(t *testing.T, token string, identity testIdentity, uri string,
	now int64,
) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 || strings.ContainsAny(token, "=+/") {
		t.Fatal("JWT must have three unpadded base64url parts")
	}
	decode := func(part string) []byte {
		decoded, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			t.Fatal("invalid JWT base64url")
		}
		return decoded
	}
	var header map[string]json.RawMessage
	var claims map[string]json.RawMessage
	if json.Unmarshal(decode(parts[0]), &header) != nil ||
		json.Unmarshal(decode(parts[1]), &claims) != nil {
		t.Fatal("invalid JWT JSON")
	}
	stringField := func(fields map[string]json.RawMessage, name, want string) {
		var got string
		if json.Unmarshal(fields[name], &got) != nil || got != want {
			t.Errorf("JWT %s mismatch", name)
		}
	}
	if len(header) != 4 {
		t.Error("unexpected JWT header fields")
	}
	stringField(header, "alg", "ES256")
	stringField(header, "kid", identity.credentials.APIKeyID)
	stringField(header, "typ", "JWT")
	var nonce string
	if json.Unmarshal(header["nonce"], &nonce) != nil {
		t.Fatal("missing JWT nonce")
	}
	decodedNonce, err := hex.DecodeString(nonce)
	if err != nil || len(decodedNonce) != 16 || nonce != strings.ToLower(nonce) {
		t.Error("nonce must be 16 bytes encoded as lowercase hex")
	}
	stringField(claims, "iss", "cdp")
	stringField(claims, "sub", identity.credentials.APIKeyID)
	for name, want := range map[string]int64{"nbf": now, "exp": now + 120} {
		var got int64
		if json.Unmarshal(claims[name], &got) != nil || got != want {
			t.Errorf("JWT %s mismatch", name)
		}
	}
	if uri == "" {
		if _, present := claims["uri"]; present || len(claims) != 4 {
			t.Error("WS JWT must omit uri and contain exactly four claims")
		}
	} else {
		stringField(claims, "uri", uri)
		if len(claims) != 5 {
			t.Error("REST JWT must contain exactly five claims")
		}
	}
	signature := decode(parts[2])
	if len(signature) != 64 {
		t.Fatal("signature must be 64 raw bytes, not DER")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&identity.credentials.PrivateKey.PublicKey, digest[:],
		new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		t.Error("JWT signature verification failed")
	}
	return nonce
}

func TestRESTJWTAndJSON(t *testing.T) {
	identity := newIdentity(t)
	var tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3/brokerage/orders" ||
			r.URL.Query().Get("cursor") != "a+b&c" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("request method, path, query or content type changed")
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["request"] != "value" {
			t.Error("JSON request body changed")
		}
		tokens = append(tokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"result":"ok"}`)
	}))
	defer server.Close()
	client := NewClient(server.URL, identity.credentials, identity.redact, server.Client())
	now := time.Unix(1700000000, 0)
	client.now = func() time.Time { return now }
	nonceBytes := append(bytes.Repeat([]byte{0x12}, 16), bytes.Repeat([]byte{0x34}, 16)...)
	client.nonce = bytes.NewReader(nonceBytes)
	query := url.Values{"cursor": {"a+b&c"}}
	for i := 0; i < 2; i++ {
		var out map[string]string
		status, err := client.Do(context.Background(), http.MethodPost, "/api/v3/brokerage/orders",
			query, map[string]string{"request": "value"}, &out)
		if err != nil || status != http.StatusCreated || out["result"] != "ok" {
			t.Fatal("signed JSON request failed")
		}
		now = now.Add(time.Second)
	}
	if query.Get("cursor") != "a+b&c" || len(query) != 1 {
		t.Error("caller query mutated")
	}
	u, _ := url.Parse(server.URL)
	for i, token := range tokens {
		nonce := verifyJWT(t, token, identity,
			"POST "+u.Host+"/api/v3/brokerage/orders", 1700000000+int64(i))
		if nonce != hex.EncodeToString(nonceBytes[i*16:(i+1)*16]) {
			t.Error("nonce did not consume the injected random source")
		}
	}
	if len(tokens) != 2 || tokens[0] == tokens[1] {
		t.Error("each REST request must mint a fresh JWT")
	}
}

func TestRESTAPIErrorRedaction(t *testing.T) {
	identity := newIdentity(t)
	for _, status := range []int{http.StatusUnauthorized, http.StatusBadRequest, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var token string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				w.WriteHeader(status)
				if status == http.StatusServiceUnavailable {
					_, _ = io.WriteString(w, "not-json upstream-marker "+token+identity.pem)
					return
				}
				message := "authentication refused"
				if status == http.StatusBadRequest {
					message = r.Header.Get("Authorization") + " " + identity.credentials.APIKeyID +
						" " + identity.pem
				}
				code := "UNAUTHENTICATED"
				if status == http.StatusBadRequest {
					code = message
				}
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": code, "message": message,
					"error_details": message, "preview_failure_reason": message,
				})
			}))
			defer server.Close()
			client := NewClient(server.URL, identity.credentials, identity.redact, server.Client())
			gotStatus, err := client.Do(context.Background(), "GET", "/test", nil, nil, nil)
			assertSecretFree(t, err, identity, token)
			var apiErr *APIError
			if gotStatus != status || !errors.As(err, &apiErr) || apiErr.Status != status {
				t.Fatal("non-2xx must return APIError with status")
			}
			if apiErr.BodyDecoded != (status != http.StatusServiceUnavailable) {
				t.Error("Coinbase error body recognition changed")
			}
			if status == http.StatusUnauthorized &&
				(apiErr.Code != "UNAUTHENTICATED" || apiErr.Message != "authentication refused" ||
					apiErr.ErrorDetails != apiErr.Message || apiErr.PreviewFailureReason != apiErr.Message) {
				t.Error("Coinbase error fields changed")
			}
			if status == http.StatusServiceUnavailable &&
				(apiErr.Message != "" || apiErr.Code != "" || apiErr.ErrorDetails != "" ||
					apiErr.PreviewFailureReason != "" || strings.Contains(err.Error(), "upstream-marker")) {
				t.Error("non-JSON error body retained")
			}
			for _, field := range []string{apiErr.Code, apiErr.Message, apiErr.ErrorDetails, apiErr.PreviewFailureReason} {
				assertSecretFree(t, errors.New(field), identity, token)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAPIErrorBodyDecoded(t *testing.T) {
	identity := newIdentity(t)
	for _, tc := range []struct {
		name, body string
		decoded    bool
	}{
		{"coinbase", `{"error":"INVALID_ARGUMENT","code":3,"message":"x","details":[]}`, true},
		{"empty_strings", `{"error":"","message":""}`, true},
		{"empty_object", `{}`, false},
		{"null", `null`, false},
		{"html", `<html>x</html>`, false},
		{"message_only", `{"message":"x"}`, false},
		{"error_only", `{"error":"x"}`, false},
		{"wrong_error_type", `{"error":5,"message":"x"}`, false},
		{"wrong_message_type", `{"error":"x","message":5}`, false},
		{"null_error", `{"error":null,"message":"x"}`, false},
		{"null_message", `{"error":"x","message":null}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var minted string
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				minted = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				body := strings.ReplaceAll(tc.body, "x", identity.credentials.APIKeyID+minted)
				return &http.Response{StatusCode: http.StatusBadRequest,
					Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			client := NewClient(LiveRESTURL, identity.credentials, identity.redact,
				&http.Client{Transport: transport})
			status, err := client.Do(context.Background(), "GET", "/test", nil, nil, nil)
			var apiErr *APIError
			if status != http.StatusBadRequest || !errors.As(err, &apiErr) {
				t.Fatal("non-2xx response must return APIError")
			}
			if apiErr.BodyDecoded != tc.decoded || errors.Is(err, ErrUnparsableResponse) {
				t.Error("API error body classification changed")
			}
			assertSecretFree(t, err, identity, minted)
		})
	}
}

func TestRESTCancellationIsFlattened(t *testing.T) {
	identity := newIdentity(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewClient(LiveRESTURL, identity.credentials, identity.redact,
		&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, r.Context().Err()
		})})
	status, err := client.Do(ctx, "GET", "/test", nil, nil, nil)
	assertSecretFree(t, err, identity)
	var apiErr *APIError
	if status != 0 || errors.Is(err, context.Canceled) ||
		errors.Is(err, ErrUnparsableResponse) || errors.As(err, &apiErr) ||
		!errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("transport error must be flattened; cancellation belongs to the context")
	}
}

func TestEarlierJWTIsRedacted(t *testing.T) {
	identity := newIdentity(t)
	var previous string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if previous == "" {
			previous = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)),
				Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: 400, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"message":"Bearer ` + previous + `"}`))}, nil
	})
	client := NewClient("https://api.coinbase.com", identity.credentials, identity.redact,
		&http.Client{Transport: transport})
	if _, err := client.Do(context.Background(), "GET", "/", nil, nil, nil); err != nil {
		t.Fatal("first request failed")
	}
	_, err := client.Do(context.Background(), "GET", "/", nil, nil, nil)
	assertSecretFree(t, err, identity, previous)
}

type hiddenCause struct{ cause error }

func (e hiddenCause) Error() string { return "safe outer message" }
func (e hiddenCause) Unwrap() error { return e.cause }

type failingJSON struct{ text string }

func (f failingJSON) MarshalJSON() ([]byte, error)  { return nil, errors.New(f.text) }
func (f *failingJSON) UnmarshalJSON(p []byte) error { return errors.New(f.text + string(p)) }

type failingBody struct{ text string }

func (f failingBody) Read([]byte) (int, error) { return 0, errors.New(f.text) }
func (f failingBody) Close() error             { return nil }

func TestRESTErrorChains(t *testing.T) {
	identity := newIdentity(t)
	for _, mode := range []string{"transport", "hidden_cause", "read", "decode", "encode", "invalid_url"} {
		t.Run(mode, func(t *testing.T) {
			var minted string
			secretText := identity.credentials.APIKeyID + identity.pem
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				minted = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				text := secretText + minted
				if mode == "transport" {
					return nil, &url.Error{Op: "GET", URL: "https://invalid/" + identity.credentials.APIKeyID,
						Err: errors.New(text)}
				}
				if mode == "hidden_cause" {
					return nil, hiddenCause{errors.New(text)}
				}
				body := io.NopCloser(strings.NewReader(`{"jwt":"` + minted + `"}`))
				if mode == "read" {
					body = failingBody{text}
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
			})
			baseURL := "https://api.coinbase.com"
			if mode == "invalid_url" {
				baseURL = "://" + identity.credentials.APIKeyID
			}
			client := NewClient(baseURL, identity.credentials, identity.redact, &http.Client{Transport: transport})
			var body any
			if mode == "encode" {
				body = failingJSON{secretText}
			}
			out := &failingJSON{secretText}
			_, err := client.Do(context.Background(), "GET", "/test", nil, body, out)
			assertSecretFree(t, err, identity, minted)
			if errors.Is(err, ErrUnparsableResponse) != (mode == "decode") {
				t.Error("decode failure must be distinguishable from local and transport failures")
			}
		})
	}
}

func TestRESTRedirectRefused(t *testing.T) {
	identity := newIdentity(t)
	var hits atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer second.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL, http.StatusFound)
	}))
	defer first.Close()
	original := first.Client()
	client := NewClient(first.URL, identity.credentials, identity.redact, original)
	status, err := client.Do(context.Background(), "GET", "/test", nil, nil, nil)
	var apiErr *APIError
	if hits.Load() != 0 {
		t.Error("REST redirect reached second host")
	}
	if status != http.StatusFound || !errors.As(err, &apiErr) || apiErr.Status != http.StatusFound {
		t.Error("redirect must return APIError with HTTP 302")
	}
	if original.CheckRedirect != nil || client.httpClient == original ||
		client.httpClient.Transport != original.Transport {
		t.Error("caller client mutated or transport replaced")
	}
}

func TestRESTTLSVerification(t *testing.T) {
	identity := newIdentity(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	untrusted := NewClient(server.URL, identity.credentials, identity.redact, &http.Client{})
	if _, err := untrusted.Do(context.Background(), "GET", "/", nil, nil, nil); err == nil {
		t.Error("untrusted TLS certificate accepted")
	}
	trusted := NewClient(server.URL, identity.credentials, identity.redact, server.Client())
	if _, err := trusted.Do(context.Background(), "GET", "/", nil, nil, nil); err != nil {
		t.Error("caller TLS trust was not preserved")
	}
}

type countingBody struct {
	reader io.Reader
	read   int
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += n
	return n, err
}
func (*countingBody) Close() error { return nil }

func TestResponseLimit(t *testing.T) {
	identity := newIdentity(t)
	for _, status := range []int{200, 500} {
		for _, withOut := range []bool{false, true} {
			t.Run(strconv.Itoa(status)+"/out="+strconv.FormatBool(withOut), func(t *testing.T) {
				body := &countingBody{reader: io.MultiReader(strings.NewReader(`{}`),
					strings.NewReader(strings.Repeat(" ", maxResponseBytes*2)))}
				transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: status, Body: body, Header: make(http.Header)}, nil
				})
				client := NewClient("https://api.coinbase.com", identity.credentials, identity.redact,
					&http.Client{Transport: transport})
				var out any
				if withOut {
					out = &map[string]string{}
				}
				gotStatus, err := client.Do(context.Background(), "GET", "/test", nil, nil, out)
				if gotStatus != status {
					t.Error("oversize response was not rejected with its status")
				}
				if status >= 200 && status < 300 {
					if !errors.Is(err, ErrUnparsableResponse) {
						t.Error("oversize 2xx response must wrap ErrUnparsableResponse")
					}
				} else {
					var apiErr *APIError
					if !errors.As(err, &apiErr) || *apiErr != (APIError{Status: status}) {
						t.Error("oversize non-2xx response must return only APIError status")
					}
				}
				if body.read > maxResponseBytes+1 {
					t.Error("response read exceeded bounded limit")
				}
			})
		}
	}
	for _, size := range []int{maxResponseBytes - 1, maxResponseBytes} {
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{}` + strings.Repeat(" ", size-2)))}, nil
		})
		client := NewClient("https://api.coinbase.com", identity.credentials, identity.redact, &http.Client{Transport: transport})
		if _, err := client.Do(context.Background(), "GET", "/", nil, nil, &map[string]string{}); err != nil {
			t.Error("response at or below limit refused")
		}
	}
}

func TestMissingPrerequisites(t *testing.T) {
	identity := newIdentity(t)
	for _, tc := range []struct {
		name string
		edit func(*Client)
		want string
	}{
		{"http", func(c *Client) { c.httpClient = nil }, "HTTP client is required"},
		{"redactor", func(c *Client) { c.redact = nil }, "credential redactor is required"},
		{"credentials", func(c *Client) { c.credentials = Credentials{} }, "valid CDP credentials are required"},
		{"nonce", func(c *Client) { c.nonce = strings.NewReader("") }, "generate JWT nonce failed"},
		{"nonce_error", func(c *Client) {
			c.nonce = failingBody{identity.pem + identity.credentials.APIKeyID}
		}, "generate JWT nonce failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient("https://api.coinbase.com", identity.credentials, identity.redact, &http.Client{})
			tc.edit(client)
			_, err := client.Do(context.Background(), "GET", "/", nil, nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Error("missing prerequisite did not fail explicitly")
			}
			assertSecretFree(t, err, identity)
		})
	}
}
