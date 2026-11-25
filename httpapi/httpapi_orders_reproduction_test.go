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

package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	officerengine "go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	fwsigning "go.openpit.dev/officer/framework/signing"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/store/schema"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	appsigning "go.openpit.dev/officer/signing"
	"go.openpit.dev/openpit/asyncengine"
	"go.openpit.dev/openpit/pretrade"
)

// reproSigningStore is a minimal in-memory appsigning.Store for building real
// signed envelopes in reproduction tests. It lets a key be rotated (a later active
// key generated) while the earlier key stays resolvable by id.
type reproSigningStore struct {
	keys   map[string]domain.SigningKey
	config map[string]string
}

func newReproSigningStore() *reproSigningStore {
	return &reproSigningStore{
		keys:   map[string]domain.SigningKey{},
		config: map[string]string{},
	}
}

func (s *reproSigningStore) UpsertSigningKey(_ context.Context, key domain.SigningKey) error {
	s.keys[key.KeyID] = key
	return nil
}

func (s *reproSigningStore) GetActiveSigningKey(_ context.Context) (domain.SigningKey, bool, error) {
	for _, k := range s.keys {
		if k.Active {
			return k, true, nil
		}
	}
	return domain.SigningKey{}, false, nil
}

func (s *reproSigningStore) GetSigningKey(_ context.Context, keyID string) (domain.SigningKey, error) {
	k, ok := s.keys[keyID]
	if !ok {
		return domain.SigningKey{}, domain.ErrNotFound
	}
	return k, nil
}

func (s *reproSigningStore) ListSigningKeys(_ context.Context) ([]domain.SigningKey, error) {
	out := make([]domain.SigningKey, 0, len(s.keys))
	for _, k := range s.keys {
		k.PrivateKey = nil
		out = append(out, k)
	}
	return out, nil
}

func (s *reproSigningStore) DeactivateAllSigningKeys(_ context.Context) error {
	for id, k := range s.keys {
		k.Active = false
		s.keys[id] = k
	}
	return nil
}

func (s *reproSigningStore) GetSetting(_ context.Context, key string) (string, bool, error) {
	v, ok := s.config[key]
	return v, ok, nil
}

func (s *reproSigningStore) SetSetting(_ context.Context, key, value string) error {
	s.config[key] = value
	return nil
}

// reproEventID is the event handle the reproduction tests attest.
func reproEventID() domain.ExternalID { return extID("repro-event") }

// reproPayload builds a representative accepted submit-verdict attestation
// payload bound to the order id and the reproduction event.
func reproPayload(id domain.ExternalID) domain.ApprovalPayload {
	now := time.Now().UTC()
	return domain.ApprovalPayload{
		Version:         1,
		ApprovalID:      "11111111-1111-1111-1111-111111111111",
		RequestType:     string(domain.AttestationRequestSubmit),
		Mode:            "immediate",
		OrderExternalID: id.String(),
		EventExternalID: reproEventID().String(),
		Instrument:      "AAPL/USD",
		Side:            "buy",
		Quantity:        "10",
		AmountKind:      "quantity",
		OrderType:       "limit",
		LimitPrice:      "150.25",
		PriceCurrency:   "USD",
		AccountID:       "acc-1",
		Verdict:         "accept",
		PolicySummary:   "accepted",
		EstimatePrice:   "150.25",
		IssuedAt:        now.Format(time.RFC3339Nano),
		Nonce:           "Zm9vYmFyYmF6cXV4MTIzNA",
	}
}

// reproOrder builds an order matching reproPayload's bindings.
func reproOrder(id domain.ExternalID) domain.Order {
	return domain.Order{
		At:          time.Now().UTC(),
		ExternalID:  id,
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "10",
		Price:       "150.25",
		Status:      domain.OrderStatusCommitted,
	}
}

// attestationFromToken decodes a real envelope into the persisted EventAttestation
// shape the store would hold, so the fake service can serve it verbatim.
func attestationFromToken(t *testing.T, token string) *domain.EventAttestation {
	t.Helper()
	env, err := fwsigning.DecodeEnvelope(token)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return &domain.EventAttestation{
		SignedEnvelope: token,
		KeyID:          env.KeyID,
		Alg:            env.Approval.Alg,
		RequestType:    domain.AttestationRequestType(env.Approval.RequestType),
		Mode:           env.Approval.Mode,
		IssuedAt:       env.Approval.IssuedAt,
	}
}

// reproDetail builds an order detail with a pre_trade_accepted event carrying
// the given attestation (nil for an unattested event).
func reproDetail(id domain.ExternalID, att *domain.EventAttestation) domain.OrderDetail {
	return domain.OrderDetail{
		Order: reproOrder(id),
		Events: []domain.OrderEvent{
			{
				At:          time.Now().UTC(),
				ExternalID:  reproEventID(),
				Order:       id,
				Type:        domain.OrderEventPreTradeAccepted,
				Source:      domain.SourcePanel,
				Attestation: att,
			},
		},
	}
}

// reproURL is the per-event reproduction endpoint for the reproduction event.
func reproURL(id domain.ExternalID) string {
	return "/api/v1/orders/" + id.String() +
		"/events/" + reproEventID().String() + "/reproduction"
}

