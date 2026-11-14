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

package backend

import (
	"context"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
)

func (s *Service) GetTestExecutionPolicy(ctx context.Context, id string) (domain.TestExecutionPolicyState, error) {
	if _, err := auth.CallerFromContext(ctx); err != nil {
		return domain.TestExecutionPolicyState{}, err
	}
	c, err := s.testTradingConnection(ctx, id)
	if err != nil {
		return domain.TestExecutionPolicyState{}, err
	}
	return s.node.GetTestExecutionPolicy(ctx, c.ExternalID)
}

func (s *Service) ReplaceTestExecutionPolicy(ctx context.Context, id string, policy domain.TestExecutionPolicy) (domain.TestExecutionPolicyState, error) {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return domain.TestExecutionPolicyState{}, err
	}
	c, err := s.testTradingConnection(ctx, id)
	if err != nil {
		return domain.TestExecutionPolicyState{}, err
	}
	if err := domain.ValidateTestExecutionPolicy(policy); err != nil {
		return domain.TestExecutionPolicyState{}, err
	}
	return s.node.ReplaceTestExecutionPolicy(ctx, c.ExternalID, policy, caller)
}

func (s *Service) testTradingConnection(ctx context.Context, id string) (domain.TradingConnection, error) {
	c, err := s.tradingConnection(ctx, id)
	if err != nil {
		return domain.TradingConnection{}, err
	}
	if err := s.requireTradingRuntime(); err != nil {
		return domain.TradingConnection{}, err
	}
	class, err := domain.ProviderSupportsExecutionPolicy(s.trading.SupportsExecutionPolicy, string(c.Provider))
	if err != nil {
		return domain.TradingConnection{}, err
	}
	if class {
		return c, nil
	}
	return domain.TradingConnection{}, domain.ErrExecutionPolicyUnsupported
}
