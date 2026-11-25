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

func (s *Service) unlockedLimits() error {
	// ruleid: barrier-write-lock
	s.node.PutRateLimit(ctx, limit, write, missing, caller)
	// ruleid: barrier-write-lock
	s.node.PutOrderSizeLimit(ctx, limit, write, missing, caller)
	// ruleid: barrier-write-lock
	s.node.PutPositionLimit(ctx, limit, write, missing, caller)
	// ruleid: barrier-write-lock
	s.node.PutSpotFundsPnlBoundsLimit(ctx, limit, write, missing, caller)
	// ruleid: barrier-write-lock
	s.node.SetSpotFundsLimitMode(ctx, mode, caller)
	// ruleid: barrier-write-lock
	return s.node.DeleteLimit(ctx, target, caller)
}

func (s *Service) unlockedEntities() {
	// ruleid: barrier-write-lock
	s.node.RestoreBackup(ctx, archive, options, caller)
	// ruleid: barrier-write-lock
	s.node.ResetDatabase(ctx, caller)
	// ruleid: barrier-write-lock
	s.node.UpdateAccount(ctx, code, account, caller)
	// ruleid: barrier-write-lock
	s.node.DeleteAccount(ctx, code, force, caller)
	// ruleid: barrier-write-lock
	s.node.UpdateGroup(ctx, code, group, caller)
	// ruleid: barrier-write-lock
	s.node.DeleteGroup(ctx, code, force, caller)
	// ruleid: barrier-write-lock
	s.node.UpdateAsset(ctx, code, asset, caller)
	// ruleid: barrier-write-lock
	s.node.DeleteAsset(ctx, code, force, caller)
}

func (service *Service) unlockedAlias() (marketdata.Sink, error) {
	n := service.node
	// ruleid: barrier-write-lock
	return n.PutRateLimit(ctx, limit, write, missing, caller)
}

func (s *Service) unlockedAssignedAlias() {
	var n node.Node
	n = s.node
	// ruleid: barrier-write-lock
	n.SetSpotFundsLimitMode(ctx, mode, caller)
}

func (s *Service) unlockedDeclaredAlias() error {
	var n = s.node
	// ruleid: barrier-write-lock
	return n.DeleteLimit(ctx, target, caller)
}

func (s *Service) unlockedTypedAlias() error {
	var n node.Node = s.node
	// ruleid: barrier-write-lock
	return n.DeleteGroup(ctx, code, force, caller)
}

func (s *Service) lockedDirect() error {
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()
	// ok: barrier-write-lock
	return s.node.DeleteAccount(ctx, code, force, caller)
}

func (service *Service) lockedAlias() (marketdata.Sink, error) {
	service.marketDataMu.Lock()
	defer service.marketDataMu.Unlock()
	n := service.node
	// ok: barrier-write-lock
	return n.PutOrderSizeLimit(ctx, limit, write, missing, caller)
}

func (s *Service) lockedWithoutResult() {
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()
	// ok: barrier-write-lock
	s.node.ResetDatabase(ctx, caller)
}

func (s *Service) writeBeforeLock() error {
	// ruleid: barrier-write-lock
	err := s.node.UpdateAsset(ctx, code, asset, caller)
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()
	return err
}

func (s *Service) aliasWriteBeforeLock() error {
	n := s.node
	// ruleid: barrier-write-lock
	err := n.DeleteAsset(ctx, code, force, caller)
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()
	return err
}

func (s *Service) aliasBeforeLock() error {
	n := s.node
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()
	// ok: barrier-write-lock
	return n.DeleteAsset(ctx, code, force, caller)
}

func (s *Service) namedResultsBeforeLock() (result T, resultErr error) {
	// ruleid: barrier-write-lock
	result, resultErr = s.node.UpdateAccount(ctx, code, account, caller)
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()
	return
}

func (s *Service) namedResultsAfterLock() (result T, resultErr error) {
	s.marketDataMu.Lock()
	defer s.marketDataMu.Unlock()
	// ok: barrier-write-lock
	return s.node.UpdateAccount(ctx, code, account, caller)
}

func (s *Service) readsAndUnrelatedWrites() {
	// ok: barrier-write-lock
	s.node.ListLimits(ctx, account)
	// ok: barrier-write-lock
	s.node.DeleteAssetClass(ctx, code, force, caller)
	// ok: barrier-write-lock
	other.PutRateLimit(ctx, limit, write, missing, caller)
}

func (s *OtherService) anotherType() {
	// ok: barrier-write-lock
	s.node.DeleteAccount(ctx, code, force, caller)
}

func freeWriter(s *Service) {
	// ok: barrier-write-lock
	s.node.DeleteLimit(ctx, target, caller)
}
