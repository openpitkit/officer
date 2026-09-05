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

// Package secret seals secret values under an operator-supplied master key.
package secret

import (
	"bytes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// Sealed values use version(1) || nonce(24) || ciphertext+tag. The version
	// byte is a forward contract for algorithm changes, not compatibility with
	// an older format.
	sealedVersion   byte = 0x01
	keySize              = chacha20poly1305.KeySize
	keyVerifierSize      = 32

	fieldSealingInfo = "go.openpit.dev/secret/v1/field-sealing"
	keyVerifierInfo  = "go.openpit.dev/secret/v1/key-verifier"
)

// MasterKey is a 256-bit key supplied by an operator. Its zero value is invalid.
type MasterKey struct {
	material []byte
}

// ParseMasterKey parses a standard-base64 master key.
func ParseMasterKey(encoded string) (MasterKey, error) {
	return parseMasterKey(encoded, "master key")
}

// ParseMasterKeyFile parses a standard-base64 master key from file contents.
// Surrounding whitespace is ignored.
func ParseMasterKeyFile(contents []byte) (MasterKey, error) {
	return parseMasterKey(string(bytes.TrimSpace(contents)), "master key file")
}

// Equal compares two initialized master keys in constant time.
func (k MasterKey) Equal(other MasterKey) (bool, error) {
	if !k.valid() {
		return false, errors.New("secret: first master key is not initialized")
	}
	if !other.valid() {
		return false, errors.New("secret: second master key is not initialized")
	}
	return subtle.ConstantTimeCompare(k.material, other.material) == 1, nil
}

// BuildAAD builds unambiguous additional authenticated data for a stored value.
// The row ID must remain immutable for the lifetime of the sealed value.
func BuildAAD(realmID, tableName, columnName, rowID string) ([]byte, error) {
	components := [...]struct {
		name  string
		value string
	}{
		{name: "realm ID", value: realmID},
		{name: "table name", value: tableName},
		{name: "column name", value: columnName},
		{name: "row ID", value: rowID},
	}

	aad := make([]byte, 0, len(components)*8)
	for _, component := range components {
		if component.value == "" {
			return nil, fmt.Errorf("secret: %s is empty", component.name)
		}
		aad = binary.BigEndian.AppendUint64(aad, uint64(len(component.value)))
		aad = append(aad, component.value...)
	}
	return aad, nil
}

// Seal encrypts and authenticates plaintext with location-binding AAD.
func Seal(key MasterKey, aad, plaintext []byte) ([]byte, error) {
	if len(aad) == 0 {
		return nil, errors.New("secret: AAD is empty")
	}

	aead, err := fieldAEAD(key)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secret: generate nonce: %w", err)
	}

	sealed := make([]byte, 1+len(nonce), 1+len(nonce)+len(plaintext)+aead.Overhead())
	sealed[0] = sealedVersion
	copy(sealed[1:], nonce)
	return aead.Seal(sealed, nonce, plaintext, aad), nil
}

// Open authenticates and decrypts a versioned sealed value with location-binding AAD.
func Open(key MasterKey, aad, sealed []byte) ([]byte, error) {
	if len(aad) == 0 {
		return nil, errors.New("secret: AAD is empty")
	}

	const minimumSize = 1 + chacha20poly1305.NonceSizeX + chacha20poly1305.Overhead
	if len(sealed) < minimumSize {
		return nil, fmt.Errorf(
			"secret: sealed value is too short: got %d bytes, want at least %d",
			len(sealed),
			minimumSize,
		)
	}
	if sealed[0] != sealedVersion {
		return nil, errors.New("secret: unsupported sealed value version")
	}

	aead, err := fieldAEAD(key)
	if err != nil {
		return nil, err
	}

	nonce := sealed[1 : 1+chacha20poly1305.NonceSizeX]
	ciphertext := sealed[1+chacha20poly1305.NonceSizeX:]
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, errors.New("secret: open sealed value: authentication failed")
	}
	return plaintext, nil
}

// KeyVerifier derives a stable, non-decrypting fingerprint of a master key.
func KeyVerifier(key MasterKey) ([]byte, error) {
	return deriveSubkey(key, keyVerifierInfo, keyVerifierSize)
}

// KeyVerifiersEqual compares two fixed-length key verifiers in constant time.
func KeyVerifiersEqual(first, second []byte) (bool, error) {
	if len(first) != keyVerifierSize {
		return false, fmt.Errorf(
			"secret: first key verifier has length %d, want %d",
			len(first),
			keyVerifierSize,
		)
	}
	if len(second) != keyVerifierSize {
		return false, fmt.Errorf(
			"secret: second key verifier has length %d, want %d",
			len(second),
			keyVerifierSize,
		)
	}
	return subtle.ConstantTimeCompare(first, second) == 1, nil
}

func parseMasterKey(encoded, source string) (MasterKey, error) {
	if encoded == "" {
		return MasterKey{}, fmt.Errorf("secret: %s is empty", source)
	}

	material, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return MasterKey{}, fmt.Errorf("secret: %s is not valid standard base64", source)
	}
	if len(material) != keySize {
		return MasterKey{}, fmt.Errorf(
			"secret: %s decodes to %d bytes, want %d",
			source,
			len(material),
			keySize,
		)
	}
	return MasterKey{material: material}, nil
}

func (k MasterKey) valid() bool {
	return len(k.material) == keySize
}

func fieldAEAD(key MasterKey) (cipher.AEAD, error) {
	material, err := deriveSubkey(key, fieldSealingInfo, keySize)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(material)
	if err != nil {
		return nil, fmt.Errorf("secret: initialize field cipher: %w", err)
	}
	return aead, nil
}

func deriveSubkey(key MasterKey, info string, size int) ([]byte, error) {
	if !key.valid() {
		return nil, errors.New("secret: master key is not initialized")
	}
	derived, err := hkdf.Key(sha256.New, key.material, nil, info, size)
	if err != nil {
		return nil, fmt.Errorf("secret: derive sub-key: %w", err)
	}
	return derived, nil
}
