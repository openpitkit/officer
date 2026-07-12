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
	"context"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	appsigning "go.openpit.dev/officer/internal/signing"
)

// reproSigningStore is a minimal in-memory appsigning.Store for building real
// signed tokens in reproduction tests. It lets a key be rotated (a later active
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

func (s *reproSigningStore) GetSigningConfig(_ context.Context, key string) (string, bool, error) {
	v, ok := s.config[key]
	return v, ok, nil
}

func (s *reproSigningStore) SetSigningConfig(_ context.Context, key, value string) error {
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
		EstimateSource:  "limit",
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

// attestationFromToken decodes a real token into the persisted EventAttestation
// shape the store would hold, so the fake service can serve it verbatim.
func attestationFromToken(t *testing.T, token string) *domain.EventAttestation {
	t.Helper()
	env, err := appsigning.DecodeEnvelope(token)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return &domain.EventAttestation{
		Token:       token,
		KeyID:       env.KeyID,
		Alg:         env.Approval.Alg,
		RequestType: domain.AttestationRequestType(env.Approval.RequestType),
		Mode:        env.Approval.Mode,
		IssuedAt:    env.Approval.IssuedAt,
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
// reproduces a signed order's token verbatim, its canonical bytes byte-identical
// to what was signed, its signature, and resolves the public key by the token's
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
	env, err := appsigning.DecodeEnvelope(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantCanon, err := appsigning.CanonicalBytes(env.Approval)
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
	if eventDTO["externalId"] != reproEventID().String() {
		t.Errorf("want event externalId=%s, got %v", reproEventID(), eventDTO["externalId"])
	}
	if m["requestType"] != string(domain.AttestationRequestSubmit) {
		t.Errorf("want requestType=submit, got %v", m["requestType"])
	}

	// attestation token verbatim.
	attestation, _ := m["attestation"].(map[string]any)
	if attestation["token"] != token {
		t.Errorf("attestation token not verbatim:\n got %v\nwant %s", attestation["token"], token)
	}

	// request reproduces the bound submit request.
	request, ok := m["request"].(map[string]any)
	if !ok {
		t.Fatalf("want request object, got %T", m["request"])
	}
	if request["requestType"] != string(domain.AttestationRequestSubmit) {
		t.Errorf("want request.requestType=submit, got %v", request["requestType"])
	}
	if request["eventExternalId"] != reproEventID().String() {
		t.Errorf("want request.eventExternalId=%s, got %v", reproEventID(), request["eventExternalId"])
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
	if submit["token"] != token {
		t.Errorf("submitResponse token not verbatim: got %v", submit["token"])
	}
	if submit["keyId"] != key.KeyID {
		t.Errorf("want submitResponse keyId=%s, got %v", key.KeyID, submit["keyId"])
	}
	if submit["orderExternalId"] != id.String() {
		t.Errorf("want submitResponse orderExternalId=%s, got %v", id, submit["orderExternalId"])
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

	// publicKey resolved by the token's keyId (rotation-safe seam exercised).
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
		t.Errorf("public key must be resolved by the token keyId, got %q", svc.keyByIDLast)
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
		t.Error("reproduction resolved the active key, not the key that signed the token")
	}
	if svc.keyByIDLast != oldKey.KeyID {
		t.Errorf("want resolution by old key id, got %q", svc.keyByIDLast)
	}
}

// TestOrderReproduction_ESignOff verifies that an eSign-off order reports alg
// "none", a null publicKey and empty signature, while submitResponse and the
// verbatim token are still present.
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
	// response.submitResponse and the verbatim token are still present.
	response, ok := m["response"].(map[string]any)
	if !ok {
		t.Fatalf("want response present under eSign-off, got %T", m["response"])
	}
	submit, ok := response["submitResponse"].(map[string]any)
	if !ok {
		t.Fatalf("want submitResponse present under eSign-off, got %T", response["submitResponse"])
	}
	if submit["token"] != token {
		t.Errorf("submitResponse token not verbatim under eSign-off: got %v", submit["token"])
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
}

func TestOrderReproduction_ConfirmCommissionSubtotalsArray(t *testing.T) {
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
		Outcome:     "committed",
		OrderStatus: string(domain.OrderStatusCommitted),
		Blocks:      []domain.AttestationBlock{},
	}
	token, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
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
	if eventDTO["externalId"] != reproEventID().String() {
		t.Errorf("want event externalId=%s, got %v", reproEventID(), eventDTO["externalId"])
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
