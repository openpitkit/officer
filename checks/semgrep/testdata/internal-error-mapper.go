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

package checks

func scheduleError(err error) error {
	switch {
	case errors.Is(err, scheduler.ErrInvalid), errors.Is(err, scheduler.ErrUnknownKind):
		// ruleid: internal-error-mapper
		return fmt.Errorf("reset schedule: %v: %w", err, domain.ErrInvalid)
	case errors.Is(err, scheduler.ErrConflict):
		// ok: internal-error-mapper
		return fmt.Errorf("schedule changed: %w", domain.ErrConflict)
	default:
		return err
	}
}

func mapError(err error) error {
	if errors.Is(err, worker.ErrBroken) || errors.Is(err, worker.ErrClosed) {
		// ruleid: internal-error-mapper
		return fmt.Errorf("worker: %v: %w", err, domain.ErrInvalid)
	} else if errors.Is(err, worker.ErrMissing) {
		// ruleid: internal-error-mapper
		return domain.ErrInvalid
	}
	return err
}

func preserveDomain(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		// ok: internal-error-mapper
		return fmt.Errorf("input: %w", domain.ErrInvalid)
	default:
		return err
	}
}

func maskWorker(err error) error {
	if errors.Is(err, worker.ErrBroken) {
		// ok: internal-error-mapper
		return domain.Internal(fmt.Errorf("worker: %v: %w", err, domain.ErrInvalid))
	}
	return err
}

func preserveWorker(err error) error {
	if errors.Is(err, worker.ErrBroken) {
		// ok: internal-error-mapper
		return errors.Join(err, domain.ErrConflict)
	}
	return err
}

func joinedMapper(err error) error {
	if errors.Is(err, worker.ErrBroken) {
		// ruleid: internal-error-mapper
		return errors.Join(err, domain.ErrInvalid)
	}
	return err
}
