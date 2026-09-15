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

// Package signing implements Ed25519 approval-token signing and verification
// over the framework approval envelope. It owns BYOK key import/export and the
// global eSign-off read path. Private keys never leave this package.
package signing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"go.openpit.dev/officer/framework/domain"
	fwsigning "go.openpit.dev/officer/framework/signing"
)

// Key material formats accepted by ImportKey and produced by ActivePublicKey.
const (
	// FormatPEMPKCS8 is a PKCS#8 (private) / PKIX-SPKI (public) PEM block.
	FormatPEMPKCS8 = "pem-pkcs8"
	// FormatOpenSSH is an OpenSSH private key / authorized_keys public line.
	FormatOpenSSH = "openssh"
	// FormatRawBase64 is the raw 32-byte seed / 32-byte public key, base64-std.
	FormatRawBase64 = "raw-base64"
)

// ErrNoPrivateMaterial reports a verify-only signing key. The installation must
// generate a new key before it can create signatures.
var ErrNoPrivateMaterial = errors.New(
	"signing key holds no private material; generate a new signing key",
)

// signingConfigNoESign is the signing_config key holding the global eSign flag
// ("0" or "1").
const signingConfigNoESign = "no_esign"

// Store is the subset of the persistence layer the signing service consumes.
// GetActiveSigningKey and GetSigningKey return PrivateKey-populated rows; all
// other reads omit it.
type Store interface {
	UpsertSigningKey(ctx context.Context, key domain.SigningKey) error
	GetActiveSigningKey(ctx context.Context) (domain.SigningKey, bool, error)
	GetSigningKey(ctx context.Context, keyID string) (domain.SigningKey, error)
	ListSigningKeys(ctx context.Context) ([]domain.SigningKey, error)
	DeactivateAllSigningKeys(ctx context.Context) error
	GetSigningConfig(ctx context.Context, key string) (string, bool, error)
	SetSigningConfig(ctx context.Context, key, value string) error
}

// Service signs and verifies approval tokens against the persisted key set. The
// active keypair is cached in memory; key generation and import refresh it, and
// callers that change keys through another path must call Reload after commit.
// A nil active key means no key is configured yet.
type Service struct {
	store Store

	mu      sync.RWMutex
	active  *domain.SigningKey // PrivateKey populated; nil when none
	signKey ed25519.PrivateKey // expanded from active seed; nil when none
}

var _ fwsigning.Service = (*Service)(nil)

