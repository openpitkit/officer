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

package signing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	cssh "golang.org/x/crypto/ssh"

	"go.openpit.dev/officer/framework/domain"
	fwsigning "go.openpit.dev/officer/framework/signing"
)

var _ fwsigning.Service = (*Service)(nil)

// fakeStore is an in-memory Store for signing tests; no sqlite needed.
type fakeStore struct {
	keys   map[string]domain.SigningKey
	config map[string]string
}

func newFakeStore() *fakeStore {
	return &fakeStore{keys: map[string]domain.SigningKey{}, config: map[string]string{}}
}

func (f *fakeStore) UpsertSigningKey(_ context.Context, key domain.SigningKey) error {
	f.keys[key.KeyID] = key
	return nil
}

func (f *fakeStore) GetActiveSigningKey(_ context.Context) (domain.SigningKey, bool, error) {
	for _, k := range f.keys {
		if k.Active {
			return k, true, nil
		}
	}
	return domain.SigningKey{}, false, nil
}

func (f *fakeStore) GetSigningKey(_ context.Context, keyID string) (domain.SigningKey, error) {
	k, ok := f.keys[keyID]
	if !ok {
		return domain.SigningKey{}, domain.ErrNotFound
	}
	return k, nil
}

func (f *fakeStore) ListSigningKeys(_ context.Context) ([]domain.SigningKey, error) {
	out := make([]domain.SigningKey, 0, len(f.keys))
	for _, k := range f.keys {
		k.PrivateKey = nil // store contract: ListSigningKeys omits private half
		out = append(out, k)
	}
	return out, nil
}

func (f *fakeStore) DeactivateAllSigningKeys(_ context.Context) error {
	for id, k := range f.keys {
		k.Active = false
		f.keys[id] = k
	}
	return nil
}

func (f *fakeStore) GetSigningConfig(_ context.Context, key string) (string, bool, error) {
	v, ok := f.config[key]
	return v, ok, nil
}

func (f *fakeStore) SetSigningConfig(_ context.Context, key, value string) error {
	f.config[key] = value
	return nil
}

type recordingReplayGuard struct {
	ctx        context.Context
	approvalID string
	nonce      string
	err        error
}

func (g *recordingReplayGuard) Record(ctx context.Context, approvalID, nonce string) error {
	g.ctx = ctx
	g.approvalID = approvalID
	g.nonce = nonce
	return g.err
}

// samplePayload builds a representative accepted approval payload.
func samplePayload() domain.ApprovalPayload {
	now := time.Now().UTC()
	return domain.ApprovalPayload{
		Version:         1,
		ApprovalID:      "11111111-1111-1111-1111-111111111111",
		Mode:            "hold",
		OrderExternalID: "AAAAAAAAAAAAAAAAAAAAAA", // 22-char base64url ExternalID handle
		Instrument:      "AAPL/USD",
		Side:            "buy",
		Quantity:        "10",
		AmountKind:      "quantity",
		OrderType:       "limit",
		LimitPrice:      "150.25",
		PriceCurrency:   "USD",
		TimeInForce:     "gtc",
		AccountID:       "acct-1",
		Verdict:         "accept",
		PolicySummary:   "ok",
		EstimatePrice:   "150.25",
		IssuedAt:        now.Format(time.RFC3339Nano),
		Nonce:           "Zm9vYmFyYmF6cXV4MTIzNA",
	}
}

// expectFor builds the matching VerifyParams for a payload.
func expectFor(p domain.ApprovalPayload) fwsigning.VerifyParams {
	return fwsigning.VerifyParams{
		OrderExternalID: p.OrderExternalID,
		Instrument:      p.Instrument,
		Venue:           p.Venue,
		Side:            p.Side,
		Quantity:        p.Quantity,
		AmountKind:      p.AmountKind,
		OrderType:       p.OrderType,
		LimitPrice:      p.LimitPrice,
		PriceCurrency:   p.PriceCurrency,
		TimeInForce:     p.TimeInForce,
		AccountID:       p.AccountID,
		AccountGroupID:  p.AccountGroupID,
	}
}