// TestOrderReproduction_ByteExactSignedToken verifies the reproduction bundle
// reproduces a signed order's envelope verbatim, its canonical bytes byte-identical
// to what was signed, its signature, and resolves the public key by the envelope's
// keyId. It also asserts no private key material appears anywhere in the body.
func TestOrderReproduction_ByteExactSignedToken(t *testing.T) {
	ctx := context.Background()
	st := newReproSigningStore()
	signer, err := appsigning.New(st)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	key, err := signer.GenerateKey(ctx)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	id := extID("repro-1")
	payload := reproPayload(id)
	token, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// The canonical signed bytes are exactly what the envelope carries; recompute
	// them from the decoded payload so the assertion targets the signed form.
	env, err := fwsigning.DecodeEnvelope(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantCanon, err := fwsigning.CanonicalBytes(env.Approval)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	pub, err := signer.PublicKeyByID(ctx, key.KeyID, "pem-pkcs8")
	if err != nil {
		t.Fatalf("public key by id: %v", err)
	}

	svc := &fakeService{
		orderDetail:    reproDetail(id, attestationFromToken(t, token)),
		publicKeysByID: map[string]string{key.KeyID: pub},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, reproURL(id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	rawBody := rec.Body.String()
	m := bodyMap(t, rec.Result())

	// event body is present and addressed by the opaque event handle.
	eventDTO, _ := m["event"].(map[string]any)
	if eventDTO["id"] != reproEventID().String() {
		t.Errorf("want event id=%s, got %v", reproEventID(), eventDTO["id"])
	}
	if m["requestType"] != string(domain.AttestationRequestSubmit) {
		t.Errorf("want requestType=submit, got %v", m["requestType"])
	}

	// attestation envelope verbatim.
	attestation, _ := m["attestation"].(map[string]any)
	if attestation["signedEnvelope"] != token {
		t.Errorf("attestation envelope not verbatim:\n got %v\nwant %s", attestation["signedEnvelope"], token)
	}

	// request reproduces the bound submit request.
	request, ok := m["request"].(map[string]any)
	if !ok {
		t.Fatalf("want request object, got %T", m["request"])
	}
	if request["requestType"] != string(domain.AttestationRequestSubmit) {
		t.Errorf("want request.requestType=submit, got %v", request["requestType"])
	}
	if request["eventId"] != reproEventID().String() {
		t.Errorf("want request.eventId=%s, got %v", reproEventID(), request["eventId"])
	}

	// response.submitResponse reproduces the exact POST /orders/submit response.
	response, ok := m["response"].(map[string]any)
	if !ok {
		t.Fatalf("want response object, got %T", m["response"])
	}
	submit, ok := response["submitResponse"].(map[string]any)
	if !ok {
		t.Fatalf("want submitResponse object, got %T", response["submitResponse"])
	}
	if submit["signedApproval"] != token {
		t.Errorf("submitResponse signedApproval not verbatim: got %v", submit["signedApproval"])
	}
	if submit["keyId"] != key.KeyID {
		t.Errorf("want submitResponse keyId=%s, got %v", key.KeyID, submit["keyId"])
	}
	if submit["id"] != id.String() {
		t.Errorf("want submitResponse id=%s, got %v", id, submit["id"])
	}

	// canonicalApproval byte-identical to the signed bytes.
	if got, _ := m["canonicalApproval"].(string); got != string(wantCanon) {
		t.Errorf("canonicalApproval not byte-identical to signed bytes:\n got %q\nwant %q",
			got, string(wantCanon))
	}

	// signature matches the envelope signature.
	if got, _ := m["signature"].(string); got != env.Signature {
		t.Errorf("want signature=%s, got %v", env.Signature, m["signature"])
	}
	if env.Signature == "" {
		t.Fatal("test fixture is not actually signed")
	}

	// publicKey resolved by the envelope's keyId (rotation-safe seam exercised).
	publicKey, ok := m["publicKey"].(map[string]any)
	if !ok {
		t.Fatalf("want publicKey object, got %T", m["publicKey"])
	}
	if publicKey["keyId"] != key.KeyID {
		t.Errorf("want publicKey keyId=%s, got %v", key.KeyID, publicKey["keyId"])
	}
	if publicKey["key"] != pub {
		t.Errorf("want publicKey material=%q, got %v", pub, publicKey["key"])
	}
	if svc.keyByIDLast != key.KeyID {
		t.Errorf("public key must be resolved by the envelope keyId, got %q", svc.keyByIDLast)
	}

	// eSign facet reports a signed ed25519 event.
	esign, _ := m["eSign"].(map[string]any)
	if esign["alg"] != "ed25519" {
		t.Errorf("want eSign.alg=ed25519, got %v", esign["alg"])
	}
	if esign["signed"] != true {
		t.Errorf("want eSign.signed=true, got %v", esign["signed"])
	}

	// No private key material anywhere in the body.
	assertNoPrivateKeyBytes(t, rawBody, st)
}

func TestOrderReproduction_PreservesOrderedRejectsAndDetails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newReproSigningStore()
	signer, err := appsigning.New(st)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	key, err := signer.GenerateKey(ctx)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	publicKey, err := signer.PublicKeyByID(ctx, key.KeyID, "pem-pkcs8")
	if err != nil {
		t.Fatalf("public key: %v", err)
	}

	id := extID("repro-rejects")
	payload := reproPayload(id)
	payload.Verdict = "reject"
	payload.PolicySummary = "rejected"
	payload.EstimatePrice = ""
	payload.Rejects = []domain.OrderReject{
		{
			Code: "max_order_size", Scope: "order", Policy: "order-size",
			Reason: "order too large", Details: "qty=100,limit=10",
		},
		{
			Code: "rate_limit", Scope: "account", Policy: "rate-limit",
			Reason: "too many orders", Details: "count=11,limit=10",
		},
	}
	first := payload.Rejects[0]
	payload.RejectCode = first.Code
	payload.RejectScope = first.Scope
	payload.RejectPolicy = first.Policy
	payload.RejectReason = first.Reason
	payload.RejectDetails = first.Details
	token, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	detail := reproDetail(id, attestationFromToken(t, token))
	detail.Events[0].Type = domain.OrderEventPreTradeRejected
	svc := &fakeService{
		orderDetail: detail,
		publicKeysByID: map[string]string{
			key.KeyID: publicKey,
		},
	}
	router, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, reproURL(id), nil),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := bodyMap(t, rec.Result())
	request, ok := body["request"].(map[string]any)
	if !ok {
		t.Fatalf("request = %T, want object", body["request"])
	}
	assertReproducedRejects(t, request["rejects"], payload.Rejects)
	response, ok := body["response"].(map[string]any)
	if !ok {
		t.Fatalf("response = %T, want object", body["response"])
	}
	submit, ok := response["submitResponse"].(map[string]any)
	if !ok {
		t.Fatalf("submitResponse = %T, want object", response["submitResponse"])
	}
	assertReproducedRejects(t, submit["reasons"], payload.Rejects)
	canonical, _ := body["canonicalApproval"].(string)
	if !strings.Contains(canonical, `"details":"count=11,limit=10"`) {
		t.Fatalf("canonical approval lost second reject details: %s", canonical)
	}
}

func assertReproducedRejects(
	t *testing.T, raw any, want []domain.OrderReject,
) {
	t.Helper()
	got, ok := raw.([]any)
	if !ok || len(got) != len(want) {
		t.Fatalf("rejects = %#v, want %d entries", raw, len(want))
	}
	for i, expected := range want {
		reject, ok := got[i].(map[string]any)
		if !ok {
			t.Fatalf("reject[%d] = %T, want object", i, got[i])
		}
		if reject["code"] != expected.Code ||
			reject["scope"] != expected.Scope ||
			reject["policy"] != expected.Policy ||
			reject["reason"] != expected.Reason ||
			reject["details"] != expected.Details {
			t.Fatalf("reject[%d] = %#v, want %+v", i, reject, expected)
		}
	}
}

// TestOrderReproduction_RotatedKeyResolvesNonActive verifies that after the
// signing key rotates, an order signed under the earlier (now inactive) key
// resolves that earlier key's public material, not the active key.
func TestOrderReproduction_RotatedKeyResolvesNonActive(t *testing.T) {
	ctx := context.Background()
	st := newReproSigningStore()
	signer, err := appsigning.New(st)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	oldKey, err := signer.GenerateKey(ctx)
	if err != nil {
		t.Fatalf("generate old key: %v", err)
	}
	id := extID("repro-rot")
	token, err := signer.Sign(reproPayload(id))
	if err != nil {
		t.Fatalf("sign under old key: %v", err)
	}
	// Rotate: a fresh active key. The old key stays resolvable by id.
	newKey, err := signer.GenerateKey(ctx)
	if err != nil {
		t.Fatalf("rotate key: %v", err)
	}
	if newKey.KeyID == oldKey.KeyID {
		t.Fatal("rotation did not produce a new key id")
	}
	oldPub, err := signer.PublicKeyByID(ctx, oldKey.KeyID, "pem-pkcs8")
	if err != nil {
		t.Fatalf("resolve old public key: %v", err)
	}
	newPub, err := signer.PublicKeyByID(ctx, newKey.KeyID, "pem-pkcs8")
	if err != nil {
		t.Fatalf("resolve new public key: %v", err)
	}
	if oldPub == newPub {
		t.Fatal("rotated key shares public material with the old key")
	}

	svc := &fakeService{
		orderDetail: reproDetail(id, attestationFromToken(t, token)),
		publicKeysByID: map[string]string{
			oldKey.KeyID: oldPub,
			newKey.KeyID: newPub,
		},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, reproURL(id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	publicKey, _ := m["publicKey"].(map[string]any)
	if publicKey["keyId"] != oldKey.KeyID {
		t.Errorf("want the signing (old) key id %s, got %v", oldKey.KeyID, publicKey["keyId"])
	}
	if publicKey["key"] != oldPub {
		t.Error("reproduction resolved the active key, not the key that signed the envelope")
	}
	if svc.keyByIDLast != oldKey.KeyID {
		t.Errorf("want resolution by old key id, got %q", svc.keyByIDLast)
	}
}

// TestOrderReproduction_ESignOff verifies that an eSign-off order reports alg
// "none", a null publicKey and empty signature, while submitResponse and the
// verbatim envelope are still present.
func TestOrderReproduction_ESignOff(t *testing.T) {
	id := extID("repro-none")
	token, err := appsigning.SignNone(reproPayload(id))
	if err != nil {
		t.Fatalf("sign none: %v", err)
	}
	svc := &fakeService{
		noESign:     true,
		orderDetail: reproDetail(id, attestationFromToken(t, token)),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, reproURL(id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())

	if m["publicKey"] != nil {
		t.Errorf("want null publicKey under eSign-off, got %v", m["publicKey"])
	}
	if sig, _ := m["signature"].(string); sig != "" {
		t.Errorf("want empty signature under eSign-off, got %q", sig)
	}
	// The rotation-safe key lookup must never run for a "none" envelope.
	if svc.keyByIDLast != "" {
		t.Errorf("public key lookup must not run under alg none, got %q", svc.keyByIDLast)
	}
	esign, _ := m["eSign"].(map[string]any)
	if esign["alg"] != "none" {
		t.Errorf("want eSign.alg=none, got %v", esign["alg"])
	}
	if esign["noESign"] != true {
		t.Errorf("want eSign.noESign=true, got %v", esign["noESign"])
	}
	if esign["signed"] != false {
		t.Errorf("want eSign.signed=false, got %v", esign["signed"])
	}
	// response.submitResponse and the verbatim envelope are still present.
	response, ok := m["response"].(map[string]any)
	if !ok {
		t.Fatalf("want response present under eSign-off, got %T", m["response"])
	}
	submit, ok := response["submitResponse"].(map[string]any)
	if !ok {
		t.Fatalf("want submitResponse present under eSign-off, got %T", response["submitResponse"])
	}
	if submit["signedApproval"] != token {
		t.Errorf("submitResponse signedApproval not verbatim under eSign-off: got %v", submit["signedApproval"])
	}
	// canonicalApproval is still reproduced (binding+display) under eSign-off.
	if _, ok := m["canonicalApproval"].(string); !ok {
		t.Errorf("want canonicalApproval string under eSign-off, got %T", m["canonicalApproval"])
	}
}

// TestOrderReproduction_SignedResultFields proves an execution report preserves
// structured commission and account block policy in its reproduction bundle.
func TestOrderReproduction_SignedResultFields(t *testing.T) {
	ctx := context.Background()
	st := newReproSigningStore()
	signer, err := appsigning.New(st)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	key, err := signer.GenerateKey(ctx)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pub, err := signer.PublicKeyByID(ctx, key.KeyID, "pem-pkcs8")
	if err != nil {
		t.Fatalf("public key by id: %v", err)
	}

	id := extID("repro-result-fields")
	reportID := extID("repro-report-id")
	const blockPolicy = domain.PolicySpotFundsPnlBoundsKillSwitch
	payload := reproPayload(id)
	payload.RequestType = string(domain.AttestationRequestExecutionReport)
	payload.Result = &domain.AttestationResult{
		Outcome:        "applied",
		FillQuantity:   "3.5",
		FillPrice:      "150.20",
		FillLockPrice:  "150.25",
		Commission:     &domain.Commission{Amount: "-0.30", Currency: "USDT"},
		LeavesQuantity: "6.5",
		OrderStatus:    string(domain.OrderStatusPartiallyFilled),
		Blocks: []domain.AttestationBlock{{
			Account: "acc-1",
			Policy:  blockPolicy,
			Code:    "pnl_bound_breached",
			Reason:  "lower bound breached",
			Details: "realized=-1500,lower=-1000",
		}},
	}
	token, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	detail := reproDetail(id, attestationFromToken(t, token))
	detail.Events[0].Payload.ExecutionReport = &domain.ExecutionReportRequest{
		ExternalID: reportID,
	}
	svc := &fakeService{
		orderDetail:    detail,
		publicKeysByID: map[string]string{key.KeyID: pub},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, reproURL(id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	canon, _ := m["canonicalApproval"].(string)
	if canon == "" {
		t.Fatalf("want canonicalApproval string, got %T", m["canonicalApproval"])
	}
	// The signed bytes bind the structured commission (amount + currency).
	if !strings.Contains(canon, `"commission":{"amount":"-0.30","currency":"USDT"}`) {
		t.Fatalf("canonicalApproval missing structured commission:\n%s", canon)
	}
	request, ok := m["request"].(map[string]any)
	if !ok {
		t.Fatalf("want request object, got %T", m["request"])
	}
	result, ok := request["result"].(map[string]any)
	if !ok {
		t.Fatalf("want request.result object, got %T", request["result"])
	}
	blocks, ok := result["blocks"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("want one request.result block, got %#v", result["blocks"])
	}
	block, ok := blocks[0].(map[string]any)
	if !ok {
		t.Fatalf("want request.result block object, got %T", blocks[0])
	}
	if block["policy"] != blockPolicy {
		t.Fatalf("request.result block policy = %v, want %s",
			block["policy"], blockPolicy)
	}
	response, ok := m["response"].(map[string]any)
	if !ok {
		t.Fatalf("want response object, got %T", m["response"])
	}
	report, ok := response["executionReport"].(map[string]any)
	if !ok {
		t.Fatalf("want executionReport object, got %T", response["executionReport"])
	}
	if report["id"] != reportID.String() {
		t.Fatalf("executionReport.id = %v, want %s", report["id"], reportID)
	}
}

func TestOrderReproduction_StoredRequestTypes(t *testing.T) {
	handler, realm, eng, _ := newRealServiceRouterWithEngine(t)
	ctx := context.Background()
	seedRealAccountAndAssets(t, realm, "acc-1")
	if err := realm.SetSetting(ctx, domain.SettingKeyNoESign, "0"); err != nil {
		t.Fatal(err)
	}

	submit := func(handler http.Handler, id string) string {
		t.Helper()
		body := fmt.Sprintf(`{"id":%q,"account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1","price":"100","mode":"hold"}`, id)
		response := serveJSON(t, handler, http.MethodPost, "/orders/submit?missingAccount=reject", body, http.StatusCreated)
		return "/orders/" + response["id"].(string)
	}
	confirmed := submit(handler, "facet-confirm")
	serveJSON(t, handler, http.MethodPost, confirmed+"/confirm", "", http.StatusOK)
	report := serveJSON(t, handler, http.MethodPost, confirmed+"/execution-reports",
		`{"id":"facet-report","quantity":"1","price":"100","leavesQuantity":"0","status":"filled"}`, http.StatusCreated)
	cancelService, cancelRealm, cancelNode := newRealServiceWithEngine(t, officerengine.NewOpenPitEngineBuildFunc(), func(err error) {
		t.Errorf("unexpected fatal shutdown: %v", err)
	}, nil)
	seedReproductionAccount(t, cancelNode, cancelRealm)
	cancelHandler, err := newRouter(cancelService)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := submit(cancelHandler, "facet-cancel")
	serveJSON(t, cancelHandler, http.MethodPost, cancelled+"/cancel",
		`{"leavesQuantity":"1","reason":"operator cancelled"}`, http.StatusOK)

	eng.reviews = restReviewMarks()
	reviewCancelled := submit(handler, "facet-review-cancel")
	serveJSON(t, handler, http.MethodPost, reviewCancelled+"/review/cancel", "", http.StatusOK)
	reviewConfirmed := submit(handler, "facet-review-confirm")
	eng.reviews = nil
	serveJSON(t, handler, http.MethodPost, reviewConfirmed+"/review/confirm", "", http.StatusOK)
	eng.reviews = heldReviewMarks()
	reviewForced := submit(handler, "facet-review-force")
	eng.reviews = nil
	serveJSON(t, handler, http.MethodPost, reviewForced+"/review/force", "", http.StatusOK)

	facetsByRequest := map[domain.AttestationRequestType]string{
		domain.AttestationRequestSubmit:          "submitResponse",
		domain.AttestationRequestExecutionReport: "executionReport",
		domain.AttestationRequestConfirm:         "confirm",
		domain.AttestationRequestCancel:          "cancel",
		domain.AttestationRequestReviewConfirm:   "reviewConfirm",
		domain.AttestationRequestReviewForce:     "reviewForce",
		domain.AttestationRequestReviewCancel:    "reviewCancel",
	}
	type storedEvent struct {
		handler http.Handler
		path    string
		event   domain.OrderEvent
	}
	eventsByRequest := make(map[domain.AttestationRequestType][]storedEvent)
	for _, path := range []string{confirmed, cancelled, reviewCancelled, reviewConfirmed, reviewForced} {
		orderRealm, orderHandler := realm, handler
		if path == cancelled {
			orderRealm, orderHandler = cancelRealm, cancelHandler
		}
		id, err := domain.ParseExternalID(strings.TrimPrefix(path, "/orders/"))
		if err != nil {
			t.Fatal(err)
		}
		detail, err := orderRealm.GetOrder(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		assertStoredAttestationMode(t, detail, domain.SubmitModeHold)
		for _, event := range detail.Events {
			if event.Attestation != nil {
				requestType := event.Attestation.RequestType
				eventsByRequest[requestType] = append(eventsByRequest[requestType], storedEvent{orderHandler, path, event})
			}
		}
	}
	for _, seed := range schema.EnumDictionarySeeds() {
		if seed.Table != schema.AttestationRequestTypeTable {
			continue
		}
		if len(seed.Codes) == 0 {
			t.Fatal("attestation request type dictionary is empty")
		}
		for _, code := range seed.Codes {
			t.Run(code.Code, func(t *testing.T) {
				requestType := domain.AttestationRequestType(code.Code)
				facet := facetsByRequest[requestType]
				if facet == "" || len(eventsByRequest[requestType]) == 0 {
					t.Fatalf("no persisted event or expected facet for %s", requestType)
				}
				for _, entry := range eventsByRequest[requestType] {
					response := reproStoredFacet(t, entry.handler, entry.path, entry.event, facet)
					if requestType == domain.AttestationRequestExecutionReport && response["id"] != report["id"] {
						t.Fatalf("reproduced execution report id = %v, want %v", response["id"], report["id"])
					}
				}
			})
		}
		return
	}
	t.Fatal("attestation request type dictionary is missing")
}

func assertStoredAttestationMode(t *testing.T, detail domain.OrderDetail, mode string) {
	t.Helper()
	for _, event := range detail.Events {
		if att := event.Attestation; att != nil && att.Mode != mode {
			t.Errorf("order %s event %s (%s/%s) attestation mode = %q, want %q",
				detail.Order.ExternalID, event.ExternalID, event.Type, att.RequestType, att.Mode, mode)
		}
	}
}

func reproStoredFacet(
	t *testing.T, handler http.Handler, path string, event domain.OrderEvent, facet string,
) map[string]any {
	t.Helper()
	bundle := serveJSON(t, handler, http.MethodGet,
		path+"/events/"+event.ExternalID.String()+"/reproduction", "", http.StatusOK)
	if bundle["requestType"] != string(event.Attestation.RequestType) || bundle["reason"] != "" {
		t.Fatalf("%s reproduction has requestType %v and reason %q", event.Type, bundle["requestType"], bundle["reason"])
	}
	response, _ := bundle["response"].(map[string]any)
	result, ok := response[facet].(map[string]any)
	if len(response) != 1 || !ok {
		t.Fatalf("%s reproduction response = %v, want exactly %s", event.Type, bundle["response"], facet)
	}
	return result
}

type immediateEngineWithoutPersistenceStatus struct {
	engine.Engine
}

func (e immediateEngineWithoutPersistenceStatus) SettleImmediate(
	order domain.Order, prepared engine.ImmediatePreparation, postTrade pretrade.PostTradeResult,
) (engine.ImmediateResult, error) {
	result, err := e.Engine.SettleImmediate(order, prepared, postTrade)
	if err == nil && result.Persistence != nil {
		result.Persistence.OrderStatus = ""
	}
	return result, err
}

func seedReproductionAccount(t *testing.T, n node.Node, realm store.RealmStore) {
	t.Helper()
	ctx := context.Background()
	if err := realm.SetSetting(ctx, domain.SettingKeyNoESign, "0"); err != nil {
		t.Fatal(err)
	}
	caller := auth.SystemCaller()
	if _, err := n.CreateAccount(ctx, domain.Account{Code: "acc-1", Currency: "USD"}, caller); err != nil {
		t.Fatal(err)
	}
	if _, err := n.CreateAsset(ctx, domain.Asset{Code: "AAPL"}, caller); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ApplyAdjustment(ctx, "acc-1", "", domain.AdjustmentRequest{
		Type:              domain.AdjustmentTypeDeposit,
		Asset:             "USD",
		AverageEntryPrice: "1",
		RealizedPnl:       "0",
		Balance:           &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "1000"},
	}, domain.MissingAccountCreate, caller); err != nil {
		t.Fatal(err)
	}
}

type reproRecordingSigner struct {
	fwsigning.Service
	signed []domain.ApprovalPayload
}

func (s *reproRecordingSigner) Sign(payload domain.ApprovalPayload) (string, error) {
	s.signed = append(s.signed, payload)
	return s.Service.Sign(payload)
}

func TestImmediateSettlementRejectsMissingPersistenceOrderStatus(t *testing.T) {
	fatal := make(chan error, 2)
	recording := &reproRecordingSigner{}
	build := officerengine.NewOpenPitEngineBuildFunc()
	svc, realm, n := newRealServiceWithEngine(t, func(snap engine.Snapshot) (engine.Engine, error) {
		eng, err := build(snap)
		if err != nil {
			return nil, err
		}
		return immediateEngineWithoutPersistenceStatus{eng}, nil
	}, func(err error) { fatal <- err }, func(signer fwsigning.Service) fwsigning.Service {
		recording.Service = signer
		return recording
	})
	seedReproductionAccount(t, n, realm)
	ctx := auth.ContextWithCaller(context.Background(), auth.SystemCaller())
	const id = "missing-immediate-status"
	_, err := svc.SubmitOrder(ctx, domain.Order{
		ExternalID: id, Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: domain.OrderSideBuy, AmountKind: domain.OrderAmountKindQuantity,
		AmountValue: "1", Price: "100",
	}, backend.SubmitModeImmediate, domain.MissingAccountReject)
	for _, payload := range recording.signed {
		if payload.OrderExternalID == id &&
			payload.RequestType == string(domain.AttestationRequestExecutionReport) {
			t.Fatal("execution report with missing persistence order status reached signer")
		}
	}
	const reason = "immediate execution report persistence missing order status"
	if err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("SubmitOrder error = %v, want missing immediate persistence order status", err)
	}
	if !errors.Is(err, asyncengine.ErrChainRetryUnsafe) {
		t.Fatalf("SubmitOrder error = %v, want retry-unsafe after engine settlement", err)
	}
	if len(fatal) != 1 {
		t.Fatalf("fatal shutdown calls = %d, want 1", len(fatal))
	}
	if shutdown := <-fatal; !strings.Contains(shutdown.Error(), reason) {
		t.Fatalf("fatal shutdown error = %v, want %q", shutdown, reason)
	}
	if _, err := realm.GetOrder(ctx, id); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("order lookup after failed immediate submission = %v, want rolled-back order and history", err)
	}
}

func TestOrderReproduction_ImmediateExecutionReport(t *testing.T) {
	svc, realm, n := newRealServiceWithEngine(t, officerengine.NewOpenPitEngineBuildFunc(), func(err error) {
		t.Errorf("unexpected fatal shutdown: %v", err)
	}, nil)
	seedReproductionAccount(t, n, realm)
	handler, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	const id = "immediate-report-reproduction"
	const path = "/orders/" + id
	serveJSON(t, handler, http.MethodPost, "/orders/submit?missingAccount=reject",
		`{"id":"immediate-report-reproduction","account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1","price":"100","mode":"immediate"}`, http.StatusCreated)
	const lateReportID = "immediate-late-fill"
	serveJSON(t, handler, http.MethodPost, path+"/execution-reports",
		`{"id":"`+lateReportID+`","quantity":"1","price":"100","leavesQuantity":"0","status":"filled","force":true}`, http.StatusCreated)
	detail, err := realm.GetOrder(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	assertStoredAttestationMode(t, detail, domain.SubmitModeImmediate)
	var immediateFill, lateFill bool
	for _, event := range detail.Events {
		if event.Type != domain.OrderEventFill {
			continue
		}
		if event.Payload.ExecutionReport == nil || event.Payload.ExecutionReport.ExternalID.IsZero() ||
			event.Attestation == nil || event.Attestation.RequestType != domain.AttestationRequestExecutionReport {
			t.Fatalf("immediate fill lacks a persisted execution report or its attestation: %+v", event)
		}
		response := reproStoredFacet(t, handler, path, event, "executionReport")
		if want := event.Payload.ExecutionReport.ExternalID.String(); response["id"] != want {
			t.Fatalf("reproduced immediate report id = %v, want %s", response["id"], want)
		}
		if event.Payload.ExecutionReport.ExternalID == lateReportID {
			lateFill = true
		} else {
			immediateFill = true
		}
	}
	if !immediateFill || !lateFill {
		t.Fatalf("stored immediate fill = %t, stored later fill = %t, want both", immediateFill, lateFill)
	}
}

func TestOrderReproduction_ReviewSubmit(t *testing.T) {
	id := extID("repro-review-submit")
	payload := reproPayload(id)
	payload.Verdict = domain.VerdictReview
	payload.Reviews = restReviewMarks()
	token, err := fwsigning.BuildEnvelope(fwsigning.Envelope{Approval: payload})
	if err != nil {
		t.Fatal(err)
	}
	detail := reproDetail(id, attestationFromToken(t, token))
	router, err := newRouter(&fakeService{orderDetail: detail})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, reproURL(id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reproduction status=%d: %s", rec.Code, rec.Body.String())
	}
	submit := bodyMap(t, rec.Result())["response"].(map[string]any)["submitResponse"].(map[string]any)
	if submit["verdict"] != "review" || submit["signedApproval"] != token {
		t.Fatalf("review submit reproduction=%v", submit)
	}
	assertRESTReviewMarks(t, submit["reviews"])
}

func TestOrderReproduction_ReviewForce(t *testing.T) {
	for _, alg := range []string{fwsigning.AlgEd25519, fwsigning.AlgNone} {
		t.Run(alg, func(t *testing.T) {
			id := extID("repro-review-force")
			payload := reproPayload(id)
			payload.RequestType = string(domain.AttestationRequestReviewForce)
			payload.Verdict = domain.VerdictAccept
			payload.Result = nil
			token, err := fwsigning.BuildEnvelope(fwsigning.Envelope{Approval: payload})
			if err != nil {
				t.Fatal(err)
			}
			att := attestationFromToken(t, token)
			att.Alg, att.KeyID = alg, "recorded-key"
			detail := reproDetail(id, att)
			detail.Order.Status = domain.OrderStatusFilled
			detail.Order.Account, detail.DisplayPrice = "renamed", "150.25"
			router, err := newRouter(&fakeService{orderDetail: detail})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, reproURL(id), nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("force reproduction status=%d: %s", rec.Code, rec.Body.String())
			}
			facets := bodyMap(t, rec.Result())["response"].(map[string]any)
			decision := facets["reviewForce"].(map[string]any)
			if len(facets) != 1 || len(decision) != 5 || decision["verdict"] != "accept" ||
				decision["signedApproval"] != token || decision["keyId"] != att.KeyID ||
				decision["signed"] != (alg == fwsigning.AlgEd25519) || len(decision["reasons"].([]any)) != 0 {
				t.Fatalf("reviewForce=%v", facets)
			}
		})
	}
}

func TestOrderReproduction_ReviewConfirm(t *testing.T) {
	for _, verdict := range []domain.Verdict{domain.VerdictAccept, domain.VerdictReject} {
		for _, alg := range []string{fwsigning.AlgEd25519, fwsigning.AlgNone} {
			t.Run(string(verdict)+"/"+alg, func(t *testing.T) {
				id := extID("repro-review-confirm")
				payload := reproPayload(id)
				payload.RequestType = string(domain.AttestationRequestReviewConfirm)
				payload.Verdict = verdict
				payload.Result = nil
				if verdict == domain.VerdictReject {
					payload.Rejects = []domain.OrderReject{
						{Code: "funds", Scope: "account", Policy: "funds", Reason: "insufficient", Details: "first"},
						{Code: "rate", Scope: "account", Policy: "rate", Reason: "too frequent", Details: "second"},
					}
				}
				token, err := fwsigning.BuildEnvelope(fwsigning.Envelope{Approval: payload})
				if err != nil {
					t.Fatal(err)
				}
				att := attestationFromToken(t, token)
				att.Alg, att.KeyID = alg, "recorded-key"
				detail := reproDetail(id, att)
				// Later account renames, fills and fees must not rewrite the decision.
				detail.Order.Account, detail.Order.Status, detail.Order.Leaves = "renamed", domain.OrderStatusFilled, "0"
				detail.Order.CommissionSubtotals = []domain.Commission{{Amount: "1", Currency: "USD"}}
				detail.DisplayPrice = "150.25"
				reproduced, err := toEventReproductionResponseDTO(detail.Events[0], detail, att, payload)
				if err != nil {
					t.Fatal(err)
				}
				response := reproduced.ReviewConfirm
				if response == nil {
					t.Fatal("missing reviewConfirm facet")
				}
				if response.Verdict != string(verdict) ||
					response.SignedApproval != token || response.KeyID != att.KeyID ||
					response.Signed != (alg == fwsigning.AlgEd25519) {
					t.Fatalf("reviewConfirm=%+v", response)
				}
				wantReasons := append([]httpx.OrderRejectDTO{}, httpx.ToOrderRejectDTOs(payload.Rejects)...)
				if !reflect.DeepEqual(response.Reasons, wantReasons) {
					t.Fatalf("decision reasons=%+v; want %+v", response.Reasons, wantReasons)
				}
				raw, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				var decision map[string]any
				if err := json.Unmarshal(raw, &decision); err != nil {
					t.Fatal(err)
				}
				if len(decision) != 5 || decision["order"] != nil || decision["venueSend"] != nil {
					t.Fatalf("decision facet=%s; want only the signed decision", raw)
				}
			})
		}
	}
}

// TestOrderReproduction_ReviewRealService verifies, through the real service,
// that a review confirm's events reproduce the live signed decision and a
// review cancel's event reproduces the cancelled order.
func TestOrderReproduction_ReviewRealService(t *testing.T) {
	for _, verdict := range []domain.Verdict{domain.VerdictAccept, domain.VerdictReject} {
		t.Run(string(verdict), func(t *testing.T) {
			handler, realm, eng, _ := newRealServiceRouterWithEngine(t)
			ctx := context.Background()
			seedRealAccountAndAssets(t, realm, "acc-1")
			if err := realm.SetSetting(ctx, domain.SettingKeyNoESign, "0"); err != nil {
				t.Fatal(err)
			}
			eng.reviews = restReviewMarks()
			submit := serveJSON(t, handler, http.MethodPost, "/orders/submit?missingAccount=reject",
				`{"id":"real-repro-review","account":"acc-1","baseAsset":"AAPL","quoteAsset":"USD","side":"buy","amountKind":"quantity","amountValue":"1","price":"100","mode":"hold"}`, http.StatusCreated)
			if submit["verdict"] != "review" {
				t.Fatalf("submit=%v", submit)
			}
			id := domain.ExternalID(submit["id"].(string))
			path := "/orders/" + id.String()
			eng.reviews = nil
			if verdict == domain.VerdictReject {
				eng.rejects = []domain.OrderReject{{Code: "amount_missing", Scope: "order", Policy: "order_validation", Reason: "amount required"}}
			}
			confirm := serveJSON(t, handler, http.MethodPost, path+"/review/confirm", "", http.StatusOK)
			decision := confirm["decision"].(map[string]any)
			if decision["verdict"] != string(verdict) {
				t.Fatalf("confirm=%v", confirm)
			}
			if verdict == domain.VerdictReject {
				serveJSON(t, handler, http.MethodPost, path+"/review/cancel", "", http.StatusOK)
			}
			detail, err := realm.GetOrder(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[domain.OrderEventType]bool{}
			for _, event := range detail.Events {
				if event.Attestation == nil || (event.Attestation.RequestType != domain.AttestationRequestReviewConfirm &&
					event.Attestation.RequestType != domain.AttestationRequestReviewCancel) {
					continue
				}
				seen[event.Type] = true
				env, err := fwsigning.DecodeEnvelope(event.Attestation.SignedEnvelope)
				if err != nil {
					t.Fatal(err)
				}
				bundle := serveJSON(t, handler, http.MethodGet, path+"/events/"+event.ExternalID.String()+"/reproduction", "", http.StatusOK)
				response := bundle["response"].(map[string]any)
				if event.Attestation.RequestType == domain.AttestationRequestReviewCancel {
					if env.Approval.Result == nil || env.Approval.Result.OrderStatus != "cancelled" {
						t.Fatalf("cancel payload result=%+v", env.Approval.Result)
					}
					cancel := response["reviewCancel"].(map[string]any)
					if len(response) != 1 || cancel["order"].(map[string]any)["status"] != "cancelled" ||
						cancel["signedAttestation"] != event.Attestation.SignedEnvelope {
						t.Fatalf("cancel reproduction=%v", response)
					}
					continue
				}
				reproduced := response["reviewConfirm"].(map[string]any)
				if len(response) != 1 || len(reproduced) != 5 || reproduced["order"] != nil || reproduced["venueSend"] != nil ||
					reproduced["verdict"] != string(env.Approval.Verdict) || reproduced["keyId"] != event.Attestation.KeyID ||
					reproduced["signed"] != true || reproduced["signedApproval"] != event.Attestation.SignedEnvelope {
					t.Fatalf("%s reproduction=%v", event.Type, response)
				}
				if event.Type == domain.OrderEventPreTradeAccepted || event.Type == domain.OrderEventPreTradeRejected {
					if env.Approval.Result != nil || !reflect.DeepEqual(reproduced, decision) {
						t.Fatalf("decision payload result=%+v reproduction=%v; want %v", env.Approval.Result, reproduced, decision)
					}
				}
			}
			wantEvents := []domain.OrderEventType{domain.OrderEventPreTradeAccepted, domain.OrderEventCommitted}
			if verdict == domain.VerdictReject {
				wantEvents = []domain.OrderEventType{domain.OrderEventPreTradeRejected, domain.OrderEventCancelled}
			}
			for _, eventType := range wantEvents {
				if !seen[eventType] {
					t.Fatalf("missing reproduced %s event; got %v", eventType, seen)
				}
			}
		})
	}
}

// TestOrderReproduction_ConfirmOrderShape verifies the reproduced confirm order
// is the live response: the persisted order through the live serializer, with
// the account, status and open quantity attested for the confirm rather than
// the ones a later rename and fill left, and an empty commission subtotal
// array, not null.
func TestOrderReproduction_ConfirmOrderShape(t *testing.T) {
	ctx := context.Background()
	st := newReproSigningStore()
	signer, err := appsigning.New(st)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	key, err := signer.GenerateKey(ctx)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pub, err := signer.PublicKeyByID(ctx, key.KeyID, "pem-pkcs8")
	if err != nil {
		t.Fatalf("public key by id: %v", err)
	}

	id := extID("repro-confirm")
	payload := reproPayload(id)
	payload.RequestType = string(domain.AttestationRequestConfirm)
	payload.Result = &domain.AttestationResult{
		Outcome:        "committed",
		OrderStatus:    string(domain.OrderStatusCommitted),
		LeavesQuantity: "10",
		Blocks:         []domain.AttestationBlock{},
	}
	token, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// The order came from the API and the event from the panel; since the confirm
	// its account was renamed and a later fill moved it on.
	detail := reproDetail(id, attestationFromToken(t, token))
	detail.Order.Source = domain.SourceAPI
	detail.Order.Account = "acc-renamed"
	detail.Order.Principal = "operator"
	detail.Order.Status = domain.OrderStatusFilled
	detail.Order.Leaves = "0"
	detail.Order.CommissionSubtotals = []domain.Commission{{Amount: "1", Currency: "USD"}}
	detail.DisplayPrice = "150.25"
	svc := &fakeService{
		orderDetail:    detail,
		publicKeysByID: map[string]string{key.KeyID: pub},
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, reproURL(id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	response, ok := m["response"].(map[string]any)
	if !ok {
		t.Fatalf("want response object, got %T", m["response"])
	}
	confirm, ok := response["confirm"].(map[string]any)
	if !ok {
		t.Fatalf("want confirm object, got %T", response["confirm"])
	}
	order, ok := confirm["order"].(map[string]any)
	if !ok {
		t.Fatalf("want confirm.order object, got %T", confirm["order"])
	}
	for field, want := range map[string]string{
		"at":             detail.Order.At.Format(time.RFC3339Nano),
		"account":        "acc-1",
		"principal":      "operator",
		"baseAsset":      "AAPL",
		"quoteAsset":     "USD",
		"source":         string(domain.SourceAPI),
		"status":         string(domain.OrderStatusCommitted),
		"leavesQuantity": "10",
		"displayPrice":   "150.25",
	} {
		if order[field] != want {
			t.Fatalf("confirm.order.%s = %v, want %q", field, order[field], want)
		}
	}
	commissions, ok := order["commissionSubtotals"].([]any)
	if !ok {
		t.Fatalf(
			"want commissionSubtotals array, got %T (%v)",
			order["commissionSubtotals"],
			order["commissionSubtotals"],
		)
	}
	if len(commissions) != 0 {
		t.Fatalf("commissionSubtotals = %v, want empty array", commissions)
	}
}

// TestOrderReproduction_ConfirmSignedAsAnswered verifies the reproduced confirm
// order reports the signed flag the live answer carried: an unsigned confirm
// stays unsigned when a later event of the order is signed.
func TestOrderReproduction_ConfirmSignedAsAnswered(t *testing.T) {
	ctx := context.Background()
	signer, err := appsigning.New(newReproSigningStore())
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	if _, err := signer.GenerateKey(ctx); err != nil {
		t.Fatalf("generate key: %v", err)
	}

	id := extID("repro-unsigned")
	payload := reproPayload(id)
	payload.RequestType = string(domain.AttestationRequestConfirm)
	payload.Result = &domain.AttestationResult{
		Outcome:     "committed",
		OrderStatus: string(domain.OrderStatusCommitted),
		Blocks:      []domain.AttestationBlock{},
	}
	unsigned, err := appsigning.SignNone(payload)
	if err != nil {
		t.Fatalf("sign none: %v", err)
	}
	later, err := signer.Sign(reproPayload(id))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	detail := reproDetail(id, attestationFromToken(t, unsigned))
	detail.Events = append(detail.Events, domain.OrderEvent{
		At:          time.Now().UTC(),
		ExternalID:  extID("repro-later"),
		Order:       id,
		Type:        domain.OrderEventPreTradeAccepted,
		Source:      domain.SourceAPI,
		Attestation: attestationFromToken(t, later),
	})
	if !detail.Signed() {
		t.Fatal("the later event must sign the order as it stands now")
	}
	r, err := newRouter(&fakeService{orderDetail: detail})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, reproURL(id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	response, _ := bodyMap(t, rec.Result())["response"].(map[string]any)
	confirm, _ := response["confirm"].(map[string]any)
	order, ok := confirm["order"].(map[string]any)
	if !ok {
		t.Fatalf("want confirm.order object, got %v", response)
	}
	if order["signed"] != false {
		t.Fatalf("confirm.order.signed = %v, want false", order["signed"])
	}
}

// TestEventReproduction_NoAttestation verifies that an event with no persisted
// attestation yields null attestation/request/response/canonicalApproval/
// publicKey and a documented reason, while the event body is still present.
func TestEventReproduction_NoAttestation(t *testing.T) {
	id := extID("repro-bare")
	svc := &fakeService{
		orderDetail: reproDetail(id, nil),
	}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, reproURL(id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())
	eventDTO, _ := m["event"].(map[string]any)
	if eventDTO["id"] != reproEventID().String() {
		t.Errorf("want event id=%s, got %v", reproEventID(), eventDTO["id"])
	}
	for _, field := range []string{"attestation", "request", "response", "canonicalApproval", "publicKey"} {
		if v, present := m[field]; !present || v != nil {
			t.Errorf("want null %s when event has no attestation, got %v (present=%v)", field, v, present)
		}
	}
	if reason, _ := m["reason"].(string); reason == "" {
		t.Error("want a non-empty reason documenting the missing attestation")
	}
}

// TestEventReproduction_UndecodableEnvelope verifies the documented contract of
// an event whose persisted attestation envelope cannot be decoded: the
// attestation metadata and the event stay present, request, response,
// canonicalApproval and publicKey are null, signature is empty, no key lookup
// runs, and reason names the decode failure.
func TestEventReproduction_UndecodableEnvelope(t *testing.T) {
	id := extID("repro-undecodable")
	const envelope = "!not-a-base64url-envelope"
	att := &domain.EventAttestation{
		SignedEnvelope: envelope,
		KeyID:          "key-1",
		Alg:            fwsigning.AlgEd25519,
		RequestType:    domain.AttestationRequestSubmit,
		Mode:           "immediate",
		IssuedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}
	svc := &fakeService{orderDetail: reproDetail(id, att)}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, reproURL(id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	m := bodyMap(t, rec.Result())

	eventDTO, _ := m["event"].(map[string]any)
	if eventDTO["id"] != reproEventID().String() {
		t.Errorf("want event id=%s, got %v", reproEventID(), eventDTO["id"])
	}
	if m["requestType"] != string(domain.AttestationRequestSubmit) {
		t.Errorf("want requestType=submit, got %v", m["requestType"])
	}
	attestation, ok := m["attestation"].(map[string]any)
	if !ok {
		t.Fatalf("want attestation present on decode failure, got %T", m["attestation"])
	}
	if attestation["signedEnvelope"] != envelope {
		t.Errorf("want attestation.signedEnvelope verbatim, got %v", attestation["signedEnvelope"])
	}
	for _, field := range []string{"request", "response", "canonicalApproval", "publicKey"} {
		if v, present := m[field]; !present || v != nil {
			t.Errorf("want null %s when the envelope cannot be decoded, got %v (present=%v)", field, v, present)
		}
	}
	if sig, present := m["signature"]; !present || sig != "" {
		t.Errorf("want empty signature when the envelope cannot be decoded, got %v (present=%v)", sig, present)
	}
	if svc.keyByIDLast != "" {
		t.Errorf("public key lookup must not run for an undecodable envelope, got %q", svc.keyByIDLast)
	}
	esign, _ := m["eSign"].(map[string]any)
	if esign["alg"] != fwsigning.AlgEd25519 {
		t.Errorf("want eSign.alg=%s from the attestation metadata, got %v", fwsigning.AlgEd25519, esign["alg"])
	}
	if esign["signed"] != true {
		t.Errorf("want eSign.signed=true from the attestation metadata, got %v", esign["signed"])
	}
	if reason, _ := m["reason"].(string); !strings.Contains(reason, "could not be decoded") {
		t.Errorf("want reason naming the decode failure, got %q", reason)
	}
}

// TestEventReproduction_OrderNotFound verifies an unknown order external id
// surfaces the store's not-found as a 404.
func TestEventReproduction_OrderNotFound(t *testing.T) {
	svc := &fakeService{stateErr: domain.ErrNotFound}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, reproURL(extID("missing")), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestEventReproduction_UnknownEventID verifies that an order that exists but has
// no event with the requested id yields a 404.
func TestEventReproduction_UnknownEventID(t *testing.T) {
	id := extID("repro-order")
	svc := &fakeService{orderDetail: reproDetail(id, nil)}
	r, err := newRouter(svc)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/orders/"+id.String()+"/events/"+extID("no-such-event").String()+"/reproduction", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// assertNoPrivateKeyBytes fails if any private-key seed held by the store appears
// anywhere in the response body, in base64 or hex form.
func assertNoPrivateKeyBytes(t *testing.T, body string, st *reproSigningStore) {
	t.Helper()
	for _, k := range st.keys {
		if len(k.PrivateKey) == 0 {
			continue
		}
		for _, enc := range []string{
			base64.StdEncoding.EncodeToString(k.PrivateKey),
			base64.RawURLEncoding.EncodeToString(k.PrivateKey),
			hex.EncodeToString(k.PrivateKey),
		} {
			if enc != "" && strings.Contains(body, enc) {
				t.Fatalf("response leaked private key material (%d-byte seed)", len(k.PrivateKey))
			}
		}
	}
}
