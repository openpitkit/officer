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

package coinbase

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"
)

func mintJWT(credentials Credentials, uri string, now time.Time,
	nonceSource io.Reader,
) (string, error) {
	key := credentials.PrivateKey
	if credentials.APIKeyID == "" || key == nil || key.Curve != elliptic.P256() ||
		key.X == nil || key.Y == nil {
		return "", errors.New("coinbase: valid CDP credentials are required")
	}
	var nonce [16]byte
	if _, err := io.ReadFull(nonceSource, nonce[:]); err != nil {
		return "", errors.New("coinbase: generate JWT nonce failed")
	}
	header, err := json.Marshal(struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
		Nonce     string `json:"nonce"`
		Type      string `json:"typ"`
	}{"ES256", credentials.APIKeyID, hex.EncodeToString(nonce[:]), "JWT"})
	if err != nil {
		return "", errors.New("coinbase: encode JWT header failed")
	}
	payload, err := json.Marshal(struct {
		Issuer    string `json:"iss"`
		Subject   string `json:"sub"`
		NotBefore int64  `json:"nbf"`
		Expires   int64  `json:"exp"`
		URI       string `json:"uri,omitempty"`
	}{"cdp", credentials.APIKeyID, now.Unix(), now.Unix() + 120, uri})
	if err != nil {
		return "", errors.New("coinbase: encode JWT payload failed")
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	if _, err := key.Bytes(); err != nil {
		return "", errors.New("coinbase: sign JWT failed")
	}
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", errors.New("coinbase: sign JWT failed")
	}
	var signature [64]byte
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature[:]), nil
}