func newServiceWithKey(t *testing.T) (*Service, *fakeStore) {
	t.Helper()
	st := newFakeStore()
	svc, err := New(st, NewMemoryReplayGuard())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := svc.GenerateKey(context.Background()); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return svc, st
}

func TestNewRejectsNilReplayGuard(t *testing.T) {
	if _, err := New(newFakeStore(), nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("New with nil replay guard = %v, want ErrInvalid", err)
	}
}

func TestMemoryReplayGuardRejectsReplay(t *testing.T) {
	guard := NewMemoryReplayGuard()
	ctx := context.Background()
	if err := guard.Record(ctx, "approval-1", "nonce-1"); err != nil {
		t.Fatalf("first Record: %v", err)
	}
	err := guard.Record(ctx, "approval-1", "nonce-1")
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second Record = %v, want ErrConflict", err)
	}
	want := `signing: nonce replay for approval "approval-1": conflict`
	if err.Error() != want {
		t.Fatalf("replay error = %q, want %q", err, want)
	}
}

func TestMemoryReplayGuardEvictsOldest(t *testing.T) {
	guard := NewMemoryReplayGuard()
	ctx := context.Background()
	for i := range maxUsedNonces {
		if err := guard.Record(ctx, "approval", strconv.Itoa(i)); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}
	if err := guard.Record(ctx, "approval", "overflow"); err != nil {
		t.Fatalf("Record overflow: %v", err)
	}
	if err := guard.Record(ctx, "approval", "1"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second-oldest Record = %v, want ErrConflict", err)
	}
	if err := guard.Record(ctx, "approval", "0"); err != nil {
		t.Fatalf("evicted oldest Record: %v", err)
	}
}

func TestVerifyImmediateUsesInjectedReplayGuard(t *testing.T) {
	guardErr := errors.New("guard unavailable")
	guard := &recordingReplayGuard{err: guardErr}
	st := newFakeStore()
	svc, err := New(st, guard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := svc.GenerateKey(context.Background()); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	p := samplePayload()
	p.Mode = "immediate"
	token, err := svc.Sign(p)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "verify")
	if _, err := svc.Verify(ctx, token, expectFor(p)); !errors.Is(err, guardErr) {
		t.Errorf("Verify = %v, want guard error", err)
	}
	if guard.ctx == nil || guard.ctx.Value(contextKey{}) != "verify" {
		t.Errorf("guard did not receive Verify context")
	}
	if guard.approvalID != p.ApprovalID || guard.nonce != p.Nonce {
		t.Errorf("guard pair = (%q, %q), want (%q, %q)", guard.approvalID, guard.nonce, p.ApprovalID, p.Nonce)
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	svc, _ := newServiceWithKey(t)
	p := samplePayload()
	token, err := svc.Sign(p)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	res, err := svc.Verify(context.Background(), token, expectFor(p))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.Signed {
		t.Fatalf("expected Signed=true")
	}
	if res.Payload.Alg != fwsigning.AlgEd25519 {
		t.Fatalf("alg = %q, want ed25519", res.Payload.Alg)
	}
	if res.Payload.KeyID == "" {
		t.Fatalf("keyId not stamped")
	}
}

func TestSignVerifyRejectRoundTrip(t *testing.T) {
	svc, _ := newServiceWithKey(t)
	p := samplePayload()
	p.Verdict = "reject"
	p.PolicySummary = "rejected"
	p.EstimatePrice = ""
	p.RejectCode = "insufficient_funds"
	p.RejectScope = "account"
	p.RejectPolicy = "spot_funds"
	p.RejectReason = "available funds below required amount"
	p.RejectDetails = "available=5,required=10"
	p.Rejects = []domain.OrderReject{
		{
			Code: "insufficient_funds", Scope: "account",
			Policy: "spot_funds", Reason: p.RejectReason,
			Details: p.RejectDetails,
		},
		{
			Code: "rate_limit", Scope: "account",
			Policy: "rate_limit", Reason: "too many orders",
			Details: "count=11,limit=10",
		},
	}

	token, err := svc.Sign(p)
	if err != nil {
		t.Fatalf("Sign reject: %v", err)
	}
	res, err := svc.Verify(context.Background(), token, expectFor(p))
	if err != nil {
		t.Fatalf("Verify reject: %v", err)
	}
	if !res.Signed || res.Payload.Verdict != "reject" ||
		res.Payload.RejectCode != "insufficient_funds" ||
		!reflect.DeepEqual(res.Payload.Rejects, p.Rejects) {
		t.Fatalf("reject verify result = %+v", res)
	}
}

func TestVerifyTamperedByteFails(t *testing.T) {
	svc, _ := newServiceWithKey(t)
	p := samplePayload()
	token, err := svc.Sign(p)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	env, err := fwsigning.DecodeEnvelope(token)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	env.Approval.Quantity = "11" // mutate signed content but keep signature
	tampered, err := fwsigning.BuildEnvelope(env)
	if err != nil {
		t.Fatalf("BuildEnvelope: %v", err)
	}
	exp := expectFor(env.Approval) // expect matches the tampered field too
	if _, err := svc.Verify(context.Background(), tampered, exp); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for tampered byte, got %v", err)
	}
}

