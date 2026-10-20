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

package access

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"go.openpit.dev/officer/framework/domain"
)

const (
	// tokenBytes is the size of a token's random body.
	tokenBytes = 32
	// sessionIDBytes is the size of a session id.
	sessionIDBytes = 32

	// The bounds of a panel password, in Unicode code points.
	minPasswordRunes = 15
	maxPasswordRunes = 256

	// The argon2id cost of a password hash, after OWASP: 19 MiB of memory, two
	// passes, one lane.
	argonMemoryKiB = 19 * 1024
	argonPasses    = 2
	argonLanes     = 1
	argonSaltBytes = 16
	argonKeyBytes  = 32

	// recoveryCodeChars is the length of a recovery code: 60 bits in the
	// base32 alphabet.
	recoveryCodeChars = 12
	// recoveryAlphabet is the RFC 4648 base32 alphabet.
	recoveryAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
)

// phcPrefix opens every stored password hash: the algorithm, its version and
// the cost. Verification accepts no other.
var phcPrefix = fmt.Sprintf(
	"$argon2id$v=%d$m=%d,t=%d,p=%d$",
	argon2.Version, argonMemoryKiB, argonPasses, argonLanes,
)

// errMalformedHash refuses a stored password hash this package did not
// write. No password can be checked against it, so it is an internal error
// rather than a wrong password. It never carries the stored value.
var errMalformedHash = errors.New(
	"access: stored panel password hash is not an argon2id PHC string " +
		"with the expected cost",
)

// randomBytes returns n bytes from crypto/rand.
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// tokenPrefix returns the prefix that opens every value of the token of
// kind, so that secret scanners recognise the token.
func tokenPrefix(kind TokenKind) (string, error) {
	switch kind {
	case TokenAPI:
		return "pit_api_", nil
	case TokenAgent:
		return "pit_mcp_", nil
	default:
		return "", errUnknownTokenKind
	}
}

// newToken returns a new value of the token of kind: its prefix followed by
// 32 random bytes in unpadded base64url.
func newToken(kind TokenKind) (string, error) {
	prefix, err := tokenPrefix(kind)
	if err != nil {
		return "", err
	}
	body, err := randomBytes(tokenBytes)
	if err != nil {
		return "", fmt.Errorf("access: generate %s token: %w", kind, err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(body), nil
}

// newSession issues a session at now. It returns the raw session id in
// unpadded base64url, which only the browser's cookie keeps, and the record
// the store keeps.
func newSession(now time.Time) (string, Session, error) {
	id, err := randomBytes(sessionIDBytes)
	if err != nil {
		return "", Session{}, fmt.Errorf("access: generate session id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(id), Session{
		IDHash:     sha256.Sum256(id),
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(sessionLifetime),
	}, nil
}

// sessionIDHash returns the stored form of the session id a cookie carries.
// A value that does not decode to exactly 32 bytes names no session.
func sessionIDHash(cookie string) ([sha256.Size]byte, bool) {
	id, err := base64.RawURLEncoding.DecodeString(cookie)
	if err != nil || len(id) != sessionIDBytes {
		return [sha256.Size]byte{}, false
	}
	return sha256.Sum256(id), true
}

// validatePassword refuses a new panel password shorter than 15 or longer
// than 256 characters, counted as Unicode code points. Any characters are
// allowed and nothing about their mix is required.
func validatePassword(password string) error {
	length := utf8.RuneCountInString(password)
	if length < minPasswordRunes {
		return domain.NewValidationError("/password", "min_length", fmt.Sprintf(
			"password must be at least %d characters", minPasswordRunes,
		))
	}
	if length > maxPasswordRunes {
		return domain.NewValidationError("/password", "max_length", fmt.Sprintf(
			"password must be at most %d characters", maxPasswordRunes,
		))
	}
	return nil
}

// hashPassword returns the argon2id hash of password, with a new random salt,
// as a PHC string: the parameters, then the salt and the key in unpadded
// standard base64.
func hashPassword(password string) (string, error) {
	salt, err := randomBytes(argonSaltBytes)
	if err != nil {
		return "", fmt.Errorf("access: generate password salt: %w", err)
	}
	key := argon2.IDKey(
		[]byte(password), salt,
		argonPasses, argonMemoryKiB, argonLanes, argonKeyBytes,
	)
	return phcPrefix +
		base64.RawStdEncoding.EncodeToString(salt) + "$" +
		base64.RawStdEncoding.EncodeToString(key), nil
}

// verifyPassword reports whether password matches hash, comparing the keys
// in constant time. A hash that is not a PHC string hashPassword writes is
// errMalformedHash.
func verifyPassword(hash, password string) (bool, error) {
	encoded, ok := strings.CutPrefix(hash, phcPrefix)
	if !ok {
		return false, errMalformedHash
	}
	saltText, keyText, ok := strings.Cut(encoded, "$")
	if !ok {
		return false, errMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(saltText)
	if err != nil || len(salt) != argonSaltBytes {
		return false, errMalformedHash
	}
	key, err := base64.RawStdEncoding.DecodeString(keyText)
	if err != nil || len(key) != argonKeyBytes {
		return false, errMalformedHash
	}
	derived := argon2.IDKey(
		[]byte(password), salt,
		argonPasses, argonMemoryKiB, argonLanes, argonKeyBytes,
	)
	return subtle.ConstantTimeCompare(derived, key) == 1, nil
}

// newRecoveryCode returns 12 random characters of the base32 alphabet.
func newRecoveryCode() (string, error) {
	code, err := randomBytes(recoveryCodeChars)
	if err != nil {
		return "", fmt.Errorf("access: generate recovery code: %w", err)
	}
	for i, b := range code {
		// 256 is a multiple of the alphabet's 32 characters, so the
		// remainder keeps every character equally likely.
		code[i] = recoveryAlphabet[int(b)%len(recoveryAlphabet)]
	}
	return string(code), nil
}

// formatRecoveryCode shows a code as XXXX-XXXX-XXXX.
func formatRecoveryCode(code string) string {
	return code[:4] + "-" + code[4:8] + "-" + code[8:]
}

// normalizeRecoveryCode drops the dashes and spaces a person may type with a
// recovery code and upper-cases the rest.
func normalizeRecoveryCode(input string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(input))
}

// secretsEqual compares two secrets in constant time. Only a difference in
// length returns early, and the length of a secret here is public.
func secretsEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
