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

package fixture

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"math/big"

	"go.openpit.dev/officer/internal/apikey"
)

func privateKeySinks(ctx context.Context, key *ecdsa.PrivateKey,
	credentials apikey.CDPCredentials, logger *slog.Logger, encoder *json.Encoder,
) {
	// ruleid: no-private-key-formatting
	fmt.Printf("%s", key)
	// ruleid: no-private-key-formatting
	fmt.Errorf("%d", credentials)
	// ruleid: no-private-key-formatting
	log.Print(key)
	// ruleid: no-private-key-formatting
	slog.Info("key", "value", key)
	// ruleid: no-private-key-formatting
	slog.Any("key", credentials)
	// ruleid: no-private-key-formatting
	logger.Debug("key", "value", key)
	// ruleid: no-private-key-formatting
	logger.Log(ctx, slog.LevelInfo, "key", "value", credentials)
	// ruleid: no-private-key-formatting
	slog.Default().Warn("key", "value", key)
	// ruleid: no-private-key-formatting
	errors.New(key)
	// ruleid: no-private-key-formatting
	json.Marshal(key)
	// ruleid: no-private-key-formatting
	json.MarshalIndent(credentials, "", "  ")
	// ruleid: no-private-key-formatting
	encoder.Encode(key)
	// ruleid: no-private-key-formatting
	fmt.Sprint(key.D)
	// ruleid: no-private-key-formatting
	key.D.String()
	// ruleid: no-private-key-formatting
	key.D.Text(16)
	// ruleid: no-private-key-formatting
	json.Marshal(struct{ Value any }{key})
	// ruleid: no-private-key-formatting
	json.Marshal(struct{ Value any }{credentials})
	// ruleid: no-private-key-formatting
	json.Marshal(struct{ Value any }{key.D})
	// ruleid: no-private-key-formatting
	json.Marshal(struct{ Value any }{Value: credentials})
	// ruleid: no-private-key-formatting
	json.Marshal(struct{ Value any }{Value: key.D})
	// ruleid: no-private-key-formatting
	json.Marshal(map[string]any{"credentials": credentials})
	// ruleid: no-private-key-formatting
	json.Marshal(map[string]any{"scalar": key.D})
	// ruleid: no-private-key-formatting
	encoder.Encode(struct{ Value any }{Value: key})
	// ruleid: no-private-key-formatting
	json.Marshal(map[string]any{"nested": map[string]any{"key": key}})

	// ok: no-private-key-formatting
	fmt.Sprint(credentials.APIKeyID)
	// ok: no-private-key-formatting
	json.Marshal(struct{ Name string }{credentials.APIKeyID})
	// ok: no-private-key-formatting
	fmt.Sprint(key.PublicKey)
	// ok: no-private-key-formatting
	logger.Info("signing failed", "error", errors.New("invalid key"))
	// ok: no-private-key-formatting
	encoder.Encode(map[string]string{"status": "ok"})
}

func unresolvedFields(value any, encoder *json.Encoder) {
	// ruleid: no-private-key-formatting
	fmt.Print(value.PrivateKey)
	// ruleid: no-private-key-formatting
	log.Printf("%s", value.PrivateKey.D)
	// ruleid: no-private-key-formatting
	slog.Error("key", "value", value.PrivateKey)
	// ruleid: no-private-key-formatting
	errors.New(value.PrivateKey)
	// ruleid: no-private-key-formatting
	json.Marshal(value.PrivateKey)
	// ruleid: no-private-key-formatting
	json.MarshalIndent(value.PrivateKey.D, "", "  ")
	// ruleid: no-private-key-formatting
	encoder.Encode(value.PrivateKey)
	// ruleid: no-private-key-formatting
	json.NewEncoder(nil).Encode(value.PrivateKey.D)
	// ruleid: no-private-key-formatting
	json.Marshal(struct{ Key any }{Key: value.PrivateKey})
	// ruleid: no-private-key-formatting
	json.Marshal(struct{ Key any }{value.PrivateKey})
	// ruleid: no-private-key-formatting
	json.Marshal(struct{ Key any }{value.PrivateKey.D})
	// ruleid: no-private-key-formatting
	json.Marshal(struct{ Key any }{Key: value.PrivateKey.D})
	// ruleid: no-private-key-formatting
	json.Marshal(map[string]any{"key": value.PrivateKey})
	// ruleid: no-private-key-formatting
	json.Marshal(map[string]any{"scalar": value.PrivateKey.D})
	// ruleid: no-private-key-formatting
	value.PrivateKey.D.String()
	// ruleid: no-private-key-formatting
	value.PrivateKey.D.Text(10)

	// ok: no-private-key-formatting
	json.Marshal(value.PublicKey)
	// ok: no-private-key-formatting
	fmt.Print(value.APIKeyID)
}