func TestVerifyUnknownKeyIDFails(t *testing.T) {
	svc, _ := newServiceWithKey(t)
	p := samplePayload()
	token, err := svc.Sign(p)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	env, _ := fwsigning.DecodeEnvelope(token)
	env.Approval.KeyID = "does-not-exist"
	env.KeyID = "does-not-exist"
	bad, _ := fwsigning.BuildEnvelope(env)
	if _, err := svc.Verify(context.Background(), bad, expectFor(p)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for unknown keyId, got %v", err)
	}
}

func TestVerifyWrongKeyFails(t *testing.T) {
	svc, _ := newServiceWithKey(t)
	p := samplePayload()
	token, err := svc.Sign(p)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// Rotate to a new active key; the old keyId is still resolvable but its
	// public key no longer matches a signature made under a different key.
	env, _ := fwsigning.DecodeEnvelope(token)
	if _, err := svc.GenerateKey(context.Background()); err != nil {
		t.Fatalf("GenerateKey rotate: %v", err)
	}
	// Re-sign payload bytes with the NEW key but keep the OLD keyId binding.
	resigned, err := svc.Sign(p)
	if err != nil {
		t.Fatalf("Sign with new key: %v", err)
	}
	newEnv, _ := fwsigning.DecodeEnvelope(resigned)
	newEnv.Approval.KeyID = env.Approval.KeyID // bind to old key
	newEnv.KeyID = env.KeyID
	mismatched, _ := fwsigning.BuildEnvelope(newEnv)
	if _, err := svc.Verify(context.Background(), mismatched, expectFor(p)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for wrong-key signature, got %v", err)
	}
}

