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
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"go.openpit.dev/officer/framework/node"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const healthMethod = "/openpit.node.Health/Health"

type jsonCodec struct{}

func (jsonCodec) Name() string { return "json" }

func (jsonCodec) Marshal(value any) ([]byte, error) { return json.Marshal(value) }

func (jsonCodec) Unmarshal(data []byte, value any) error {
	if !utf8.Valid(data) || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("expected a non-null UTF-8 JSON value")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

// Client owns one connection bound to an immutable realm, shard, and lease token.
// Closing it releases that connection without stopping the remote node.
type Client struct {
	connection *grpc.ClientConn
	scope      BoundScope
	once       sync.Once
	closeErr   error
}

// Dial creates a health client for an explicit http or https origin. HTTP uses
// h2c and belongs on a trusted internal network. HTTPS verifies the server with
// the system trust store. The caller's context controls each Health request.
func Dial(ctx context.Context, address string, scope BoundScope) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	origin, err := url.Parse(address)
	if err != nil || origin.Hostname() == "" || origin.User != nil ||
		origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
		return nil, errors.New("node endpoint must be an origin without credentials, path, query, or fragment")
	}
	var transport credentials.TransportCredentials
	switch origin.Scheme {
	case "http":
		transport = insecure.NewCredentials()
	case "https":
		transport = credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: origin.Hostname(),
		})
	default:
		return nil, errors.New("node endpoint requires an explicit http or https scheme")
	}
	connection, err := grpc.NewClient(
		"dns:///"+origin.Host,
		grpc.WithTransportCredentials(transport),
		grpc.WithDisableRetry(),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(jsonCodec{}), grpc.MaxCallRecvMsgSize(1<<20)),
	)
	if err != nil {
		return nil, fmt.Errorf("create node connection: %w", err)
	}
	return &Client{connection: connection, scope: scope}, nil
}

// Health returns the remote engine and store health after scope validation.
func (client *Client) Health(ctx context.Context) (node.Health, error) {
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(
		realmHeader, string(client.scope.Realm),
		shardHeader, string(client.scope.Shard),
		fenceHeader, strconv.FormatUint(uint64(client.scope.FencingToken), 10),
	))
	var result node.Health
	if err := client.connection.Invoke(ctx, healthMethod, &struct{}{}, &result); err != nil {
		switch status.Code(err) {
		case codes.Canceled:
			return node.Health{}, errors.Join(context.Canceled, err)
		case codes.DeadlineExceeded:
			return node.Health{}, errors.Join(context.DeadlineExceeded, err)
		case codes.FailedPrecondition:
			if status.Convert(err).Message() == "stale_fencing_token" {
				return node.Health{}, errors.Join(ErrStaleFencingToken, err)
			}
		}
		return node.Health{}, err
	}
	return result, nil
}

// Close is an idempotent connection shutdown.
func (client *Client) Close() error {
	client.once.Do(func() { client.closeErr = client.connection.Close() })
	return client.closeErr
}

// Server exposes node health over gRPC and owns the transport lifecycle.
// The composition retains ownership of the target node and must close this
// server before closing its target.
type Server struct {
	rpc       *grpc.Server
	target    node.Node
	validator ScopeValidator
}

// NewServer requires a real node and a validator that runs before every probe.
func NewServer(target node.Node, validator ScopeValidator) (*Server, error) {
	if target == nil || validator == nil {
		return nil, errors.New("node target and scope validator are required")
	}
	server := &Server{target: target, validator: validator}
	server.rpc = grpc.NewServer(
		grpc.ForceServerCodec(jsonCodec{}),
		grpc.MaxRecvMsgSize(16<<10),
		grpc.MaxSendMsgSize(1<<20),
		grpc.WaitForHandlers(true),
	)
	server.rpc.RegisterService(&healthDescription, server)
	return server, nil
}

// Handler shares an HTTP listener with next, including cleartext HTTP/2.
func (server *Server) Handler(next http.Handler) (http.Handler, error) {
	if next == nil {
		return nil, errors.New("HTTP handler is required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			server.rpc.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}

// HTTPProtocols enables HTTP/1 probes and HTTP/2 RPC on a shared listener.
// Assign it to http.Server.Protocols before serving the Handler.
// Unencrypted HTTP/2 requires a trusted network or a TLS-terminating proxy.
func HTTPProtocols() *http.Protocols {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	return protocols
}

// Close cancels transport calls and waits for handlers to finish. It leaves the
// target's engine and store lifecycle to the composition.
func (server *Server) Close() error {
	server.rpc.Stop()
	return nil
}

func (server *Server) health(ctx context.Context) (node.Health, error) {
	scope, err := incomingScope(ctx)
	if err != nil {
		return node.Health{}, status.Error(codes.InvalidArgument, "invalid_scope")
	}
	if err := server.validator.Validate(ctx, scope); err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return node.Health{}, status.Error(codes.Canceled, "request_canceled")
		case errors.Is(err, context.DeadlineExceeded):
			return node.Health{}, status.Error(codes.DeadlineExceeded, "deadline_exceeded")
		case errors.Is(err, ErrStaleFencingToken):
			return node.Health{}, status.Error(codes.FailedPrecondition, "stale_fencing_token")
		default:
			return node.Health{}, status.Error(codes.PermissionDenied, "scope_rejected")
		}
	}
	result, err := server.target.Health(ctx)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return node.Health{}, status.Error(codes.Canceled, "request_canceled")
		case errors.Is(err, context.DeadlineExceeded):
			return node.Health{}, status.Error(codes.DeadlineExceeded, "deadline_exceeded")
		default:
			return node.Health{}, status.Error(codes.Internal, "node_health_failed")
		}
	}
	return result, nil
}

type healthService interface {
	health(context.Context) (node.Health, error)
}

var healthDescription = grpc.ServiceDesc{
	ServiceName: "openpit.node.Health",
	HandlerType: (*healthService)(nil),
	Methods:     []grpc.MethodDesc{{MethodName: "Health", Handler: serveHealth}},
}

func serveHealth(
	service any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor,
) (any, error) {
	if err := decode(&struct{}{}); err != nil {
		return nil, err
	}
	target, ok := service.(healthService)
	if !ok {
		return nil, status.Error(codes.Internal, "invalid_health_service")
	}
	if interceptor == nil {
		return target.health(ctx)
	}
	return interceptor(ctx, &struct{}{}, &grpc.UnaryServerInfo{
		Server: service, FullMethod: healthMethod,
	}, func(ctx context.Context, _ any) (any, error) {
		return target.health(ctx)
	})
}
