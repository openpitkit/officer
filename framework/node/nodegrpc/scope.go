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

// Package nodegrpc provides a realm-bound gRPC health probe for node.Node.
// It uses the framework's JSON types. The composition supplies the scope and
// lease validator; this package contains no placement, lease, or authorization
// policy. Its RPC surface is Health.
package nodegrpc

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"google.golang.org/grpc/metadata"
)

// ErrStaleFencingToken identifies a request made with an obsolete lease token.
var ErrStaleFencingToken = errors.New("stale fencing token")

// FencingToken identifies one lease generation. Zero is invalid.
type FencingToken uint64

// BoundScope identifies the dataset, partition, and lease of one client.
type BoundScope struct {
	Realm        domain.RealmID
	Shard        node.ShardID
	FencingToken FencingToken
}

// ScopeValidator checks ownership and the current fencing token before dispatch.
// Implementations must honor cancellation and return ErrStaleFencingToken when
// the lease token is obsolete.
type ScopeValidator interface {
	Validate(context.Context, BoundScope) error
}

const (
	realmHeader = "pit-realm-bin"
	shardHeader = "pit-shard-bin"
	fenceHeader = "pit-fencing-token"
)

func validateScope(scope BoundScope) error {
	if !utf8.ValidString(string(scope.Realm)) || !utf8.ValidString(string(scope.Shard)) {
		return errors.New("scope must contain valid UTF-8")
	}
	if err := domain.ValidateRealmID(scope.Realm); err != nil {
		return fmt.Errorf("scope realm: %w", err)
	}
	if scope.Shard == "" {
		return errors.New("scope shard is required")
	}
	if scope.FencingToken == 0 {
		return errors.New("scope fencing token is required")
	}
	return nil
}

func incomingScope(ctx context.Context) (BoundScope, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return BoundScope{}, errors.New("scope metadata is required")
	}
	var fields [3]string
	for i, name := range []string{realmHeader, shardHeader, fenceHeader} {
		values := md.Get(name)
		if len(values) != 1 || values[0] == "" {
			return BoundScope{}, fmt.Errorf("exactly one %s value is required", name)
		}
		fields[i] = values[0]
	}
	token, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil || strconv.FormatUint(token, 10) != fields[2] {
		return BoundScope{}, errors.New("invalid scope fencing token")
	}
	scope := BoundScope{
		Realm: domain.RealmID(fields[0]), Shard: node.ShardID(fields[1]), FencingToken: FencingToken(token),
	}
	if err := validateScope(scope); err != nil {
		return BoundScope{}, err
	}
	return scope, nil
}
