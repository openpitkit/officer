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

package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/secret"
)

func TestLoadMasterKey(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytesOf(0x11, 32))
	otherKey := base64.StdEncoding.EncodeToString(bytesOf(0x22, 32))
	wrongLengthKey := base64.StdEncoding.EncodeToString(bytesOf(0x33, 31))
	nonBase64Key := "not-base64!"

	type fileFixture struct {
		name    string
		value   string
		missing bool
	}

	tests := []struct {
		name                   string
		args                   []string
		env                    map[string]string
		environmentFile        *fileFixture
		flagFile               *fileFixture
		wantKey                string
		wantLoadErrorSource    string
		wantResolveErrorSource string
		secrets                []string
	}{
		{name: "none configured"},
		{name: "environment", env: map[string]string{EnvMasterKey: key}, wantKey: key},
		{name: "file", environmentFile: &fileFixture{name: "master-key", value: key}, wantKey: key},
		{name: "flag file", flagFile: &fileFixture{name: "master-key", value: key}, wantKey: key},
		{
			name:            "same environment and file",
			env:             map[string]string{EnvMasterKey: key},
			environmentFile: &fileFixture{name: "master-key", value: key},
			wantKey:         key,
		},
		{
			name:                   "different environment and file",
			env:                    map[string]string{EnvMasterKey: key},
			environmentFile:        &fileFixture{name: "master-key", value: otherKey},
			wantResolveErrorSource: "environment variable and master key file",
			secrets:                []string{key, otherKey},
		},
		{
			name:                   "wrong length environment key",
			env:                    map[string]string{EnvMasterKey: wrongLengthKey},
			wantResolveErrorSource: "master key environment variable",
			secrets:                []string{wrongLengthKey},
		},
		{
			name:                   "non-base64 environment key",
			env:                    map[string]string{EnvMasterKey: nonBase64Key},
			wantResolveErrorSource: "master key environment variable",
			secrets:                []string{nonBase64Key},
		},
		{
			name:                   "empty environment key with valid file",
			env:                    map[string]string{EnvMasterKey: ""},
			environmentFile:        &fileFixture{name: "master-key", value: key},
			wantResolveErrorSource: "master key environment variable",
			secrets:                []string{key},
		},
		{
			name:                   "malformed environment key with valid file",
			env:                    map[string]string{EnvMasterKey: nonBase64Key},
			environmentFile:        &fileFixture{name: "master-key", value: key},
			wantResolveErrorSource: "master key environment variable",
			secrets:                []string{nonBase64Key, key},
		},
		{
			name:                   "missing file",
			flagFile:               &fileFixture{name: "missing-master-key", missing: true},
			wantResolveErrorSource: "master key file",
		},
		{
			name:                   "empty file",
			environmentFile:        &fileFixture{name: "master-key"},
			wantResolveErrorSource: "master key file",
		},
		{
			name:                   "valid environment key with missing file",
			env:                    map[string]string{EnvMasterKey: key},
			environmentFile:        &fileFixture{name: "missing-master-key", missing: true},
			wantResolveErrorSource: "master key file",
			secrets:                []string{key},
		},
		{
			name:                   "valid environment key with empty file",
			env:                    map[string]string{EnvMasterKey: key},
			environmentFile:        &fileFixture{name: "master-key"},
			wantResolveErrorSource: "master key file",
			secrets:                []string{key},
		},
		{
			name:                "empty environment file setting",
			env:                 map[string]string{EnvMasterKeyFile: ""},
			wantLoadErrorSource: "master key file",
		},
		{
			name:                "empty flag file setting",
			args:                []string{"-master-key-file", ""},
			wantLoadErrorSource: "master key file",
		},
		{
			name:     "flag file beats empty environment file setting",
			env:      map[string]string{EnvMasterKeyFile: ""},
			flagFile: &fileFixture{name: "flag-master-key", value: key},
			wantKey:  key,
		},
		{
			name:            "flag file beats environment file",
			environmentFile: &fileFixture{name: "environment-master-key", value: otherKey},
			flagFile:        &fileFixture{name: "flag-master-key", value: key},
			wantKey:         key,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			env := make(map[string]string, len(tt.env)+1)
			for name, value := range tt.env {
				env[name] = value
			}
			writeFile := func(fixture *fileFixture) string {
				path := filepath.Join(tempDir, fixture.name)
				if fixture.missing {
					return path
				}
				if err := os.WriteFile(path, []byte(fixture.value), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				return path
			}
			if tt.environmentFile != nil {
				env[EnvMasterKeyFile] = writeFile(tt.environmentFile)
			}
			args := append([]string{"-mode", "mcp"}, tt.args...)
			if tt.flagFile != nil {
				args = append(args, "-master-key-file", writeFile(tt.flagFile))
			}

			cfg, err := Load(args, envLookup(env))
			if tt.wantLoadErrorSource != "" {
				if err == nil {
					t.Fatal("Load() error = nil")
				}
				if !strings.Contains(err.Error(), tt.wantLoadErrorSource) {
					t.Fatalf("Load() error = %v, want source %q", err, tt.wantLoadErrorSource)
				}
				for _, material := range tt.secrets {
					if strings.Contains(err.Error(), material) {
						t.Fatalf("Load() error exposes key material: %v", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}

			masterKey, err := ResolveMasterKey(cfg, envLookup(env))
			if tt.wantResolveErrorSource != "" {
				if err == nil {
					t.Fatal("ResolveMasterKey() error = nil")
				}
				if !strings.Contains(err.Error(), tt.wantResolveErrorSource) {
					t.Fatalf("ResolveMasterKey() error = %v, want source %q", err, tt.wantResolveErrorSource)
				}
				for _, material := range tt.secrets {
					if strings.Contains(err.Error(), material) {
						t.Fatalf("ResolveMasterKey() error exposes key material: %v", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveMasterKey() error = %v", err)
			}
			if tt.wantKey == "" {
				if masterKey != nil {
					t.Fatal("ResolveMasterKey() returned a key, want none")
				}
				return
			}
			if masterKey == nil {
				t.Fatal("ResolveMasterKey() returned no key")
			}
			expected, err := secret.ParseMasterKey(tt.wantKey)
			if err != nil {
				t.Fatalf("ParseMasterKey(want): %v", err)
			}
			equal, err := masterKey.Equal(expected)
			if err != nil {
				t.Fatalf("MasterKey.Equal: %v", err)
			}
			if !equal {
				t.Fatal("ResolveMasterKey() returned the wrong key")
			}
		})
	}
}

func bytesOf(value byte, length int) []byte {
	result := make([]byte, length)
	for i := range result {
		result[i] = value
	}
	return result
}

func envLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
