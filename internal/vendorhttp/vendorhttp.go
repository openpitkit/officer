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

// Package vendorhttp shares redirect refusal and request errors for vendor
// clients. Redactors come from secret.CredentialRedactor.
package vendorhttp

import (
	"net/http"
	"strings"
)

// RefuseRedirect returns the redirect response without following its Location.
func RefuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// CopyClient copies client and overwrites its redirect policy. Nil stays nil.
func CopyClient(client *http.Client) *http.Client {
	if client == nil {
		return nil
	}
	copy := *client
	copy.CheckRedirect = RefuseRedirect
	return &copy
}

type requestError struct {
	message string
	cause   error
}

func (e *requestError) Error() string { return e.message }
func (e *requestError) Unwrap() error { return e.cause }

// Wrap redacts the request message and retains the raw error as its cause.
// The caller must supply its connection's non-nil credential redactor.
func Wrap(redactor *strings.Replacer, vendor, operation string, err error) error {
	return NewRequestError(redactor.Replace(vendor+": "+operation+": "+err.Error()), err)
}

// NewRequestError retains an already redacted message and a caller-chosen cause.
func NewRequestError(message string, cause error) error {
	return &requestError{message: message, cause: cause}
}
