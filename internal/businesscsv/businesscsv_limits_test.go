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

package businesscsv

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"

	"go.openpit.dev/officer/internal/domain"
)

func TestDecodeZipWithLimit_RejectsDeclaredUncompressedSize(t *testing.T) {
	t.Parallel()
	payload := zipPayload(t, "accounts.csv", "12345")

	_, err := decodeZipWithLimit(payload, 4)
	if !errors.Is(err, domain.ErrTooLarge) {
		t.Fatalf("decodeZipWithLimit error = %v, want too large", err)
	}
}

func TestReadLimitedImportContent_RejectsExceededExtraction(t *testing.T) {
	t.Parallel()

	_, err := readLimitedImportContent(strings.NewReader("12345"), 4)
	if !errors.Is(err, domain.ErrTooLarge) {
		t.Fatalf("readLimitedImportContent error = %v, want too large", err)
	}
}

func TestReadLimitedImportContent_AcceptsAtLimit(t *testing.T) {
	t.Parallel()

	body, err := readLimitedImportContent(strings.NewReader("1234"), 4)
	if err != nil {
		t.Fatalf("readLimitedImportContent: %v", err)
	}
	if string(body) != "1234" {
		t.Fatalf("body = %q, want 1234", body)
	}
}

func zipPayload(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatalf("Create(%s): %v", name, err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatalf("Write(%s): %v", name, err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}
