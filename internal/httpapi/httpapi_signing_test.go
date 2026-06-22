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
// Please see https://openpit.dev and the OWNERS file for details.

package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.openpit.dev/officer/internal/backend"
	"go.openpit.dev/officer/internal/domain"
)

// TestGenerateSigningKey_HappyPath exercises the POST /signing/keys/generate
// handler: it expects a 201 with key metadata and NO private key in the body.
func TestGenerateSigningKey_HappyPath(t *testing.T) {
	pub := make([]byte, 32)
	for i := range pub {
		pub[i] = byte(i)
	}
	svc := &fakeService{
		signingKey: domain.SigningKey{
			KeyID:      "key-1",
			Alg:        "ed25519",
			PublicKey:  pub,
			PrivateKey: []byte("secret-must-not-appear"),
			Active:     true,
			CreatedAt:  time.Now().UTC(),
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/v1/signing/keys/generate", http.NoBody))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	key, ok := m["key"].(map[string]any)
	if !ok {
		t.Fatalf("want key object, got %T", m["key"])
	}
	if key["keyId"] != "key-1" {
		t.Errorf("want keyId=key-1, got %v", key["keyId"])
	}
	if key["alg"] != "ed25519" {
		t.Errorf("want alg=ed25519, got %v", key["alg"])
	}
	if _, present := key["privateKey"]; present {
		t.Error("privateKey must not appear in DTO response")
	}
	// Fingerprint must be set (non-empty) for a key that has public material.
	if fp, _ := key["fingerprint"].(string); fp == "" {
		t.Error("want non-empty fingerprint")
	}
}

// TestImportSigningKey_HappyPath exercises POST /signing/keys/import with a
// valid body; it expects 201 and no private key in the response.
func TestImportSigningKey_HappyPath(t *testing.T) {
	pub := make([]byte, 32)
	svc := &fakeService{
		signingKey: domain.SigningKey{
			KeyID:      "key-2",
			Alg:        "ed25519",
			PublicKey:  pub,
			PrivateKey: []byte("secret"),
			Active:     true,
			CreatedAt:  time.Now().UTC(),
		},
	}
	body, _ := json.Marshal(map[string]any{
		"key":    "-----BEGIN PRIVATE KEY-----\nABC\n-----END PRIVATE KEY-----",
		"format": "pem-pkcs8",
	})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/signing/keys/import",
			bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	key, ok := m["key"].(map[string]any)
	if !ok {
		t.Fatalf("want key object, got %T", m["key"])
	}
	if _, present := key["privateKey"]; present {
		t.Error("privateKey must not appear in import response")
	}
}

// TestImportSigningKey_MissingFields verifies that missing key or format yields
// a 400 signing error, not a 500.
func TestImportSigningKey_MissingFields(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"missing_key", map[string]any{"format": "pem-pkcs8"}},
		{"missing_format", map[string]any{"key": "abc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(tc.body)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec,
				httptest.NewRequest(http.MethodPost, "/api/v1/signing/keys/import",
					bytes.NewReader(b)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d", rec.Code)
			}
			m := bodyMap(t, rec.Result())
			errObj, _ := m["error"].(map[string]any)
			if errObj["code"] != "signing" {
				t.Errorf("want code=signing, got %v", errObj["code"])
			}
		})
	}
}

// TestListSigningKeys_HappyPath exercises GET /signing/keys and verifies that
// all returned entries lack private material.
func TestListSigningKeys_HappyPath(t *testing.T) {
	pub := make([]byte, 32)
	svc := &fakeService{
		signingKeys: []domain.SigningKey{
			{
				KeyID:      "key-1",
				Alg:        "ed25519",
				PublicKey:  pub,
				PrivateKey: []byte("secret"),
				Active:     true,
				CreatedAt:  time.Now().UTC(),
			},
			{
				KeyID:      "key-0",
				Alg:        "ed25519",
				PublicKey:  pub,
				PrivateKey: nil,
				Active:     false,
				CreatedAt:  time.Now().UTC().Add(-time.Hour),
			},
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/signing/keys", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	keys, ok := m["keys"].([]any)
	if !ok || len(keys) != 2 {
		t.Fatalf("want 2 keys, got %v", m["keys"])
	}
	for _, entry := range keys {
		k := entry.(map[string]any)
		if _, present := k["privateKey"]; present {
			t.Errorf("privateKey must not appear for keyId=%v", k["keyId"])
		}
	}
}

// TestGetActivePublicKey_DefaultFormat verifies that omitting ?format= defaults
// to pem-pkcs8 and returns the public key string.
func TestGetActivePublicKey_DefaultFormat(t *testing.T) {
	svc := &fakeService{activePublicKey: "-----BEGIN PUBLIC KEY-----\nABC\n-----END PUBLIC KEY-----"}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/signing/keys/active/public", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.activePublicKeyFormat != "pem-pkcs8" {
		t.Errorf("want default format pem-pkcs8, got %q", svc.activePublicKeyFormat)
	}
}

// TestGetActivePublicKey_BadFormat verifies that an unknown format yields 400.
func TestGetActivePublicKey_BadFormat(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/signing/keys/active/public?format=bad-format", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "signing" {
		t.Errorf("want code=signing, got %v", errObj["code"])
	}
}

// TestGetActivePublicKey_Formats verifies that all three supported formats are
// passed through to the service unchanged.
func TestGetActivePublicKey_Formats(t *testing.T) {
	for _, format := range []string{"pem-pkcs8", "openssh", "raw-base64"} {
		t.Run(format, func(t *testing.T) {
			svc := &fakeService{activePublicKey: "data"}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/api/v1/signing/keys/active/public?format="+format, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
			if svc.activePublicKeyFormat != format {
				t.Errorf("want format=%s forwarded, got %q", format, svc.activePublicKeyFormat)
			}
		})
	}
}

