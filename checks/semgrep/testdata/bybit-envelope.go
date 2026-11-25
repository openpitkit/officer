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

type incompleteEnvelope struct {
	// ruleid: bybit-retcode-required
	RetCode int `json:"retCode"`
}

type alignedEnvelope struct {
	// ruleid: bybit-retcode-required
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
}

// Keep the one-line regex fixture stable under gofmt.
// ruleid: bybit-retcode-required
// type inlineEnvelope struct{ RetCode int `json:"retCode"` }

type taggedEnvelopes struct {
	// ruleid: bybit-retcode-required
	ExtraBefore int `yaml:"rc" json:"retCode"`
	// ruleid: bybit-retcode-required
	ExtraAfter int `json:"retCode" yaml:"retCode"`
	// ruleid: bybit-retcode-required
	OmitEmpty int `json:"retCode,omitempty"`
	// ruleid: bybit-retcode-required
	RetCode int `json:"retcode"`
}

type integerEnvelopes struct {
	// ruleid: bybit-retcode-required
	Signed8 int8 `json:"retCode"`
	// ruleid: bybit-retcode-required
	Signed16 int16 `json:"retCode"`
	// ruleid: bybit-retcode-required
	Signed32 int32 `json:"retCode"`
	// ruleid: bybit-retcode-required
	Signed64 int64 `json:"retCode"`
	// ruleid: bybit-retcode-required
	Unsigned uint `json:"retCode"`
	// ruleid: bybit-retcode-required
	Unsigned8 uint8 `json:"retCode"`
	// ruleid: bybit-retcode-required
	Unsigned16 uint16 `json:"retCode"`
	// ruleid: bybit-retcode-required
	Unsigned32 uint32 `json:"retCode"`
	// ruleid: bybit-retcode-required
	Unsigned64 uint64 `json:"retCode"`
	// ruleid: bybit-retcode-required
	Rune rune `json:"retCode"`
}

type requiredEnvelope struct {
	// ok: bybit-retcode-required
	RetCode *int `json:"retCode"`
}

type optionalEnvelope struct {
	// ok: bybit-retcode-required
	RetCode *int `json:"retCode,omitempty"`
}

type errorValue struct {
	// ok: bybit-retcode-required
	RetCode int
}

type yamlEnvelope struct {
	// ok: bybit-retcode-required
	RetCode int `yaml:"retCode"`
}

type myJSONEnvelope struct {
	// ok: bybit-retcode-required
	RetCode int `myjson:"retCode"`
}