// New constructs a Service backed by st and loads the active key (if any) into
// the in-memory cache.
func New(st Store) (*Service, error) {
	if st == nil {
		return nil, fmt.Errorf("signing: nil store: %w", domain.ErrInvalid)
	}
	s := &Service{store: st}
	if err := s.reloadActive(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// reloadActive refreshes the cached active key from the store.
func (s *Service) reloadActive(ctx context.Context) error {
	key, ok, err := s.store.GetActiveSigningKey(ctx)
	if err != nil {
		return fmt.Errorf("signing: load active key: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ok {
		s.active = nil
		s.signKey = nil
		return nil
	}
	seed, err := seedToPrivate(key.PrivateKey)
	if err != nil {
		return fmt.Errorf("signing: active key material: %w", err)
	}
	k := key
	s.active = &k
	s.signKey = seed
	return nil
}

// Reload refreshes the cached active key from committed store state. Callers use
// it after an operation outside the signing service, such as backup restore,
// changes persisted keys.
func (s *Service) Reload(ctx context.Context) error {
	return s.reloadActive(ctx)
}

// GenerateKey creates a fresh Ed25519 keypair, deactivates any prior active
// key, stores the new key as active, and returns it without the private half.
func (s *Service) GenerateKey(ctx context.Context) (domain.SigningKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return domain.SigningKey{}, fmt.Errorf("signing: generate key: %w", err)
	}
	return s.storeNewActive(ctx, pub, priv.Seed())
}

// ImportKey imports an externally supplied Ed25519 private key in the given
// format, deactivates any prior active key, stores it as active, and returns it
// without the private half. Encrypted or non-Ed25519 material is rejected.
func (s *Service) ImportKey(ctx context.Context, material, format string) (domain.SigningKey, error) {
	seed, pub, err := parsePrivateMaterial(material, format)
	if err != nil {
		return domain.SigningKey{}, err
	}
	return s.storeNewActive(ctx, pub, seed)
}

// storeNewActive persists (pub, seed) as the sole active key and refreshes the
// cache. seed is the 32-byte Ed25519 seed.
func (s *Service) storeNewActive(ctx context.Context, pub ed25519.PublicKey, seed []byte) (domain.SigningKey, error) {
	if err := s.store.DeactivateAllSigningKeys(ctx); err != nil {
		return domain.SigningKey{}, fmt.Errorf("signing: deactivate prior keys: %w", err)
	}
	key := domain.SigningKey{
		CreatedAt:  time.Now().UTC(),
		KeyID:      uuid.NewString(),
		Alg:        fwsigning.AlgEd25519,
		PublicKey:  append([]byte(nil), pub...),
		PrivateKey: append([]byte(nil), seed...),
		Active:     true,
	}
	if err := s.store.UpsertSigningKey(ctx, key); err != nil {
		return domain.SigningKey{}, fmt.Errorf("signing: store key: %w", err)
	}
	if err := s.reloadActive(ctx); err != nil {
		return domain.SigningKey{}, err
	}
	return redact(key), nil
}

// ListKeys returns all keys ordered newest-first, including inactive public
// keys (connectors verify in-flight tokens). Private material is never carried.
func (s *Service) ListKeys(ctx context.Context) ([]domain.SigningKey, error) {
	keys, err := s.store.ListSigningKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("signing: list keys: %w", err)
	}
	out := make([]domain.SigningKey, len(keys))
	for i := range keys {
		out[i] = redact(keys[i])
	}
	return out, nil
}

// ActivePublicKey exports the active public key in the given format.
func (s *Service) ActivePublicKey(format string) (string, error) {
	s.mu.RLock()
	active := s.active
	s.mu.RUnlock()
	if active == nil {
		return "", fmt.Errorf("signing: no active key: %w", domain.ErrNotFound)
	}
	return exportPublic(active.PublicKey, format)
}

// PublicKeyByID exports the public key bound to keyID in the given format. It
// resolves the key by id, not by the active flag, so a token signed under a
// since-rotated key still yields the exact public key that signed it. It reads
// only the public half; an unknown id reports domain.ErrNotFound.
func (s *Service) PublicKeyByID(ctx context.Context, keyID, format string) (string, error) {
	if keyID == "" {
		return "", fmt.Errorf("signing: empty keyId: %w", domain.ErrInvalid)
	}
	pub, err := s.publicKeyFor(ctx, keyID)
	if err != nil {
		return "", err
	}
	return exportPublic(pub, format)
}

// Fingerprint returns a short hex SHA-256 prefix of the public key. Display
// only; never stored or used as an identity.
func (s *Service) Fingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// Sign builds the signed envelope token for payload, using the active key. It
// sets KeyID and Alg on a copy of the payload, signs the canonical bytes, and
// returns the base64url-encoded envelope.
func (s *Service) Sign(payload domain.ApprovalPayload) (string, error) {
	s.mu.RLock()
	active := s.active
	key := s.signKey
	s.mu.RUnlock()
	if active == nil {
		return "", fmt.Errorf("signing: no active key: %w", domain.ErrNotFound)
	}
	// reloadActive writes active and signKey together, so construction cannot reach
	// this branch; it prevents a nil signing key from reaching ed25519.Sign.
	if key == nil {
		return "", fmt.Errorf("signing: %w", ErrNoPrivateMaterial)
	}
	payload.KeyID = active.KeyID
	payload.Alg = fwsigning.AlgEd25519
	canon, err := fwsigning.CanonicalBytes(payload)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(key, canon)
	env := fwsigning.Envelope{
		Approval:  payload,
		Signature: base64.StdEncoding.EncodeToString(sig),
		KeyID:     active.KeyID,
		Alg:       fwsigning.AlgEd25519,
	}
	return fwsigning.BuildEnvelope(env)
}

// SignNone builds an unsigned (eSign-off) envelope token so Service satisfies
// the framework signing seam.
func (s *Service) SignNone(payload domain.ApprovalPayload) (string, error) {
	return SignNone(payload)
}

// SignNone builds an unsigned (eSign-off) envelope token: the full payload is
// emitted with alg "none" and no signature.
func SignNone(payload domain.ApprovalPayload) (string, error) {
	payload.KeyID = ""
	payload.Alg = fwsigning.AlgNone
	env := fwsigning.Envelope{Approval: payload, Alg: fwsigning.AlgNone}
	return fwsigning.BuildEnvelope(env)
}

// Verify decodes the token, recomputes its canonical bytes, verifies the
// signature when alg is ed25519, and re-binds the bound order params against
// expect. For alg "none" the signature step is skipped; binding still applies.
func (s *Service) Verify(
	ctx context.Context, token string, expect fwsigning.VerifyParams,
) (fwsigning.VerifyResult, error) {
	env, err := fwsigning.DecodeEnvelope(token)
	if err != nil {
		return fwsigning.VerifyResult{}, err
	}
	canon, err := fwsigning.CanonicalBytes(env.Approval)
	if err != nil {
		return fwsigning.VerifyResult{}, err
	}
	signed := false
	switch env.Approval.Alg {
	case fwsigning.AlgEd25519:
		if env.Alg != fwsigning.AlgEd25519 {
			return fwsigning.VerifyResult{}, fmt.Errorf("signing: envelope alg %q mismatches payload: %w", env.Alg, domain.ErrInvalid)
		}
		pub, err := s.publicKeyFor(ctx, env.Approval.KeyID)
		if err != nil {
			return fwsigning.VerifyResult{}, err
		}
		sig, err := base64.StdEncoding.DecodeString(env.Signature)
		if err != nil {
			return fwsigning.VerifyResult{}, fmt.Errorf("signing: decode signature: %w", domain.ErrInvalid)
		}
		if !ed25519.Verify(pub, canon, sig) {
			return fwsigning.VerifyResult{}, fmt.Errorf("signing: signature verification failed: %w", domain.ErrInvalid)
		}
		signed = true
	case fwsigning.AlgNone:
		if env.Alg != fwsigning.AlgNone {
			return fwsigning.VerifyResult{}, fmt.Errorf("signing: envelope alg %q mismatches payload: %w", env.Alg, domain.ErrInvalid)
		}
		off, err := s.NoESign(ctx)
		if err != nil {
			return fwsigning.VerifyResult{}, err
		}
		if !off {
			return fwsigning.VerifyResult{}, fmt.Errorf("signing: unsigned token rejected while eSign is enabled: %w", domain.ErrInvalid)
		}
		// eSign-off: no signature, binding still enforced.
	default:
		return fwsigning.VerifyResult{}, fmt.Errorf("signing: unsupported alg %q: %w", env.Approval.Alg, domain.ErrInvalid)
	}
	if env.Approval.Version != 1 {
		return fwsigning.VerifyResult{}, fmt.Errorf("signing: unsupported payload version %d: %w", env.Approval.Version, domain.ErrInvalid)
	}
	if err := rebind(env.Approval, expect); err != nil {
		return fwsigning.VerifyResult{}, err
	}
	return fwsigning.VerifyResult{Payload: env.Approval, Signed: signed}, nil
}

// publicKeyFor resolves the public key bound to keyID, preferring the cached
// active key, then the store. Unknown keys are rejected.
func (s *Service) publicKeyFor(ctx context.Context, keyID string) (ed25519.PublicKey, error) {
	if keyID == "" {
		return nil, fmt.Errorf("signing: empty keyId: %w", domain.ErrInvalid)
	}
	s.mu.RLock()
	active := s.active
	s.mu.RUnlock()
	if active != nil && active.KeyID == keyID {
		return ed25519.PublicKey(active.PublicKey), nil
	}
	keys, err := s.store.ListSigningKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("signing: lookup key %q: %w", keyID, err)
	}
	for i := range keys {
		if keys[i].KeyID == keyID {
			return ed25519.PublicKey(keys[i].PublicKey), nil
		}
	}
	return nil, fmt.Errorf("signing: unknown keyId %q: %w", keyID, domain.ErrNotFound)
}

