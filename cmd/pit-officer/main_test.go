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
// Please see https://openpit.dev and the OWNERS file for details.

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
)

func TestServiceLifecycleHandlerAuditsBeforeAccepting(t *testing.T) {
	t.Parallel()
	requests := make(chan lifecycleAction, 1)
	var recordedAction lifecycleAction
	var recordedSource domain.Source
	controller := &serviceLifecycleController{
		requests: requests,
		record: func(
			_ context.Context,
			action lifecycleAction,
			source domain.Source,
		) error {
			recordedAction = action
			recordedSource = source
			return nil
		},
	}
	handler := controller.handler(lifecycleRestart)

	req := httptest.NewRequest(http.MethodPost, "/app/api/v1/service/restart", nil)
	req = req.WithContext(auth.ContextWithCaller(req.Context(), domain.Caller{
		Source:    domain.SourcePanel,
		Principal: domain.PrincipalOperator,
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if recordedAction != lifecycleRestart || recordedSource != domain.SourcePanel {
		t.Fatalf("recorded = (%q, %q), want restart from panel",
			recordedAction, recordedSource)
	}
	select {
	case got := <-requests:
		if got != lifecycleRestart {
			t.Fatalf("request = %q, want %q", got, lifecycleRestart)
		}
	default:
		t.Fatalf("lifecycle request was not queued")
	}
}

func TestServiceLifecycleHandlerRejectsWhenAuditFails(t *testing.T) {
	t.Parallel()
	requests := make(chan lifecycleAction, 1)
	controller := &serviceLifecycleController{
		requests: requests,
		record: func(context.Context, lifecycleAction, domain.Source) error {
			return errors.New("audit failed")
		},
	}
	handler := controller.handler(lifecycleRestart)

	req := httptest.NewRequest(http.MethodPost, "/app/api/v1/service/restart", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	select {
	case got := <-requests:
		t.Fatalf("queued lifecycle request despite audit failure: %q", got)
	default:
	}
}

func TestRestartProcessArgsPinsHTTPAddr(t *testing.T) {
	t.Parallel()
	const addr = "127.0.0.1:54321"
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "append",
			in:   []string{"pit-officer", "serve", "-sqlite-path", "db.sqlite"},
			want: []string{
				"pit-officer", "serve", "-sqlite-path", "db.sqlite",
				"-http-addr", addr, "-open-browser=false",
			},
		},
		{
			name: "replace separate short flag",
			in: []string{
				"pit-officer", "serve", "-http-addr", "127.0.0.1:0",
			},
			want: []string{
				"pit-officer", "serve", "-http-addr", addr,
				"-open-browser=false",
			},
		},
		{
			name: "replace separate long flag",
			in: []string{
				"pit-officer", "serve", "--http-addr", "127.0.0.1:0",
			},
			want: []string{
				"pit-officer", "serve", "--http-addr", addr,
				"-open-browser=false",
			},
		},
		{
			name: "replace joined short flag",
			in:   []string{"pit-officer", "serve", "-http-addr=127.0.0.1:0"},
			want: []string{
				"pit-officer", "serve", "-http-addr=" + addr,
				"-open-browser=false",
			},
		},
		{
			name: "replace joined long flag",
			in:   []string{"pit-officer", "serve", "--http-addr=127.0.0.1:0"},
			want: []string{
				"pit-officer", "serve", "--http-addr=" + addr,
				"-open-browser=false",
			},
		},
		{
			name: "complete missing value",
			in:   []string{"pit-officer", "serve", "-http-addr"},
			want: []string{
				"pit-officer", "serve", "-http-addr", addr,
				"-open-browser=false",
			},
		},
		{
			name: "override open browser",
			in: []string{
				"pit-officer", "serve", "-open-browser=true",
				"-http-addr", "127.0.0.1:0",
			},
			want: []string{
				"pit-officer", "serve", "-open-browser=true",
				"-http-addr", addr, "-open-browser=false",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			original := append([]string(nil), tt.in...)
			got := restartProcessArgs(tt.in, addr)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("restartProcessArgs() = %#v, want %#v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.in, original) {
				t.Fatalf("restartProcessArgs mutated input: %#v", tt.in)
			}
		})
	}
}
