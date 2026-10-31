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

package ib

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/scmhub/ibapi"
)

// Readiness is a session's nextValidId barrier. Its zero value is not ready;
// construct it with NewReadiness and discard it when the session ends.
type Readiness struct {
	once  sync.Once
	ready chan struct{}
}

// NewReadiness creates a barrier for one TWS session.
func NewReadiness() *Readiness { return &Readiness{ready: make(chan struct{})} }

// Confirm releases waiters after nextValidId, never after ConnectAck.
func (r *Readiness) Confirm() { r.once.Do(func() { close(r.ready) }) }

// Wait bounds waiting for the API handshake and honors cancellation.
func (r *Readiness) Wait(ctx context.Context, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.ready:
		return nil
	case <-timer.C:
		return errors.New("ib: timed out waiting for TWS/Gateway API readiness")
	}
}

// ContractDetailsCollector joins contract callbacks until ContractDetailsEnd.
// It belongs to one session and suppresses duplicate end callbacks. Its zero
// value is usable; completed request ids are discarded with the session.
type ContractDetailsCollector struct {
	mu   sync.Mutex
	acc  map[int64][]*ibapi.ContractDetails
	done map[int64]bool
}

// Add retains a copy of a contract detail received for reqID.
func (c *ContractDetailsCollector) Add(reqID int64, detail *ibapi.ContractDetails) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done[reqID] {
		return
	}
	if c.acc == nil {
		c.acc = make(map[int64][]*ibapi.ContractDetails)
	}
	copy := *detail
	c.acc[reqID] = append(c.acc[reqID], &copy)
}

// Finish removes accumulated details and returns false for a duplicate end.
func (c *ContractDetailsCollector) Finish(reqID int64) ([]*ibapi.ContractDetails, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done[reqID] {
		return nil, false
	}
	if c.done == nil {
		c.done = make(map[int64]bool)
	}
	c.done[reqID] = true
	result := c.acc[reqID]
	delete(c.acc, reqID)
	return result, true
}