type CDPCredentials struct{ PrivateKey *ecdsa.PrivateKey }

func localCredentialType(credentials CDPCredentials, public *ecdsa.PublicKey, number *big.Int) {
	// ruleid: no-private-key-formatting
	fmt.Sprint(credentials)
	// ruleid: no-private-key-formatting
	json.Marshal(struct{ Value any }{credentials})
	// ruleid: no-private-key-formatting
	json.Marshal(struct{ Value any }{Value: credentials})
	// ruleid: no-private-key-formatting
	json.Marshal(map[string]any{"credentials": credentials})
	// ok: no-private-key-formatting
	fmt.Sprint(public)
	// ok: no-private-key-formatting
	number.String()
	// ok: no-private-key-formatting
	number.Text(16)
}

type Credentials = apikey.CDPCredentials

func aliasedCredentials(credentials Credentials) {
	// ruleid: no-private-key-formatting
	json.Marshal(credentials)
	// ruleid: no-private-key-formatting
	credentials.PrivateKey.D.String()
	key := credentials.PrivateKey
	// ruleid: no-private-key-formatting
	slog.Debug("key", "value", key)
	// ruleid: no-private-key-formatting
	fmt.Errorf("%v", key)
	// ruleid: no-private-key-formatting
	key.D.Text(16)
}

type signer struct {
	key *ecdsa.PrivateKey
}

func (s *signer) log() {
	// ruleid: no-private-key-formatting
	slog.Info("signer", "key", s.key)
}

type client struct {
	baseURL     string
	credentials Credentials
}

func (c *client) log() {
	// ruleid: no-private-key-formatting
	fmt.Sprintf("%s %v", c.baseURL, c.credentials)
	// ruleid: no-private-key-formatting
	slog.Debug("coinbase request", "credentials", c.credentials)
}

func anonymousKeyField(key *ecdsa.PrivateKey) {
	w := struct{ K *ecdsa.PrivateKey }{key}
	// ruleid: no-private-key-formatting
	fmt.Sprint(w.K)
}

func wrappedComposites(key *ecdsa.PrivateKey) {
	type entry struct{ Key any }
	var entries []entry
	// todoruleid: no-private-key-formatting
	json.Marshal(append(entries, entry{Key: key}))
	// todoruleid: no-private-key-formatting
	json.Marshal(any(entry{key}))
}

func hmacCredentials() {
	type Credentials struct {
		APIKey    string
		APISecret string
	}
	credentials := Credentials{APIKey: "identifier", APISecret: "secret"}
	frame := struct{ APIKey string }{credentials.APIKey}
	// ok: no-private-key-formatting
	json.Marshal(frame)
	// ruleid: no-private-key-formatting
	json.Marshal(credentials)
}

func parsedPrivateKey(der []byte) error {
	key, err := x509.ParseECPrivateKey(der)
	if err != nil {
		// ok: no-private-key-formatting
		return errors.New("invalid private key")
	}
	// ruleid: no-private-key-formatting
	return fmt.Errorf("%v", key)
}

func signAndSubscribe(credentials Credentials) error {
	key := credentials.PrivateKey
	// ok: no-private-key-formatting
	header, err := json.Marshal(struct{ Kid string }{credentials.APIKeyID})
	if err != nil {
		// ok: no-private-key-formatting
		return errors.New("encode header")
	}
	// ok: no-private-key-formatting
	claims, err := json.Marshal(struct{ Sub string }{credentials.APIKeyID})
	if err != nil {
		// ok: no-private-key-formatting
		return errors.New("encode claims")
	}
	digest := sha256.Sum256(append(header, claims...))
	// ok: no-private-key-formatting
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		// ok: no-private-key-formatting
		return errors.New("sign JWT")
	}
	token := fmt.Sprintf("%x.%x", r.Bytes(), s.Bytes())
	// ok: no-private-key-formatting
	_, err = json.Marshal(struct{ JWT string }{token})
	return err
}
