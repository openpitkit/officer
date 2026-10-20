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

func redactLocalSecret(text, secret string) string {
	// ruleid: no-local-secret-redaction
	return strings.ReplaceAll(text, secret, "[secret]")
}

func scrubLocalCredential(text, credential string) string {
	// ruleid: no-local-secret-redaction
	return strings.ReplaceAll(text, credential, "[redacted]")
}

func redactOrdinaryText(text, word string) string {
	// ok: no-local-secret-redaction
	return strings.ReplaceAll(text, word, "replacement")
}

func replaceCredentialSetting(text, credential string) string {
	// ok: no-local-secret-redaction
	return strings.ReplaceAll(text, credential, "replacement")
}

func redactWithSharedAlgorithm(credentials, text string) (string, error) {
	// ok: no-local-secret-redaction
	_, replacer, err := secret.CredentialRedactor(credentials)
	if err != nil {
		return "", err
	}
	return replacer.Replace(text), nil
}