// TestPublicKeyByID_ResolvesRotatedKey verifies PublicKeyByID resolves a key by
// id after rotation: the earlier (now inactive) key still yields its own public
// material, distinct from the active key's, and matches ActivePublicKey while it
// was active.
func TestPublicKeyByID_ResolvesRotatedKey(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	svc, err := New(st, NewMemoryReplayGuard())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	oldKey, err := svc.GenerateKey(ctx)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	oldActive, err := svc.ActivePublicKey(FormatPEMPKCS8)
	if err != nil {
		t.Fatalf("ActivePublicKey: %v", err)
	}
	oldByID, err := svc.PublicKeyByID(ctx, oldKey.KeyID, FormatPEMPKCS8)
	if err != nil {
		t.Fatalf("PublicKeyByID old: %v", err)
	}
	if oldByID != oldActive {
		t.Fatalf("PublicKeyByID must match ActivePublicKey while active")
	}
	newKey, err := svc.GenerateKey(ctx)
	if err != nil {
		t.Fatalf("GenerateKey rotate: %v", err)
	}
	// After rotation, the OLD key must still resolve to its OWN public material.
	afterByID, err := svc.PublicKeyByID(ctx, oldKey.KeyID, FormatPEMPKCS8)
	if err != nil {
		t.Fatalf("PublicKeyByID after rotation: %v", err)
	}
	if afterByID != oldByID {
		t.Fatalf("rotated key's public material changed under its id")
	}
	newByID, err := svc.PublicKeyByID(ctx, newKey.KeyID, FormatPEMPKCS8)
	if err != nil {
		t.Fatalf("PublicKeyByID new: %v", err)
	}
	if newByID == oldByID {
		t.Fatalf("new key shares public material with the old key")
	}
	if got, err := svc.ActivePublicKey(FormatPEMPKCS8); err != nil || got != newByID {
		t.Fatalf("active key should now be the new key, got %q err %v", got, err)
	}
}

// TestPublicKeyByID_UnknownAndEmpty verifies PublicKeyByID rejects an unknown id
// with ErrNotFound and an empty id with ErrInvalid, and never emits private
// material.
func TestPublicKeyByID_UnknownAndEmpty(t *testing.T) {
	svc, _ := newServiceWithKey(t)
	if _, err := svc.PublicKeyByID(context.Background(), "nope", FormatPEMPKCS8); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound for unknown id, got %v", err)
	}
	if _, err := svc.PublicKeyByID(context.Background(), "", FormatPEMPKCS8); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty id, got %v", err)
	}
}

func TestParamBindingTamperRejected(t *testing.T) {
	svc, _ := newServiceWithKey(t)
	p := samplePayload()
	token, err := svc.Sign(p)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	mutators := map[string]func(*fwsigning.VerifyParams){
		"instrument":    func(e *fwsigning.VerifyParams) { e.Instrument = "MSFT/USD" },
		"side":          func(e *fwsigning.VerifyParams) { e.Side = "sell" },
		"quantity":      func(e *fwsigning.VerifyParams) { e.Quantity = "99" },
		"amountKind":    func(e *fwsigning.VerifyParams) { e.AmountKind = "volume" },
		"orderType":     func(e *fwsigning.VerifyParams) { e.OrderType = "market" },
		"limitPrice":    func(e *fwsigning.VerifyParams) { e.LimitPrice = "999.99" },
		"priceCurrency": func(e *fwsigning.VerifyParams) { e.PriceCurrency = "EUR" },
		"timeInForce":   func(e *fwsigning.VerifyParams) { e.TimeInForce = "ioc" },
		"accountId":     func(e *fwsigning.VerifyParams) { e.AccountID = "acct-2" },
		"orderId":       func(e *fwsigning.VerifyParams) { e.OrderExternalID = "BBBBBBBBBBBBBBBBBBBBBB" },
	}
	for name, mut := range mutators {
		exp := expectFor(p)
		mut(&exp)
		if _, err := svc.Verify(context.Background(), token, exp); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("field %s: expected re-bind rejection, got %v", name, err)
		}
	}
}

func TestSignVerifyEmptyOrderExternalID(t *testing.T) {
	// The generic payload permits a request without an order handle and must still
	// sign and verify it consistently.
	svc, _ := newServiceWithKey(t)
	p := samplePayload()
	p.OrderExternalID = ""
	token, err := svc.Sign(p)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	res, err := svc.Verify(context.Background(), token, expectFor(p))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Payload.OrderExternalID != "" {
		t.Fatalf("orderId = %q, want empty", res.Payload.OrderExternalID)
	}
	// An empty expectation leaves the bound order handle unchecked; a populated
	// payload handle verifies against an empty expectation (no surrogate to leak).
	if _, err := svc.Verify(context.Background(), token, expectFor(p)); err != nil {
		t.Fatalf("Verify empty handle: %v", err)
	}
}

