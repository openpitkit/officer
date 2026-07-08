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

package node

import (
	"context"

	"go.openpit.dev/officer/framework/domain"
)

func (r *memoryRealm) UpsertSigningKey(_ context.Context, key domain.SigningKey) error {
	r.signingKeys[key.KeyID] = key
	return nil
}

func (r *memoryRealm) GetActiveSigningKey(
	context.Context,
) (domain.SigningKey, bool, error) {
	for _, key := range r.signingKeys {
		if key.Active {
			return key, true, nil
		}
	}
	return domain.SigningKey{}, false, nil
}

func (r *memoryRealm) GetSigningKey(
	_ context.Context, keyID string,
) (domain.SigningKey, error) {
	key, ok := r.signingKeys[keyID]
	if !ok {
		return domain.SigningKey{}, domain.ErrNotFound
	}
	return key, nil
}

func (r *memoryRealm) ListSigningKeys(context.Context) ([]domain.SigningKey, error) {
	out := make([]domain.SigningKey, 0, len(r.signingKeys))
	for _, key := range r.signingKeys {
		key.PrivateKey = nil
		out = append(out, key)
	}
	return out, nil
}

func (r *memoryRealm) DeactivateAllSigningKeys(context.Context) error {
	for id, key := range r.signingKeys {
		key.Active = false
		r.signingKeys[id] = key
	}
	return nil
}

func (r *memoryRealm) GetSigningConfig(
	_ context.Context, key string,
) (string, bool, error) {
	value, ok := r.signingConfig[key]
	return value, ok, nil
}

func (r *memoryRealm) SetSigningConfig(_ context.Context, key, value string) error {
	r.signingConfig[key] = value
	return nil
}

func (r *memoryRealm) ListMcpAccess(context.Context) (map[string]bool, error) {
	out := make(map[string]bool, len(r.mcpAccess))
	for key, value := range r.mcpAccess {
		out[key] = value
	}
	return out, nil
}

func (r *memoryRealm) SetMcpAccess(
	_ context.Context, command string, enabled bool,
) error {
	r.mcpAccess[command] = enabled
	return nil
}

func (r *memoryRealm) GetUserSetting(
	_ context.Context, userID, key string,
) (string, bool, error) {
	setting, ok := r.userSettings[settingKey(userID, key)]
	return setting.Value, ok, nil
}

func (r *memoryRealm) SetUserSetting(
	_ context.Context, userID, key, value string,
) error {
	r.userSettings[settingKey(userID, key)] = domain.UserSetting{
		UserID: userID,
		Key:    key,
		Value:  value,
	}
	return nil
}

func (r *memoryRealm) ListUserSettings(context.Context) ([]domain.UserSetting, error) {
	out := make([]domain.UserSetting, 0, len(r.userSettings))
	for _, setting := range r.userSettings {
		out = append(out, setting)
	}
	return out, nil
}
