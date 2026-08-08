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
	"strconv"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
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
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("want 400, got %d", rec.Code)
			}
			m := bodyMap(t, rec.Result())
			errObj, _ := m["error"].(map[string]any)
			if errObj["code"] != "validation" {
				t.Errorf("want code=validation, got %v", errObj["code"])
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
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Errorf("want code=validation, got %v", errObj["code"])
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
	for _, noESign := range []bool{false, true} {
		t.Run(strconv.FormatBool(noESign), func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(map[string]any{"noESign": noESign})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(
				http.MethodPut, "/api/v1/signing/config", bytes.NewReader(body),
			))
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
			if svc.noESignCalls != 1 || svc.noESignSet != noESign {
				t.Fatalf(
					"SetNoESign calls=%d value=%v, want 1/%v",
					svc.noESignCalls, svc.noESignSet, noESign,
				)
			}
		})
	}
}

func TestSetSigningConfig_RequiresNoESign(t *testing.T) {
	for _, body := range []string{`{}`, `{"noESign":null}`} {
		t.Run(body, func(t *testing.T) {
			svc := &fakeService{}
			r, err := newRouter(svc)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(
				http.MethodPut, "/api/v1/signing/config",
				bytes.NewBufferString(body),
			))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
			}
			if svc.noESignCalls != 0 {
				t.Fatalf("invalid request reached service %d times", svc.noESignCalls)
			}
		})
	}
}

// TestSubmitOrderToken_HappyPath verifies POST /orders/submit CREATES the order
// from a supplied external id and returns that id in the approval envelope. The
// fake mirrors the real create-once behavior: it does not echo a pre-stored id,
// so the returned id is the one the caller supplied — and it is the id a later
// confirm resolves, proving submit created exactly one order.
func TestSubmitOrderToken_HappyPath(t *testing.T) {
	supplied := extID("order-1").String()
	svc := &fakeService{
		approvalToken: backend.ApprovalToken{
			Token:  "eyJhbHQ...",
			KeyID:  "key-1",
			Signed: true,
		},
	}
	body, _ := json.Marshal(map[string]any{
		"id":          supplied,
		"account":     "acc-1",
		"baseAsset":   "BTC",
		"quoteAsset":  "USDT",
		"side":        "buy",
		"amountKind":  "quantity",
		"amountValue": "1",
		"mode":        "immediate",
	})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/submit?missingAccount=create", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	if m["token"] != "eyJhbHQ..." {
		t.Errorf("want token in body, got %v", m["token"])
	}
	// The supplied external id is used as-is on the created order.
	if svc.submitOrderIn.ExternalID.String() != supplied {
		t.Errorf("supplied id not threaded onto order: got %s", svc.submitOrderIn.ExternalID)
	}
	// The authorised order is referenced by its opaque external id, never a surrogate.
	if m["id"] != supplied {
		t.Errorf("want id=%s, got %v", supplied, m["id"])
	}
	if _, present := m["orderId"]; present {
		t.Errorf("response leaked surrogate orderId: %v", m)
	}
	// The returned id must be the one subsequently confirmable: confirm resolves
	// the SAME order, proving submit created exactly one and no second order.
	confirmBody, _ := json.Marshal(map[string]any{"token": "mytoken"})
	confRec := httptest.NewRecorder()
	r.ServeHTTP(confRec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+supplied+"/confirm",
			bytes.NewReader(confirmBody)))
	if confRec.Code != http.StatusOK {
		t.Fatalf("confirm: want 200, got %d: %s", confRec.Code, confRec.Body.String())
	}
	confM := bodyMap(t, confRec.Result())
	ord, _ := confM["order"].(map[string]any)
	if ord["id"] != supplied {
		t.Errorf("confirm resolved a different order: want %s, got %v", supplied, ord["id"])
	}
}