func TestVerifyOrderExternalIDMismatchRejected(t *testing.T) {
	svc, _ := newServiceWithKey(t)
	p := samplePayload()
	token, err := svc.Sign(p)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	exp := expectFor(p)
	exp.OrderExternalID = "BBBBBBBBBBBBBBBBBBBBBB"
	if _, err := svc.Verify(context.Background(), token, exp); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Verify mismatch = %v, want ErrInvalid", err)
	}
}

func TestVerifyEmptyPayloadOrderExternalIDRejectedForCreatedOrder(t *testing.T) {
	svc, _ := newServiceWithKey(t)
	p := samplePayload()
	p.OrderExternalID = ""
	token, err := svc.Sign(p)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	exp := expectFor(p)
	exp.OrderExternalID = "AAAAAAAAAAAAAAAAAAAAAA"
	if _, err := svc.Verify(context.Background(), token, exp); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Verify empty payload order id = %v, want ErrInvalid", err)
	}
}

func TestCanonicalBytesNoSurrogateAndCarriesHandle(t *testing.T) {
	// The canonical bytes must carry the opaque order handle, never an integer
	// surrogate id field.
	p := samplePayload()
	canon, err := fwsigning.CanonicalBytes(p)
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	s := string(canon)
	if !strings.Contains(s, `"orderId":"`+p.OrderExternalID+`"`) {
		t.Fatalf("canonical bytes missing order handle: %s", s)
	}
	if strings.Contains(s, `"orderExternalId"`) {
		t.Fatalf("canonical bytes leaked internal identity mnemonic: %s", s)
	}
	// An empty handle is omitted entirely (omitempty), keeping the envelope free
	// of any order reference.
	p.OrderExternalID = ""
	canonEmpty, err := fwsigning.CanonicalBytes(p)
	if err != nil {
		t.Fatalf("CanonicalBytes empty: %v", err)
	}
	if strings.Contains(string(canonEmpty), `"orderId"`) {
		t.Fatalf("empty order handle should be omitted: %s", canonEmpty)
	}
}

func TestCanonicalBytesLegacyPayloadWithoutPrincipalRemainStable(t *testing.T) {
	payload := domain.ApprovalPayload{
		Version:       1,
		ApprovalID:    "legacy-approval",
		Mode:          "hold",
		Instrument:    "AAPL/USD",
		Side:          "buy",
		Quantity:      "1",
		AmountKind:    "quantity",
		OrderType:     "limit",
		LimitPrice:    "10",
		PriceCurrency: "USD",
		AccountID:     "acc-1",
		Verdict:       "accept",
		PolicySummary: "accepted",
		EstimatePrice: "10",
		IssuedAt:      "2026-01-02T03:04:05Z",
		Nonce:         "legacy-nonce",
	}
	got, err := fwsigning.CanonicalBytes(payload)
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	const want = `{"version":1,"approvalId":"legacy-approval","mode":"hold",` +
		`"instrument":"AAPL/USD","side":"buy","quantity":"1",` +
		`"amountKind":"quantity","orderType":"limit","limitPrice":"10",` +
		`"priceCurrency":"USD","timeInForce":"","accountId":"acc-1",` +
		`"verdict":"accept","policySummary":"accepted",` +
		`"estimatePrice":"10","issuedAt":"2026-01-02T03:04:05Z",` +
		`"nonce":"legacy-nonce","keyId":"","alg":""}`
	if string(got) != want {
		t.Fatalf("legacy canonical bytes changed:\n got %s\nwant %s", got, want)
	}
}

