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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"go.openpit.dev/officer/internal/vendorhttp"
)

func privateHexSign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	// ruleid: shared-vendor-http-sign
	return hex.EncodeToString(mac.Sum(nil))
}

func privateHexSignTwoStatements(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	sum := mac.Sum(nil)
	// ruleid: shared-vendor-http-sign
	return hex.EncodeToString(sum)
}

func privateHexSignSprintf(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	// ruleid: shared-vendor-http-sign
	return fmt.Sprintf("%x", mac.Sum(nil))
}

func privateHexSignVar(payload, secret string) string {
	var mac = hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	// ruleid: shared-vendor-http-sign
	return hex.EncodeToString(mac.Sum(nil))
}

func privateBase64Sign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	// ok: shared-vendor-http-sign
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func sharedHexSign(payload, secret string) string {
	// ok: shared-vendor-http-sign
	return vendorhttp.SignHex(payload, secret)
}
