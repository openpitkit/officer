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
// backend without binding it to a concrete key-management implementation.
package signing

import (
	"context"
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