func TestCanonicalBytesLegacyRejectWithoutRejectListRemainStable(t *testing.T) {
	payload := domain.ApprovalPayload{
		Version:       1,
		ApprovalID:    "legacy-reject",
		Mode:          "hold",
		Instrument:    "AAPL/USD",
		Side:          "buy",
		Quantity:      "1",
		AmountKind:    "quantity",
		OrderType:     "limit",
		LimitPrice:    "10",
		PriceCurrency: "USD",
		AccountID:     "acc-1",
		Verdict:       "reject",
		PolicySummary: "rejected",
		IssuedAt:      "2026-01-02T03:04:05Z",
		Nonce:         "legacy-nonce",
		RejectCode:    "insufficient_funds",
		RejectScope:   "account",
		RejectPolicy:  "spot_funds",
		RejectReason:  "available funds below required amount",
		RejectDetails: "available=5,required=10",
	}
	got, err := fwsigning.CanonicalBytes(payload)
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	const want = `{"version":1,"approvalId":"legacy-reject","mode":"hold",` +
		`"instrument":"AAPL/USD","side":"buy","quantity":"1",` +
		`"amountKind":"quantity","orderType":"limit","limitPrice":"10",` +
		`"priceCurrency":"USD","timeInForce":"","accountId":"acc-1",` +
		`"verdict":"reject","policySummary":"rejected","estimatePrice":"",` +
		`"issuedAt":"2026-01-02T03:04:05Z","nonce":"legacy-nonce",` +
		`"keyId":"","alg":"","rejectCode":"insufficient_funds",` +
		`"rejectScope":"account","rejectPolicy":"spot_funds",` +
		`"rejectReason":"available funds below required amount",` +
		`"rejectDetails":"available=5,required=10"}`
	if string(got) != want {
		t.Fatalf("legacy reject canonical bytes changed:\n got %s\nwant %s", got, want)
	}
}

func TestVerifyRejectsNoneAlgWhenESignEnabled(t *testing.T) {
	svc, _ := newServiceWithKey(t)
	p := samplePayload()
	token, err := svc.SignNone(p)
	if err != nil {
		t.Fatalf("SignNone: %v", err)
	}
	if _, err := svc.Verify(context.Background(), token, expectFor(p)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for alg none while eSign enabled, got %v", err)
	}
}

func TestVerifyRejectsUnsupportedPayloadVersion(t *testing.T) {
	svc, _ := newServiceWithKey(t)
	p := samplePayload()
	p.Version = 2
	token, err := svc.Sign(p)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := svc.Verify(context.Background(), token, expectFor(p)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for unsupported payload version, got %v", err)
	}
}

func TestVerifyImmediateNonceReplayRejected(t *testing.T) {
	svc, _ := newServiceWithKey(t)
	p := samplePayload()
	p.Mode = "immediate"
	token, err := svc.Sign(p)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := svc.Verify(context.Background(), token, expectFor(p)); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	if _, err := svc.Verify(context.Background(), token, expectFor(p)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second Verify = %v, want ErrConflict", err)
	}
}

func TestESignOffNoneAlg(t *testing.T) {
	svc, st := newServiceWithKey(t)
	ctx := context.Background()
	if err := svc.SetNoESign(ctx, true); err != nil {
		t.Fatalf("SetNoESign: %v", err)
	}
	off, err := svc.NoESign(ctx)
	if err != nil || !off {
		t.Fatalf("NoESign = %v, %v; want true", off, err)
	}
	if st.config[signingConfigNoESign] != "1" {
		t.Fatalf("config no_esign = %q, want 1", st.config[signingConfigNoESign])
	}
	p := samplePayload()
	token, err := SignNone(p)
	if err != nil {
		t.Fatalf("SignNone: %v", err)
	}
	env, err := fwsigning.DecodeEnvelope(token)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if env.Alg != fwsigning.AlgNone {
		t.Fatalf("envelope alg = %q, want none", env.Alg)
	}
	if env.Signature != "" {
		t.Fatalf("expected no signature under eSign-off, got %q", env.Signature)
	}
	res, err := svc.Verify(ctx, token, expectFor(p))
	if err != nil {
		t.Fatalf("Verify none: %v", err)
	}
	if res.Signed {
		t.Fatalf("expected Signed=false under eSign-off")
	}
	// Binding still enforced under alg none.
	bad := expectFor(p)
	bad.Quantity = "1"
	if _, err := svc.Verify(ctx, token, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("expected binding rejection under eSign-off, got %v", err)
	}
}