func TestSubmitOrderToken_RiskRejectReturnsSignedDecision(t *testing.T) {
	supplied := extID("order-reject").String()
	svc := &fakeService{
		approvalToken: backend.ApprovalToken{
			Token:           "reject-envelope",
			KeyID:           "key-1",
			OrderExternalID: supplied,
			Verdict:         "reject",
			Reasons: []domain.OrderReject{{
				Code:   "insufficient_funds",
				Scope:  "account",
				Policy: "spot_funds",
				Reason: "available funds below required amount",
			}},
			Signed: true,
		},
	}
	body, _ := json.Marshal(map[string]any{
		"id":          supplied,
		"account":     "acc-1",
		"baseAsset":   "BTC",
		"quoteAsset":  "USDT",
		"side":        "buy",
		"amountKind":  "quantity",
		"amountValue": "1",
		"mode":        "hold",
	})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/submit?missingAccount=create", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	if m["token"] != "reject-envelope" || m["verdict"] != "reject" {
		t.Fatalf("reject response = %v", m)
	}
	reasons, ok := m["reasons"].([]any)
	if !ok || len(reasons) != 1 {
		t.Fatalf("want one reject reason, got %v", m["reasons"])
	}
	reason, _ := reasons[0].(map[string]any)
	if reason["code"] != "insufficient_funds" || reason["reason"] == "" {
		t.Fatalf("reject reason not on wire: %v", reason)
	}
}

// TestSubmitOrderToken_GeneratesWhenAbsent verifies that omitting id has
// the server generate and return a 22-char id.
func TestSubmitOrderToken_GeneratesWhenAbsent(t *testing.T) {
	svc := &fakeService{
		approvalToken: backend.ApprovalToken{Token: "tok", KeyID: "key-1"},
	}
	body, _ := json.Marshal(map[string]any{
		"account":     "acc-1",
		"baseAsset":   "BTC",
		"quoteAsset":  "USDT",
		"side":        "buy",
		"amountKind":  "quantity",
		"amountValue": "1",
		"mode":        "immediate",
	})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/submit?missingAccount=create", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if !svc.submitOrderIn.ExternalID.IsZero() {
		t.Errorf("absent id should leave order external id unset for the backend to generate")
	}
	m := bodyMap(t, rec.Result())
	id, _ := m["id"].(string)
	if len(id) != domain.ExternalIDStringLen {
		t.Errorf("want a generated %d-char id, got %q", domain.ExternalIDStringLen, id)
	}
}

// TestSubmitOrderToken_DuplicateConflict verifies a duplicate supplied id yields
// 409 (the backend rejects with domain.ErrAlreadyExists).
func TestSubmitOrderToken_DuplicateConflict(t *testing.T) {
	svc := &fakeService{signingErr: domain.ErrAlreadyExists}
	body, _ := json.Marshal(map[string]any{
		"id":          extID("order-1").String(),
		"account":     "acc-1",
		"baseAsset":   "BTC",
		"quoteAsset":  "USDT",
		"side":        "buy",
		"amountKind":  "quantity",
		"amountValue": "1",
		"mode":        "immediate",
	})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/submit?missingAccount=create", bytes.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "conflict" {
		t.Errorf("want code=conflict, got %v", errObj["code"])
	}
}

// TestSubmitOrderToken_OpaqueID verifies a caller-supplied id is accepted
// verbatim without a base64url shape requirement.
func TestSubmitOrderToken_OpaqueID(t *testing.T) {
	svc := &fakeService{}
	supplied := "not-a-valid-external-id"
	body, _ := json.Marshal(map[string]any{
		"id":          supplied,
		"account":     "acc-1",
		"baseAsset":   "BTC",
		"quoteAsset":  "USDT",
		"side":        "buy",
		"amountKind":  "quantity",
		"amountValue": "1",
		"mode":        "immediate",
	})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/submit?missingAccount=create", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.submitOrderIn.ExternalID.String() != supplied {
		t.Fatalf("supplied id not threaded onto order: got %s", svc.submitOrderIn.ExternalID)
	}
}

// TestSubmitOrderToken_BadMode verifies that an unrecognised mode yields 400
// with code "signing".
func TestSubmitOrderToken_BadMode(t *testing.T) {
	svc := &fakeService{}
	body, _ := json.Marshal(map[string]any{"account": "acc-1", "mode": "unsupported"})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/submit?missingAccount=create", bytes.NewReader(body)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "validation" {
		t.Errorf("want code=validation, got %v", errObj["code"])
	}
}