// NoESign reports whether the global eSign-off flag is set.
func (s *Service) NoESign(ctx context.Context) (bool, error) {
	v, ok, err := s.store.GetSigningConfig(ctx, signingConfigNoESign)
	if err != nil {
		return false, fmt.Errorf("signing: read no_esign: %w", err)
	}
	if !ok {
		return false, nil
	}
	return v == "1", nil
}

// SetNoESign sets the global eSign-off flag.
func (s *Service) SetNoESign(ctx context.Context, off bool) error {
	v := "0"
	if off {
		v = "1"
	}
	if err := s.store.SetSigningConfig(ctx, signingConfigNoESign, v); err != nil {
		return fmt.Errorf("signing: write no_esign: %w", err)
	}
	return nil
}

// rebind compares the bound order params in the payload against expect, failing
// on any mismatch. Optional fields are checked only when non-empty in expect.
func rebind(p domain.ApprovalPayload, expect fwsigning.VerifyParams) error {
	check := func(field, want, got string) error {
		if want != got {
			return fmt.Errorf("signing: %s mismatch (token %q != order %q): %w", field, got, want, domain.ErrInvalid)
		}
		return nil
	}
	for _, c := range []struct{ field, want, got string }{
		{"instrument", expect.Instrument, p.Instrument},
		{"side", expect.Side, p.Side},
		{"quantity", expect.Quantity, p.Quantity},
		{"amountKind", expect.AmountKind, p.AmountKind},
		{"orderType", expect.OrderType, p.OrderType},
		{"limitPrice", expect.LimitPrice, p.LimitPrice},
		{"priceCurrency", expect.PriceCurrency, p.PriceCurrency},
		{"timeInForce", expect.TimeInForce, p.TimeInForce},
		{"accountId", expect.AccountID, p.AccountID},
	} {
		if err := check(c.field, c.want, c.got); err != nil {
			return err
		}
	}
	if expect.OrderExternalID != "" {
		// Once an order row exists, its opaque handle is a required binding.
		if p.OrderExternalID == "" {
			return fmt.Errorf("signing: orderId missing for order %q: %w",
				expect.OrderExternalID, domain.ErrInvalid)
		}
		if err := check("orderId", expect.OrderExternalID, p.OrderExternalID); err != nil {
			return err
		}
	}
	if expect.Venue != "" {
		if err := check("venue", expect.Venue, p.Venue); err != nil {
			return err
		}
	}
	if expect.AccountGroupID != "" {
		if err := check("accountGroupId", expect.AccountGroupID, p.AccountGroupID); err != nil {
			return err
		}
	}
	return nil
}