func TestCanonicalBytesStable(t *testing.T) {
	p := samplePayload()
	b1, err := fwsigning.CanonicalBytes(p)
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	b2, err := fwsigning.CanonicalBytes(p)
	if err != nil {
		t.Fatalf("CanonicalBytes 2: %v", err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("canonical bytes not stable:\n%s\n%s", b1, b2)
	}
	// Version field appears first (declaration order == wire order).
	if !strings.HasPrefix(string(b1), `{"version":1,`) {
		t.Fatalf("unexpected canonical prefix: %s", b1)
	}
}

func TestGenerateDeactivatesPrior(t *testing.T) {
	svc, st := newServiceWithKey(t)
	ctx := context.Background()
	first, _, _ := st.GetActiveSigningKey(ctx)
	k2, err := svc.GenerateKey(ctx)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if k2.PrivateKey != nil {
		t.Fatalf("GenerateKey returned private key material")
	}
	active, ok, _ := st.GetActiveSigningKey(ctx)
	if !ok || active.KeyID != k2.KeyID {
		t.Fatalf("active key not rotated to new key")
	}
	if active.KeyID == first.KeyID {
		t.Fatalf("prior key still active")
	}
}

func TestListKeysOmitsPrivateIncludesInactive(t *testing.T) {
	svc, _ := newServiceWithKey(t)
	ctx := context.Background()
	if _, err := svc.GenerateKey(ctx); err != nil { // rotate -> one inactive remains
		t.Fatalf("GenerateKey: %v", err)
	}
	keys, err := svc.ListKeys(ctx)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("ListKeys len = %d, want 2 (incl. inactive)", len(keys))
	}
	var inactive int
	for _, k := range keys {
		if k.PrivateKey != nil {
			t.Fatalf("ListKeys leaked private key for %s", k.KeyID)
		}
		if !k.Active {
			inactive++
		}
	}
	if inactive != 1 {
		t.Fatalf("expected 1 inactive key, got %d", inactive)
	}
}

func TestFingerprint(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	fp := Fingerprint(pub)
	if len(fp) != 16 { // 8 bytes hex
		t.Fatalf("fingerprint len = %d, want 16", len(fp))
	}
	if fp != Fingerprint(pub) {
		t.Fatalf("fingerprint not deterministic")
	}
}

// --- BYOK import + export round-trips ---------------------------------------

func TestImportPEMPKCS8(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalPKCS8: %v", err)
	}
	material := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	assertImportSignsVerifies(t, material, FormatPEMPKCS8, priv)
}

func TestImportRawBase64(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	material := base64.StdEncoding.EncodeToString(priv.Seed())
	assertImportSignsVerifies(t, material, FormatRawBase64, priv)
}

func TestImportOpenSSH(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := cssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("MarshalPrivateKey: %v", err)
	}
	material := string(pem.EncodeToMemory(block))
	assertImportSignsVerifies(t, material, FormatOpenSSH, priv)
}

// assertImportSignsVerifies imports the material, signs a payload, and verifies
// the resulting token, asserting the imported public key matches priv.
func assertImportSignsVerifies(t *testing.T, material, format string, priv ed25519.PrivateKey) {
	t.Helper()
	st := newFakeStore()
	svc, err := New(st, NewMemoryReplayGuard())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	imported, err := svc.ImportKey(context.Background(), material, format)
	if err != nil {
		t.Fatalf("ImportKey(%s): %v", format, err)
	}
	if imported.PrivateKey != nil {
		t.Fatalf("ImportKey returned private material")
	}
	wantPub := priv.Public().(ed25519.PublicKey)
	if string(imported.PublicKey) != string(wantPub) {
		t.Fatalf("imported public key mismatch")
	}
	p := samplePayload()
	token, err := svc.Sign(p)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := svc.Verify(context.Background(), token, expectFor(p)); err != nil {
		t.Fatalf("Verify after import(%s): %v", format, err)
	}
}