// TestConfirmExecution_HappyPath verifies POST /orders/{id}/confirm returns the
// updated order.
func TestConfirmExecution_HappyPath(t *testing.T) {
	svc := &fakeService{
		submitOrder: domain.Order{
			ExternalID: extID("order-1"), Status: domain.OrderStatusCommitted,
		},
	}
	body, _ := json.Marshal(map[string]any{"token": "mytoken"})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+extID("order-1").String()+"/confirm",
			bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	ord, _ := m["order"].(map[string]any)
	if ord["id"] != extID("order-1").String() {
		t.Errorf("want id=%s, got %v", extID("order-1").String(), ord["id"])
	}
	assertNoSurrogateID(t, ord)
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
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+extID("order-1").String()+"/confirm",
			bytes.NewReader(body)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

// TestCancelOrder_HappyPath verifies POST /orders/{id}/cancel returns
// the updated order.
func TestCancelOrder_HappyPath(t *testing.T) {
	svc := &fakeService{
		submitOrder: domain.Order{
			ExternalID: extID("order-1"), Status: domain.OrderStatusRejected,
		},
	}
	body, _ := json.Marshal(map[string]any{
		"token": "mytoken", "leavesQuantity": "3.5", "reason": "user request",
	})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+extID("order-1").String()+"/cancel",
			bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.cancelLeavesQuantity != "3.5" {
		t.Fatalf("forwarded leaves = %q, want 3.5", svc.cancelLeavesQuantity)
	}
	m := bodyMap(t, rec.Result())
	ord, _ := m["order"].(map[string]any)
	if ord["id"] != extID("order-1").String() {
		t.Errorf("want id=%s, got %v", extID("order-1").String(), ord["id"])
	}
	assertNoSurrogateID(t, ord)
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
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+extID("order-1").String()+"/cancel",
			bytes.NewReader(body)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

// TestCancelOrder_OmittedLeavesQuantity verifies that the shortcut forwards an
// omitted leavesQuantity as the empty string.
func TestCancelOrder_OmittedLeavesQuantity(t *testing.T) {
	svc := &fakeService{submitOrder: domain.Order{
		ExternalID: extID("order-1"), Status: domain.OrderStatusRejected,
	}}
	body, _ := json.Marshal(map[string]any{"token": "mytoken"})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+extID("order-1").String()+"/cancel",
			bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.cancelCalls != 1 || svc.cancelLeavesQuantity != "" {
		t.Fatalf(
			"cancel calls = %d leaves = %q, want one call with empty leaves",
			svc.cancelCalls,
			svc.cancelLeavesQuantity,
		)
	}
}

// TestCancelOrder_PreservesLeavesQuantity verifies the HTTP boundary does not
// normalize caller-reported leaves before the normal report validator sees it.
func TestCancelOrder_PreservesLeavesQuantity(t *testing.T) {
	svc := &fakeService{
		submitOrder: domain.Order{
			ExternalID: extID("order-1"), Status: domain.OrderStatusRejected,
		},
	}
	body, _ := json.Marshal(map[string]any{
		"token": "mytoken", "leavesQuantity": "  3.5  ",
	})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+extID("order-1").String()+"/cancel",
			bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.cancelLeavesQuantity != "  3.5  " {
		t.Fatalf("forwarded leaves = %q, want raw value", svc.cancelLeavesQuantity)
	}
}

// TestCancelOrder_BlankLeavesQuantity verifies whitespace is supplied malformed
// data, not an omission synthesized by the HTTP boundary.
func TestCancelOrder_BlankLeavesQuantity(t *testing.T) {
	svc := &fakeService{submitOrder: domain.Order{
		ExternalID: extID("order-1"), Status: domain.OrderStatusRejected,
	}}
	body, _ := json.Marshal(map[string]any{
		"token": "mytoken", "leavesQuantity": "   ",
	})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+extID("order-1").String()+"/cancel",
			bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.cancelCalls != 1 || svc.cancelLeavesQuantity != "   " {
		t.Fatalf(
			"cancel calls = %d leaves = %q, want raw whitespace",
			svc.cancelCalls,
			svc.cancelLeavesQuantity,
		)
	}
}

// TestConfirmExecution_TerminalOrderConflict verifies POST /orders/{id}/confirm
// surfaces the node's ErrTerminalOrder as 409 terminal_order.
func TestConfirmExecution_TerminalOrderConflict(t *testing.T) {
	svc := &fakeService{confirmErr: domain.ErrTerminalOrder}
	body, _ := json.Marshal(map[string]any{"token": "mytoken"})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+extID("order-1").String()+"/confirm",
			bytes.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "terminal_order" {
		t.Fatalf("want code=terminal_order, got %v", errObj["code"])
	}
}

// TestConfirmExecution_ExecutionReportRequired verifies that the history-only
// shortcut tells the caller to use the normal execution-report workflow once
// the order already has report activity.
func TestConfirmExecution_ExecutionReportRequired(t *testing.T) {
	svc := &fakeService{confirmErr: domain.ErrExecutionReportRequired}
	body, _ := json.Marshal(map[string]any{"token": "mytoken"})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+extID("order-1").String()+"/confirm",
			bytes.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "execution_report_required" {
		t.Fatalf("want code=execution_report_required, got %v", errObj["code"])
	}
	if errObj["message"] != domain.ErrExecutionReportRequired.Error() {
		t.Fatalf("want message=%q, got %v",
			domain.ErrExecutionReportRequired.Error(), errObj["message"])
	}
}

// TestCancelOrder_TerminalOrderConflict verifies POST /orders/{id}/cancel on a
// terminal-status order surfaces ErrTerminalOrder as 409 terminal_order.
func TestCancelOrder_TerminalOrderConflict(t *testing.T) {
	svc := &fakeService{cancelErr: domain.ErrTerminalOrder}
	body, _ := json.Marshal(map[string]any{
		"token": "mytoken", "leavesQuantity": "10", "reason": "user request",
	})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+extID("order-1").String()+"/cancel",
			bytes.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "terminal_order" {
		t.Fatalf("want code=terminal_order, got %v", errObj["code"])
	}
}

// TestCancelOrder_ExecutionReportRequired verifies that Officer refuses to
// infer a cancellation report after any explicit execution-report activity.
func TestCancelOrder_ExecutionReportRequired(t *testing.T) {
	svc := &fakeService{cancelErr: domain.ErrExecutionReportRequired}
	body, _ := json.Marshal(map[string]any{
		"token": "mytoken", "leavesQuantity": "10", "reason": "user request",
	})
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+extID("order-1").String()+"/cancel",
			bytes.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != "execution_report_required" {
		t.Fatalf("want code=execution_report_required, got %v", errObj["code"])
	}
	if errObj["message"] != domain.ErrExecutionReportRequired.Error() {
		t.Fatalf("want message=%q, got %v",
			domain.ErrExecutionReportRequired.Error(), errObj["message"])
	}
}

// TestGetSigningKeyPublic_HappyPath verifies GET /signing/keys/{keyId}/public
// resolves the public key by id and returns only public material.
func TestGetSigningKeyPublic_HappyPath(t *testing.T) {
	svc := &fakeService{
		publicKeysByID: map[string]string{
			"key-42": "-----BEGIN PUBLIC KEY-----\nABC\n-----END PUBLIC KEY-----",
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/signing/keys/key-42/public", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.keyByIDLast != "key-42" {
		t.Errorf("want resolution by keyId=key-42, got %q", svc.keyByIDLast)
	}
	if svc.keyByIDFormat != "pem-pkcs8" {
		t.Errorf("want default format pem-pkcs8, got %q", svc.keyByIDFormat)
	}
	m := bodyMap(t, rec.Result())
	if m["keyId"] != "key-42" {
		t.Errorf("want keyId=key-42, got %v", m["keyId"])
	}
	if m["format"] != "pem-pkcs8" {
		t.Errorf("want format=pem-pkcs8, got %v", m["format"])
	}
	if _, ok := m["key"].(string); !ok {
		t.Errorf("want key string, got %T", m["key"])
	}
	for _, forbidden := range []string{"privateKey", "private_key", "priv"} {
		if _, present := m[forbidden]; present {
			t.Errorf("private material %q must not appear", forbidden)
		}
	}
}

// TestGetSigningKeyPublic_Format verifies ?format= is forwarded to the service.
func TestGetSigningKeyPublic_Format(t *testing.T) {
	svc := &fakeService{publicKeysByID: map[string]string{"k": "data"}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/signing/keys/k/public?format=openssh", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if svc.keyByIDFormat != "openssh" {
		t.Errorf("want format=openssh forwarded, got %q", svc.keyByIDFormat)
	}
}

// TestGetSigningKeyPublic_BadFormat verifies an unknown format yields 400.
func TestGetSigningKeyPublic_BadFormat(t *testing.T) {
	svc := &fakeService{publicKeysByID: map[string]string{"k": "data"}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/signing/keys/k/public?format=bad", nil))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

// TestGetSigningKeyPublic_Unknown verifies an unknown key id yields 404.
func TestGetSigningKeyPublic_Unknown(t *testing.T) {
	svc := &fakeService{publicKeysByID: map[string]string{}}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/signing/keys/nope/public", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rec.Code, rec.Body.String())
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
