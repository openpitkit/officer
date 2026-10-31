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

package trading

import (
	"context"
	"errors"
	"sync"

	"go.openpit.dev/officer/internal/ib"
)

type ibProbeLock struct {
	token chan struct{}
	users int
}

var ibProbeLocks = struct {
	sync.Mutex
	entries map[int64]*ibProbeLock
}{entries: make(map[int64]*ibProbeLock)}

func lockIBProbe(ctx context.Context, id int64) (func(), error) {
	ibProbeLocks.Lock()
	entry := ibProbeLocks.entries[id]
	if entry == nil {
		entry = &ibProbeLock{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		ibProbeLocks.entries[id] = entry
	}
	entry.users++
	ibProbeLocks.Unlock()
	releaseUser := func() {
		ibProbeLocks.Lock()
		entry.users--
		if entry.users == 0 {
			delete(ibProbeLocks.entries, id)
		}
		ibProbeLocks.Unlock()
	}
	select {
	case <-ctx.Done():
		releaseUser()
		return nil, ctx.Err()
	case <-entry.token:
		return func() { entry.token <- struct{}{}; releaseUser() }, nil
	}
}

func (c *ibConnector) probe(ctx context.Context, use func(context.Context, *ibTradingSession) error) (err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("ib: connector is closed")
	}
	c.probes.Add(1)
	c.mu.Unlock()
	defer c.probes.Done()
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	id := c.clientID + ibProbeClientIDOffset
	unlock, err := lockIBProbe(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	s := newIBTradingSession(ctx, c)
	s.clientID = id
	defer s.cancel()
	s.client = c.newClient(s)
	defer func() { err = errors.Join(err, ib.Disconnect(s.client)) }()
	if err := ib.Connect(ctx, s.client, c.credentials.Host, c.credentials.Port, id, c.timeout); err != nil {
		return ib.ConnectError(c.credentials.Host, c.credentials.Port, err)
	}
	if err := s.readiness.Wait(ctx, c.timeout); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return use(ctx, s)
}