func TestImportUnsupportedFormat(t *testing.T) {
	st := newFakeStore()
	svc, _ := New(st, NewMemoryReplayGuard())
	if _, err := svc.ImportKey(context.Background(), "x", "bogus"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for unsupported format, got %v", err)
	}
}

func TestImportRawBase64BadLength(t *testing.T) {
	st := newFakeStore()
	svc, _ := New(st, NewMemoryReplayGuard())
	short := base64.StdEncoding.EncodeToString([]byte("too short"))
	if _, err := svc.ImportKey(context.Background(), short, FormatRawBase64); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad seed length, got %v", err)
	}
}

func TestActivePublicKeyExportRoundTrips(t *testing.T) {
	svc, st := newServiceWithKey(t)
	active, _, _ := st.GetActiveSigningKey(context.Background())
	wantPub := ed25519.PublicKey(active.PublicKey)

	// pem-pkcs8 (PKIX/SPKI)
	pemStr, err := svc.ActivePublicKey(FormatPEMPKCS8)
	if err != nil {
		t.Fatalf("export pem: %v", err)
	}
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		t.Fatalf("export pem not a PEM block")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse PKIX: %v", err)
	}
	if string(parsed.(ed25519.PublicKey)) != string(wantPub) {
		t.Fatalf("pem public key round-trip mismatch")
	}

	// openssh authorized_keys
	sshStr, err := svc.ActivePublicKey(FormatOpenSSH)
	if err != nil {
		t.Fatalf("export openssh: %v", err)
	}
	parsedSSH, _, _, _, err := cssh.ParseAuthorizedKey([]byte(sshStr))
	if err != nil {
		t.Fatalf("parse authorized key: %v", err)
	}
	cryptoPub, ok := parsedSSH.(cssh.CryptoPublicKey)
	if !ok {
		t.Fatalf("ssh key is not a CryptoPublicKey")
	}
	if string(cryptoPub.CryptoPublicKey().(ed25519.PublicKey)) != string(wantPub) {
		t.Fatalf("openssh public key round-trip mismatch")
	}

	// raw-base64
	rawStr, err := svc.ActivePublicKey(FormatRawBase64)
	if err != nil {
		t.Fatalf("export raw: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(rawStr)
	if err != nil {
		t.Fatalf("decode raw export: %v", err)
	}
	if string(raw) != string(wantPub) {
		t.Fatalf("raw public key round-trip mismatch")
	}
}

func TestActivePublicKeyNoKey(t *testing.T) {
	st := newFakeStore()
	svc, _ := New(st, NewMemoryReplayGuard())
	if _, err := svc.ActivePublicKey(FormatPEMPKCS8); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound with no active key, got %v", err)
	}
}

func TestSignNoActiveKey(t *testing.T) {
	st := newFakeStore()
	svc, _ := New(st, NewMemoryReplayGuard())
	if _, err := svc.Sign(samplePayload()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound signing without a key, got %v", err)
	}
}

func TestReloadDropsActiveKeyRemovedOutsideService(t *testing.T) {
	st := newFakeStore()
	svc, err := New(st, NewMemoryReplayGuard())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := svc.GenerateKey(context.Background()); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	st.keys = map[string]domain.SigningKey{}
	if err := svc.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, err := svc.Sign(samplePayload()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Sign after Reload = %v, want ErrNotFound", err)
	}
}

func TestVerifyOnlyActiveKeyFailsToLoad(t *testing.T) {
	key := domain.SigningKey{
		KeyID: "verify-only", Alg: fwsigning.AlgEd25519, Active: true,
	}
	st := newFakeStore()
	st.keys[key.KeyID] = key
	if _, err := New(st, NewMemoryReplayGuard()); !errors.Is(err, ErrNoPrivateMaterial) {
		t.Fatalf("New with active verify-only key = %v, want ErrNoPrivateMaterial", err)
	}
}
