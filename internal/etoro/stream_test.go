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

package etoro

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"go.openpit.dev/officer/internal/vendorhttp"
)

type memoryStreamWriter struct {
	ctx     context.Context
	typ     websocket.MessageType
	payload []byte
	err     error
	calls   int
}

func (w *memoryStreamWriter) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	w.ctx, w.typ, w.payload = ctx, typ, append([]byte(nil), p...)
	w.calls++
	return w.err
}

func TestStreamFrames(t *testing.T) {
	const id = "ED72693C-1545-4FA1-8A10-ACA7CF5419A6"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, tc := range []struct {
		name, want string
		write      func(vendorhttp.StreamWriter) error
	}{
		{"frame", `{"arbitrary":"frame"}`, func(w vendorhttp.StreamWriter) error {
			return WriteStreamFrame(ctx, w, struct {
				Value string `json:"arbitrary"`
			}{"frame"})
		}},
		{"authenticate", `{"id":"` + id + `","operation":"Authenticate","data":{"userKey":"user","apiKey":"api"}}`, func(w vendorhttp.StreamWriter) error {
			return WriteAuthenticate(ctx, w, id, Credentials{APIKey: "api", UserKey: "user"})
		}},
		{"subscribe-demo", `{"id":"` + id + `","operation":"Subscribe","data":{"topics":["private:demo"]}}`, func(w vendorhttp.StreamWriter) error { return WriteSubscribe(ctx, w, id, []string{"private:demo"}) }},
		{"subscribe-real", `{"id":"` + id + `","operation":"Subscribe","data":{"topics":["private"]}}`, func(w vendorhttp.StreamWriter) error { return WriteSubscribe(ctx, w, id, []string{"private"}) }},
		{"subscribe-multiple", `{"id":"` + id + `","operation":"Subscribe","data":{"topics":["private","private:demo"]}}`, func(w vendorhttp.StreamWriter) error {
			return WriteSubscribe(ctx, w, id, []string{"private", "private:demo"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer := &memoryStreamWriter{}
			if err := tc.write(writer); err != nil {
				t.Fatal("valid stream frame refused")
			}
			if writer.calls != 1 || writer.typ != websocket.MessageText || string(writer.payload) != tc.want || writer.ctx != ctx {
				t.Fatal("stream frame JSON, id, text type or context changed")
			}
		})
	}
}

func TestStreamRequiredInputs(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, want string
		write      func(vendorhttp.StreamWriter) error
	}{
		{"writer", "stream writer is required", func(vendorhttp.StreamWriter) error { return WriteStreamFrame(ctx, nil, struct{}{}) }},
		{"api-key", "apiKey is required", func(w vendorhttp.StreamWriter) error {
			return WriteAuthenticate(ctx, w, "id", Credentials{APIKey: " \t", UserKey: testCredentials.UserKey})
		}},
		{"user-key", "userKey is required", func(w vendorhttp.StreamWriter) error {
			return WriteAuthenticate(ctx, w, "id", Credentials{APIKey: testCredentials.APIKey, UserKey: " \n"})
		}},
		{"auth-id", "stream request id is required", func(w vendorhttp.StreamWriter) error { return WriteAuthenticate(ctx, w, " \t", testCredentials) }},
		{"subscribe-id", "stream request id is required", func(w vendorhttp.StreamWriter) error { return WriteSubscribe(ctx, w, " \t", []string{"private:demo"}) }},
		{"topics", "stream topics are required", func(w vendorhttp.StreamWriter) error { return WriteSubscribe(ctx, w, "id", nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer := &memoryStreamWriter{}
			err := tc.write(writer)
			if err == nil || !strings.Contains(err.Error(), tc.want) || writer.calls != 0 {
				t.Fatal("invalid stream input did not fail explicitly before writing")
			}
			assertNoCredentials(t, err.Error())
		})
	}
}

func TestStreamErrorsDoNotExposeCredentials(t *testing.T) {
	for name, spelling := range credentialSpellings(t) {
		t.Run(name, func(t *testing.T) {
			writer := &memoryStreamWriter{}
			err := WriteStreamFrame(context.Background(), writer, failingJSON(spelling))
			if err == nil {
				t.Fatal("stream encoding error was swallowed")
			}
			assertNoCredentials(t, err.Error())
			if err.Error() != "etoro: encode stream frame failed" || writer.calls != 0 {
				t.Fatal("stream encoding error exposed frame contents or wrote a frame")
			}
		})
	}
}

func TestStreamWriteErrorsPreserveCause(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		write func(vendorhttp.StreamWriter) error
	}{
		{"frame", func(w vendorhttp.StreamWriter) error { return WriteStreamFrame(ctx, w, testCredentials) }},
		{"authenticate", func(w vendorhttp.StreamWriter) error { return WriteAuthenticate(ctx, w, "id", testCredentials) }},
		{"subscribe", func(w vendorhttp.StreamWriter) error { return WriteSubscribe(ctx, w, "id", []string{"private:demo"}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer := &memoryStreamWriter{err: context.DeadlineExceeded}
			err := tc.write(writer)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("stream write must preserve context.DeadlineExceeded")
			}
			assertNoCredentials(t, err.Error())
			if err.Error() != "etoro: write stream frame: context deadline exceeded" {
				t.Fatal("stream write error lost its operation or cause text")
			}
		})
	}
}
