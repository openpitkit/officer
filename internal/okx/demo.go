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

package okx

import (
	"net/http"

	"go.openpit.dev/officer/internal/vendorhttp"
)

// DemoHTTPClient copies client and selects demo trading for all requests.
// It refuses redirects and never changes the supplied client or request.
func DemoHTTPClient(client *http.Client) *http.Client {
	c := vendorhttp.CopyClient(client)
	if c == nil {
		return nil
	}
	if c.Transport == nil {
		c.Transport = http.DefaultTransport
	}
	c.Transport = demoTransport{c.Transport}
	return c
}

type demoTransport struct{ base http.RoundTripper }

func (t demoTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	request := req.Clone(req.Context())
	request.Header.Set("x-simulated-trading", "1")
	return t.base.RoundTrip(request)
}