// --- key material helpers ---------------------------------------------------

// redact returns a copy of key with the private half cleared.
func redact(key domain.SigningKey) domain.SigningKey {
	key.PrivateKey = nil
	return key
}

// seedToPrivate builds an ed25519.PrivateKey from a stored 32-byte seed. It also
// accepts a full 64-byte key for tolerance.
func seedToPrivate(material []byte) (ed25519.PrivateKey, error) {
	switch len(material) {
	case 0:
		return nil, ErrNoPrivateMaterial
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(material), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(append([]byte(nil), material...)), nil
	default:
		return nil, fmt.Errorf("unexpected key length %d: %w", len(material), domain.ErrInvalid)
	}
}

// parsePrivateMaterial decodes BYOK private-key material in the given format and
// returns the 32-byte seed plus the derived public key. Encrypted or non-Ed25519
// material is rejected with a clear error.
func parsePrivateMaterial(material, format string) (seed []byte, pub ed25519.PublicKey, err error) {
	switch format {
	case FormatPEMPKCS8:
		return parsePEMPKCS8(material)
	case FormatOpenSSH:
		return parseOpenSSH(material)
	case FormatRawBase64:
		return parseRawBase64(material)
	default:
		return nil, nil, fmt.Errorf("signing: unsupported key format %q: %w", format, domain.ErrInvalid)
	}
}

