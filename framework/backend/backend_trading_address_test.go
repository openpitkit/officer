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

package backend

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

func TestTradingOpenAlgoPaddedAddresses(t *testing.T) {
	s, n, rt := newTradingTestService(t)
	c := tradingTestConnection()
	c.Provider = domain.TradingProviderOpenAlgo
	const address = " \thttp://127.0.0.1:5000/path \n"
	const wsAddress = " \nws://127.0.0.1:8765\t "
	const key = "test-address-key-marker"
	credentials, err := json.Marshal(map[string]string{
		"url": address, "wsUrl": wsAddress, "apiKey": key,
	})
	if err != nil {
		t.Fatal("cannot encode test credentials")
	}
	c.Credentials = string(credentials)
	wantSettings := map[string]any{"url": address, "wsUrl": wsAddress}
	if !reflect.DeepEqual(TradingConnectionSettings(c), wantSettings) {
		t.Fatal("padded addresses not classified as their stored settings")
	}
	n.connections = []domain.TradingConnection{c}
	status, err := s.GetTrading(systemCtx())
	if err != nil || len(status.Connections) != 1 {
		t.Fatal("cannot get trading connection status")
	}
	got := status.Connections[0]
	if !reflect.DeepEqual(got.Settings, wantSettings) {
		t.Error("status settings lost stored address whitespace")
	}
	if !reflect.DeepEqual(got.Secrets, map[string]bool{"apiKey": true}) {
		t.Error("address classified as secret or API key omitted")
	}
	rt.checkErr = errors.New("connect " + address + " " + wsAddress + " " + key)
	_, err = s.CheckTradingConnection(systemCtx(), c.ExternalID.String())
	want := "connect " + address + " " + wsAddress + " [redacted]"
	if !errors.Is(err, domain.ErrUpstream) || !strings.Contains(err.Error(), want) ||
		strings.Contains(err.Error(), key) {
		t.Fatal("backend error hid an address or exposed the credential")
	}
}