// TestGetSigningConfig_HappyPath verifies GET /signing/config returns noESign.
func TestGetSigningConfig_HappyPath(t *testing.T) {
	svc := &fakeService{noESign: true}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/signing/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	if m["noESign"] != true {
		t.Errorf("want noESign=true, got %v", m["noESign"])
	}
}

// TestSetSigningConfig_HappyPath verifies PUT /signing/config persists the flag.
func TestSetSigningConfig_HappyPath(t *testing.T) {
	svc := &fakeService{}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"noESign": true})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPut, "/api/v1/signing/config",
			bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if !svc.noESignSet {
		t.Error("want SetNoESign called with true")
	}
}

// TestSubmitOrderToken_HappyPath verifies POST /orders/{id}/submit returns the
// approval token envelope.
func TestSubmitOrderToken_HappyPath(t *testing.T) {
	exp := time.Now().UTC().Add(2 * time.Minute)
	svc := &fakeService{
		orderDetail: domain.OrderDetail{
			Order: domain.Order{
				ID:          42,
				Account:     "acc-1",
				BaseAsset:   "BTC",
				QuoteAsset:  "USDT",
				Side:        domain.OrderSideBuy,
				AmountKind:  domain.OrderAmountKindQuantity,
				AmountValue: "1",
				Status:      domain.OrderStatusAccepted,
			},
		},
		approvalToken: backend.ApprovalToken{
			Token:     "eyJhbHQ...",
			KeyID:     "key-1",
			ExpiresAt: exp,
			OrderID:   42,
			Signed:    true,
		},
	}
	body, _ := json.Marshal(map[string]any{"mode": "immediate"})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/42/submit",
			bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	if m["token"] != "eyJhbHQ..." {
		t.Errorf("want token in body, got %v", m["token"])
	}
	if m["orderId"] != float64(42) {
		t.Errorf("want orderId=42, got %v", m["orderId"])
	}
}

// TestSubmitOrderToken_BadMode verifies that an unrecognised mode yields 400
// with code "signing".
func TestSubmitOrderToken_BadMode(t *testing.T) {
	svc := &fakeService{
		orderDetail: domain.OrderDetail{Order: domain.Order{ID: 1}},
	}
	body, _ := json.Marshal(map[string]any{"mode": "unsupported"})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/1/submit",
			bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "signing" {
		t.Errorf("want code=signing, got %v", errObj["code"])
	}
}

// TestConfirmExecution_HappyPath verifies POST /orders/{id}/confirm returns the
// updated order.
func TestConfirmExecution_HappyPath(t *testing.T) {
	svc := &fakeService{
		submitOrder: domain.Order{ID: 42, Status: domain.OrderStatusCommitted},
	}
	body, _ := json.Marshal(map[string]any{"token": "mytoken", "force": true})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/42/confirm",
			bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	ord, _ := m["order"].(map[string]any)
	if ord["id"] != float64(42) {
		t.Errorf("want orderId=42, got %v", ord["id"])
	}
	if !svc.confirmForce {
		t.Fatal("force was not forwarded to ConfirmExecution")
	}
}

// TestConfirmExecution_MissingToken verifies that a missing token yields 400.
func TestConfirmExecution_MissingToken(t *testing.T) {
	svc := &fakeService{}
	body, _ := json.Marshal(map[string]any{})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/1/confirm",
			bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

// TestCancelOrder_HappyPath verifies POST /orders/{id}/cancel returns the
// updated order.
func TestCancelOrder_HappyPath(t *testing.T) {
	svc := &fakeService{
		submitOrder: domain.Order{ID: 42, Status: domain.OrderStatusRejected},
	}
	body, _ := json.Marshal(map[string]any{
		"token": "mytoken", "reason": "user request", "force": true,
	})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/42/cancel",
			bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	ord, _ := m["order"].(map[string]any)
	if ord["id"] != float64(42) {
		t.Errorf("want orderId=42, got %v", ord["id"])
	}
	if !svc.cancelForce {
		t.Fatal("force was not forwarded to CancelOrder")
	}
}

// TestCancelOrder_MissingToken verifies that a missing token yields 400.
func TestCancelOrder_MissingToken(t *testing.T) {
	svc := &fakeService{}
	body, _ := json.Marshal(map[string]any{"reason": "x"})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/1/cancel",
			bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

// TestToSigningKeyDTO_NoPrivateKey is a unit test for the DTO mapper: it asserts
// that PrivateKey is NEVER copied regardless of what the domain.SigningKey holds.
func TestToSigningKeyDTO_NoPrivateKey(t *testing.T) {
	pub := make([]byte, 32)
	for i := range pub {
		pub[i] = byte(i + 1)
	}
	k := domain.SigningKey{
		KeyID:      "k",
		Alg:        "ed25519",
		PublicKey:  pub,
		PrivateKey: []byte("private-material"),
		Active:     true,
		CreatedAt:  time.Now().UTC(),
	}
	dto := toSigningKeyDTO(k)
	// Encode to JSON and back to verify no private key field exists.
	b, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, forbidden := range []string{"privateKey", "private_key", "PrivateKey"} {
		if _, ok := raw[forbidden]; ok {
			t.Errorf("field %q must not appear in signingKeyDTO JSON", forbidden)
		}
	}
	if raw["keyId"] != "k" {
		t.Errorf("want keyId=k, got %v", raw["keyId"])
	}
	if fp, _ := raw["fingerprint"].(string); fp == "" {
		t.Error("want non-empty fingerprint")
	}
}
