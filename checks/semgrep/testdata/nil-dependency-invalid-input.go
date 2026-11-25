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

func (s *Service) Send(req *Request) error {
	if req == nil {
		// ok: nil-dependency-invalid-input
		return fmt.Errorf("request missing: %w", domain.ErrInvalid)
	}
	if s.sender == nil {
		// ruleid: nil-dependency-invalid-input
		return fmt.Errorf("sender missing: %w", domain.ErrInvalid)
	}
	if req.Account == nil {
		// ok: nil-dependency-invalid-input
		return fmt.Errorf("account missing: %w", domain.ErrInvalid)
	}
	return nil
}

func (s *Service) Call() (bool, error) {
	if nil == s.callback || s.store == nil {
		// ruleid: nil-dependency-invalid-input
		return false, domain.ErrInvalid
	}
	return true, nil
}

func (r *Registry) Register(p Provider) error {
	if r == nil || p.Type == "" || p.Build == nil {
		// ruleid: nil-dependency-invalid-input
		return fmt.Errorf("provider type and builder required: %w", domain.ErrInvalid)
	}
	return nil
}

func (s *Service) Internal() error {
	if s.sender == nil {
		// ok: nil-dependency-invalid-input
		return fmt.Errorf("sender missing")
	}
	return nil
}

func (s *Service) Bare() error {
	if s.callback == nil {
		// ruleid: nil-dependency-invalid-input
		return domain.ErrInvalid
	}
	return nil
}

func (s *Service) Masked() error {
	if s.callback == nil {
		// ok: nil-dependency-invalid-input
		return domain.Internal(fmt.Errorf("callback missing: %w", domain.ErrInvalid))
	}
	return nil
}

func NewRequest(payload *Payload) (*Request, error) {
	if payload.Account == nil {
		// ok: nil-dependency-invalid-input
		return nil, fmt.Errorf("account missing: %w", domain.ErrInvalid)
	}
	return &Request{}, nil
}

func (in Input) Validate() error {
	if in.X == nil {
		// ok: nil-dependency-invalid-input
		return errors.Join(errors.New("X is required"), domain.ErrInvalid)
	}
	return nil
}

func (s *Service) Joined() error {
	if s.store == nil {
		// ruleid: nil-dependency-invalid-input
		return errors.Join(errors.New("store missing"), domain.ErrInvalid)
	}
	return nil
}
