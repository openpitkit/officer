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
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"go.openpit.dev/officer/internal/apikey"
	"go.openpit.dev/officer/internal/backoff"
)

func reconnect() {
	for attempt := 0; ; attempt++ {
		// ruleid: shared-venue-stream-lifecycle
		time.Sleep(backoff.Delay(attempt, time.Second, time.Minute))
	}
}

func reconnectWithFixedDelay(ctx context.Context) {
	for {
		// ruleid: shared-venue-stream-lifecycle
		backoff.Sleep(ctx, 2*time.Second)
	}
}

func reconnectDelayFunction() {
	// ruleid: shared-venue-stream-lifecycle
	delay := backoff.Delay
	_ = delay
}

type retryPolicy struct{}

func (retryPolicy) Delay(attempt int) time.Duration {
	return time.Duration(attempt) * time.Second
}

func retryWithLocalPolicy() {
	retry := retryPolicy{}
	// ok: shared-venue-stream-lifecycle
	time.Sleep(retry.Delay(1))
}

func ParseCredentials(raw json.RawMessage) (string, string, error) {
	var fields map[string]string
	// ruleid: shared-vendor-credentials
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", "", err
	}
	// ruleid: shared-vendor-credentials
	key := fields["apiKey"]
	// ruleid: shared-vendor-credentials
	secret := fields["apiSecret"]
	return key, secret, nil
}

func ParseCredentials(raw string) (map[string]string, error) {
	var fields map[string]string
	// ruleid: shared-vendor-credentials
	decoder := json.NewDecoder(strings.NewReader(raw))
	err := decoder.Decode(&fields)
	return fields, err
}

func ParseCredentials(raw json.RawMessage) (apikey.Credentials, error) {
	// ok: shared-vendor-credentials
	return apikey.Parse(raw, "vendor")
}

func ParseCredentials(raw json.RawMessage) (Credentials, error) {
	// ok: shared-vendor-credentials
	parsed, err := apikey.ParseWithSecretField(raw, "vendor", "secretKey")
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{Key: parsed.APIKey, Secret: parsed.APISecret}, nil
}

func decodePayload(raw json.RawMessage) error {
	var fields map[string]string
	// ok: shared-vendor-credentials
	return json.Unmarshal(raw, &fields)
}

func NewVendorConnector(raw string) (string, string, error) {
	var fields map[string]string
	// ok: shared-vendor-credentials
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return "", "", err
	}
	// ruleid: shared-vendor-credentials
	key := fields["apiKey"]
	// ruleid: shared-vendor-credentials
	secret := fields["apiSecret"]
	return key, secret, nil
}

func inspectCredentialField(field string) {
	// ruleid: shared-vendor-credentials
	_ = field != "apiKey"
	switch field {
	// ruleid: shared-vendor-credentials
	case "apiSecret":
		return
	}
}

func outboundCredentials(values url.Values, key string) {
	// ok: shared-vendor-credentials
	_ = map[string]string{"apiKey": key}
	// ok: shared-vendor-credentials
	values.Set("apiKey", key)
}

type vendorConfig struct {
	// ok: shared-vendor-credentials
	APIKey string `json:"apiKey"`
	// ok: shared-vendor-credentials
	APISecret string `json:"apiSecret"`
}
