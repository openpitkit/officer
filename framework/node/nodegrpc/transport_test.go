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

package nodegrpc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type healthTarget struct {
	node.Node
	probe func(context.Context) (node.Health, error)
}

func (target healthTarget) Health(ctx context.Context) (node.Health, error) {
	return target.probe(ctx)
}

type scopeCheck func(context.Context, BoundScope) error

func (check scopeCheck) Validate(ctx context.Context, scope BoundScope) error {
	return check(ctx, scope)
}

var testScope = BoundScope{Realm: "realm-α", Shard: "partition", FencingToken: 9}

func checkTestScope(_ context.Context, scope BoundScope) error {
	if scope.Realm != testScope.Realm || scope.Shard != testScope.Shard {
		return errors.New("scope mismatch")
	}
	if scope.FencingToken != testScope.FencingToken {
		return ErrStaleFencingToken
	}
	return nil
}

func startTestServer(t *testing.T, target node.Node) (*Server, string) {
	t.Helper()
	server, err := NewServer(target, scopeCheck(checkTestScope))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := server.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewUnstartedServer(handler)
	httpServer.Config.Protocols = HTTPProtocols()
	httpServer.Start()
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
		httpServer.Close()
	})
	return server, httpServer.URL
}

func testClient(t *testing.T, address string) *Client {
	t.Helper()
	client, err := Dial(context.Background(), address, testScope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	return client
}

func TestHealthSharesHTTPListenerAndUsesBoundScope(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	expected := node.Health{Engine: engine.Health{Running: true}, Store: store.StoreHealth{Reachable: true}}
	_, address := startTestServer(t, healthTarget{probe: func(context.Context) (node.Health, error) {
		calls.Add(1)
		return expected, nil
	}})
	client := testClient(t, address)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(realmHeader, "wrong", fenceHeader, "1"))
	actual, err := client.Health(ctx)
	if err != nil || !actual.Engine.Running || !actual.Store.Reachable || calls.Load() != 1 {
		t.Fatalf("Health = %+v, %v; calls = %d", actual, err, calls.Load())
	}
	response, err := http.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("HTTP status = %d", response.StatusCode)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	second := testClient(t, address)
	if _, err := second.Health(ctx); err != nil {
		t.Fatalf("client shutdown stopped the remote node: %v", err)
	}
}

func TestInvalidMetadataNeverReachesTheNode(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	_, address := startTestServer(t, healthTarget{probe: func(context.Context) (node.Health, error) {
		calls.Add(1)
		return node.Health{}, nil
	}})
	client := testClient(t, address)
	valid := metadata.Pairs(realmHeader, string(testScope.Realm), shardHeader, string(testScope.Shard), fenceHeader, "9")
	for _, tc := range []struct {
		name   string
		change func(metadata.MD)
		code   codes.Code
	}{
		{"missing", func(md metadata.MD) { md.Delete(realmHeader) }, codes.InvalidArgument},
		{"duplicate", func(md metadata.MD) { md.Append(realmHeader, string(testScope.Realm)) }, codes.InvalidArgument},
		{"empty", func(md metadata.MD) { md.Set(shardHeader, "") }, codes.InvalidArgument},
		{"noncanonical token", func(md metadata.MD) { md.Set(fenceHeader, "09") }, codes.InvalidArgument},
		{"foreign realm", func(md metadata.MD) { md.Set(realmHeader, "foreign") }, codes.PermissionDenied},
		{"foreign shard", func(md metadata.MD) { md.Set(shardHeader, "foreign") }, codes.PermissionDenied},
		{"stale token", func(md metadata.MD) { md.Set(fenceHeader, "8") }, codes.FailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			md := valid.Copy()
			tc.change(md)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = metadata.NewOutgoingContext(ctx, md)
			var health node.Health
			err := client.connection.Invoke(ctx, healthMethod, &struct{}{}, &health)
			if status.Code(err) != tc.code {
				t.Fatalf("status = %s, want %s: %v", status.Code(err), tc.code, err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("rejected scopes reached the node %d times", calls.Load())
	}
	stale := testScope
	stale.FencingToken--
	staleClient, err := Dial(context.Background(), address, stale)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := staleClient.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := staleClient.Health(ctx); !errors.Is(err, ErrStaleFencingToken) {
		t.Fatalf("stale fencing token did not survive RPC: %v", err)
	}
}

func TestDeadlineAndServerCloseCancelTheNode(t *testing.T) {
	for _, closeServer := range []bool{false, true} {
		name := "deadline"
		if closeServer {
			name = "server close"
		}
		t.Run(name, func(t *testing.T) {
			entered := make(chan struct{})
			exited := make(chan struct{})
			server, address := startTestServer(t, healthTarget{probe: func(ctx context.Context) (node.Health, error) {
				close(entered)
				defer close(exited)
				<-ctx.Done()
				return node.Health{}, ctx.Err()
			}})
			client := testClient(t, address)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := client.Health(ctx); result <- err }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("probe did not reach the node")
			}
			if closeServer {
				if err := server.Close(); err != nil {
					t.Fatal(err)
				}
				select {
				case <-exited:
				default:
					t.Fatal("Close returned before the node handler exited")
				}
			}
			select {
			case err := <-result:
				if err == nil || (!closeServer && !errors.Is(err, context.DeadlineExceeded)) {
					t.Fatalf("Health error = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("RPC did not stop")
			}
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation did not reach the node")
			}
		})
	}
}

func FuzzScopeRejectsBeforeDispatch(f *testing.F) {
	f.Add("realm-α", "partition", "9", false)
	f.Add("realm-α", "partition", "9", true)
	f.Add("foreign", "partition", "9", false)
	f.Add("realm-α", "partition", "09", false)
	f.Fuzz(func(t *testing.T, realm, shard, token string, duplicate bool) {
		calls := 0
		server := &Server{
			target:    healthTarget{probe: func(context.Context) (node.Health, error) { calls++; return node.Health{}, nil }},
			validator: scopeCheck(checkTestScope),
		}
		md := metadata.Pairs(realmHeader, realm, shardHeader, shard, fenceHeader, token)
		if duplicate {
			md.Append(realmHeader, realm)
		}
		_, err := server.health(metadata.NewIncomingContext(context.Background(), md))
		allowed := realm == string(testScope.Realm) && shard == string(testScope.Shard) && token == "9" && !duplicate
		if allowed && (err != nil || calls != 1) {
			t.Fatalf("valid scope rejected: %v; calls = %d", err, calls)
		}
		if !allowed && (err == nil || calls != 0) {
			t.Fatalf("invalid scope dispatched: %v; calls = %d", err, calls)
		}
	})
}
