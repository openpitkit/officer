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

// Package signing defines the approval-token signing seam used by the Officer
// backend without binding it to a concrete key-management implementation. It
// also owns the approval envelope wire form: every signer produces it, and every
// verifier and reproduction reader decodes it.
package signing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"go.openpit.dev/officer/framework/domain"
)

// Algorithm identifiers carried in the approval envelope and payload. They are
// the stable, backend-agnostic contract shared by every issuer and verifier
// implementation.
const (
	AlgEd25519 = "ed25519" // a signed envelope
	AlgNone    = "none"    // an unsigned envelope produced under global eSign-off
)

// Service is the signing-and-verification seam the backend depends on to issue
// and check approval tokens. Use ServiceOrUnavailable when a composition point
// accepts optional signing; there is no allow-all default for signing.
type Service interface {
	GenerateKey(ctx context.Context) (domain.SigningKey, error)
	ImportKey(ctx context.Context, material, format string) (domain.SigningKey, error)
	ListKeys(ctx context.Context) ([]domain.SigningKey, error)
	ActivePublicKey(format string) (string, error)
	// PublicKeyByID exports the public key bound to keyID in the given format,
	// resolving it by id rather than by the active flag so a token signed under a
	// rotated (now inactive) key still yields the key that actually signed it. An
	// unknown id reports domain.ErrNotFound.
	PublicKeyByID(ctx context.Context, keyID, format string) (string, error)
	Fingerprint(pub []byte) string
	Sign(payload domain.ApprovalPayload) (string, error)
	// SignNone issues an unsigned token after the implementation reports
	// eSign-off via NoESign.
	SignNone(payload domain.ApprovalPayload) (string, error)
	Verify(ctx context.Context, token string, expect VerifyParams) (VerifyResult, error)
	NoESign(ctx context.Context) (bool, error)
	SetNoESign(ctx context.Context, off bool) error
	// Reload refreshes cached key state from committed store state after
	// backup restore.
	Reload(ctx context.Context) error
}

// ErrNotConfigured reports that no signing implementation was configured.
var ErrNotConfigured = fmt.Errorf(
	"signing: service not configured: %w",
	domain.ErrNotImplemented,
)

// ServiceOrUnavailable returns service or a nil-safe implementation that
// reports ErrNotConfigured from every operation.
func ServiceOrUnavailable(service Service) Service {
	if service == nil {
		return unavailableService{}
	}
	return service
}

// IsUnavailable reports whether service is nil or the nil-safe unavailable
// implementation returned by ServiceOrUnavailable.
func IsUnavailable(service Service) bool {
	if service == nil {
		return true
	}
	_, ok := service.(unavailableService)
	return ok
}

type unavailableService struct{}

func (unavailableService) GenerateKey(context.Context) (domain.SigningKey, error) {
	return domain.SigningKey{}, ErrNotConfigured
}

func (unavailableService) ImportKey(
	context.Context,
	string,
	string,
) (domain.SigningKey, error) {
	return domain.SigningKey{}, ErrNotConfigured
}

func (unavailableService) ListKeys(context.Context) ([]domain.SigningKey, error) {
	return nil, ErrNotConfigured
}

func (unavailableService) ActivePublicKey(string) (string, error) {
	return "", ErrNotConfigured
}

func (unavailableService) PublicKeyByID(
	context.Context,
	string,
	string,
) (string, error) {
	return "", ErrNotConfigured
}

func (unavailableService) Fingerprint([]byte) string { return "" }

func (unavailableService) Sign(domain.ApprovalPayload) (string, error) {
	return "", ErrNotConfigured
}

func (unavailableService) SignNone(domain.ApprovalPayload) (string, error) {
	return "", ErrNotConfigured
}

func (unavailableService) Verify(
	context.Context,
	string,
	VerifyParams,
) (VerifyResult, error) {
	return VerifyResult{}, ErrNotConfigured
}

func (unavailableService) NoESign(context.Context) (bool, error) {
	return false, ErrNotConfigured
}

func (unavailableService) SetNoESign(context.Context, bool) error {
	return ErrNotConfigured
}

func (unavailableService) Reload(context.Context) error {
	// An unconfigured service has no cached key state, so there is nothing to
	// refresh and refreshing it trivially succeeds.
	return nil
}

// VerifyParams are the connector-side expectations a token is re-bound against.
// Empty fields are not checked, except those always present in the payload.
type VerifyParams struct {
	OrderExternalID string
	Instrument      string
	Venue           string
	Side            string
	Quantity        string
	AmountKind      string
	OrderType       string
	LimitPrice      string
	PriceCurrency   string
	TimeInForce     string
	AccountID       string
	AccountGroupID  string
}

// VerifyResult is the structured outcome of a successful Verify.
type VerifyResult struct {
	Payload domain.ApprovalPayload
	Signed  bool
}

// Envelope is the wire form of a signed (or eSign-off) approval token. Signature
// is omitted under alg "none".
type Envelope struct {
	Approval  domain.ApprovalPayload `json:"approval"`
	Signature string                 `json:"signature,omitempty"`
	KeyID     string                 `json:"keyId,omitempty"`
	Alg       string                 `json:"alg"`
}

// CanonicalBytes returns the canonical wire bytes of payload: json.Marshal of
// the concrete struct (Go emits fields in declaration order, deterministic). It
// asserts byte-stability by marshalling twice and comparing.
//
// The struct field DECLARATION ORDER in domain.ApprovalPayload is load-bearing:
// it defines the canonical/signed form, so reordering its fields silently
// changes these bytes and invalidates every previously issued token (their
// signatures no longer verify against the re-ordered canonical form). Never
// reorder ApprovalPayload's fields to "tidy" them.
func CanonicalBytes(payload domain.ApprovalPayload) ([]byte, error) {
	b1, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("signing: marshal payload: %w", err)
	}
	b2, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("signing: marshal payload: %w", err)
	}
	if string(b1) != string(b2) {
		return nil, errors.New("signing: canonical bytes not stable across marshals")
	}
	return b1, nil
}

// BuildEnvelope encodes env to its base64url token form.
func BuildEnvelope(env Envelope) (string, error) {
	raw, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("signing: marshal envelope: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeEnvelope decodes a base64url token back into its envelope.
func DecodeEnvelope(token string) (Envelope, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Envelope{}, fmt.Errorf("signing: decode token: %w", domain.ErrInvalid)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Envelope{}, fmt.Errorf("signing: unmarshal envelope: %w", domain.ErrInvalid)
	}
	return env, nil
}