func parsePEMPKCS8(material string) ([]byte, ed25519.PublicKey, error) {
	block, _ := pem.Decode([]byte(material))
	if block == nil {
		return nil, nil, fmt.Errorf("signing: no PEM block found: %w", domain.ErrInvalid)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Encrypted PKCS#8 fails here; surface a single clear rejection.
		return nil, nil, fmt.Errorf("signing: parse PKCS#8 (unencrypted Ed25519 only): %w", domain.ErrInvalid)
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("signing: PKCS#8 key is not Ed25519: %w", domain.ErrInvalid)
	}
	return priv.Seed(), priv.Public().(ed25519.PublicKey), nil
}

func parseOpenSSH(material string) ([]byte, ed25519.PublicKey, error) {
	parsed, err := ssh.ParseRawPrivateKey([]byte(material))
	if err != nil {
		return nil, nil, fmt.Errorf("signing: parse OpenSSH key (unencrypted Ed25519 only): %w", domain.ErrInvalid)
	}
	// ParseRawPrivateKey returns *ed25519.PrivateKey for Ed25519 keys.
	priv, ok := parsed.(*ed25519.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("signing: OpenSSH key is not Ed25519: %w", domain.ErrInvalid)
	}
	return priv.Seed(), priv.Public().(ed25519.PublicKey), nil
}

func parseRawBase64(material string) ([]byte, ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(material)
	if err != nil {
		return nil, nil, fmt.Errorf("signing: decode raw base64 seed: %w", domain.ErrInvalid)
	}
	if len(raw) != ed25519.SeedSize {
		return nil, nil, fmt.Errorf("signing: raw seed must be %d bytes, got %d: %w", ed25519.SeedSize, len(raw), domain.ErrInvalid)
	}
	priv := ed25519.NewKeyFromSeed(raw)
	return raw, priv.Public().(ed25519.PublicKey), nil
}

// exportPublic serializes a 32-byte Ed25519 public key in the given format.
func exportPublic(pub []byte, format string) (string, error) {
	switch format {
	case FormatPEMPKCS8:
		der, err := x509.MarshalPKIXPublicKey(ed25519.PublicKey(pub))
		if err != nil {
			return "", fmt.Errorf("signing: marshal PKIX public key: %w", err)
		}
		block := &pem.Block{Type: "PUBLIC KEY", Bytes: der}
		return string(pem.EncodeToMemory(block)), nil
	case FormatOpenSSH:
		sshPub, err := ssh.NewPublicKey(ed25519.PublicKey(pub))
		if err != nil {
			return "", fmt.Errorf("signing: build OpenSSH public key: %w", err)
		}
		return string(ssh.MarshalAuthorizedKey(sshPub)), nil
	case FormatRawBase64:
		return base64.StdEncoding.EncodeToString(pub), nil
	default:
		return "", fmt.Errorf("signing: unsupported export format %q: %w", format, domain.ErrInvalid)
	}
}
