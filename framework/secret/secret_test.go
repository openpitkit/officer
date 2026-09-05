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

package secret

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	key := testMasterKey(t, 0x11)
	aad := testAAD(t, "realm-1", "signing_key", "private_key", "key-1")
	plaintext := []byte("private material")

	sealed, err := Seal(key, aad, plaintext)
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	if len(sealed) == 0 || sealed[0] != sealedVersion {
		t.Fatalf("Seal() did not produce a version %d envelope", sealedVersion)
	}

	got, err := Open(key, aad, sealed)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatal("Open() returned different plaintext")
	}
}

// TestOpenVersionOneKnownAnswer pins the on-disk contract of version 0x01.
// A deliberate format change must bump the version byte rather than update this vector.
func TestOpenVersionOneKnownAnswer(t *testing.T) {
	key, err := ParseMasterKey("QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI=")
	if err != nil {
		t.Fatalf("ParseMasterKey() error = %v", err)
	}
	aad, err := BuildAAD("realm-vector", "market_data_instance", "credentials", "instance-vector")
	if err != nil {
		t.Fatalf("BuildAAD() error = %v", err)
	}
	sealed, err := base64.StdEncoding.DecodeString("AVX1Yz2lGFiU9MFfNcwd9GdY/7+6Xr6o7naluDdYUmuKsWhu/uUdN/bFJvHLYm/41qdT+RvwUGo4G/yEQ7tG")
	if err != nil {
		t.Fatalf("decode sealed value: %v", err)
	}

	got, err := Open(key, aad, sealed)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if !bytes.Equal(got, []byte("pinned operator secret")) {
		t.Fatal("Open() returned different plaintext")
	}
}

func TestSealUsesFreshNonce(t *testing.T) {
	key := testMasterKey(t, 0x12)
	aad := testAAD(t, "realm-1", "signing_key", "private_key", "key-1")
	plaintext := []byte("private material")

	first := testSeal(t, key, aad, plaintext)
	second := testSeal(t, key, aad, plaintext)
	if bytes.Equal(first, second) {
		t.Fatal("Seal() produced equal envelopes")
	}
	if bytes.Equal(first[1:1+chachaNonceSizeForTest], second[1:1+chachaNonceSizeForTest]) {
		t.Fatal("Seal() produced equal nonces")
	}
	for name, sealed := range map[string][]byte{"first": first, "second": second} {
		got, err := Open(key, aad, sealed)
		if err != nil {
			t.Fatalf("Open(%s) error = %v", name, err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("Open(%s) returned different plaintext", name)
		}
	}
}

func TestOpenRejectsCorruptedTag(t *testing.T) {
	key := testMasterKey(t, 0x22)
	aad := testAAD(t, "realm-1", "signing_key", "private_key", "key-1")
	sealed := testSeal(t, key, aad, []byte("private material"))
	sealed[len(sealed)-1] ^= 0x01

	if got, err := Open(key, aad, sealed); err == nil || got != nil {
		t.Fatalf("Open() returned data or no error: %v", err)
	}
}

func TestOpenRejectsUnsupportedVersion(t *testing.T) {
	key := testMasterKey(t, 0x33)
	aad := testAAD(t, "realm-1", "signing_key", "private_key", "key-1")
	sealed := testSeal(t, key, aad, []byte("private material"))
	sealed[0] = 0x02

	if got, err := Open(key, aad, sealed); err == nil || got != nil {
		t.Fatalf("Open() returned data or no error: %v", err)
	}
}

func TestOpenRejectsTruncatedValues(t *testing.T) {
	key := testMasterKey(t, 0x44)
	aad := testAAD(t, "realm-1", "signing_key", "private_key", "key-1")
	minimumSize := 1 + chachaNonceSizeForTest + chachaTagSizeForTest

	for _, size := range []int{0, 1, minimumSize - 1} {
		t.Run(testNameForSize(size), func(t *testing.T) {
			if got, err := Open(key, aad, make([]byte, size)); err == nil || got != nil {
				t.Fatalf("Open() returned data or no error: %v", err)
			}
		})
	}
}

func TestOpenRejectsDifferentAADComponents(t *testing.T) {
	key := testMasterKey(t, 0x55)
	aad := testAAD(t, "realm-1", "market_data_instance", "credentials", "instance-1")
	sealed := testSeal(t, key, aad, []byte("provider credential"))

	tests := []struct {
		name       string
		realmID    string
		tableName  string
		columnName string
		rowID      string
	}{
		{name: "realm", realmID: "realm-2", tableName: "market_data_instance", columnName: "credentials", rowID: "instance-1"},
		{name: "table", realmID: "realm-1", tableName: "other_table", columnName: "credentials", rowID: "instance-1"},
		{name: "column", realmID: "realm-1", tableName: "market_data_instance", columnName: "other_column", rowID: "instance-1"},
		{name: "row ID", realmID: "realm-1", tableName: "market_data_instance", columnName: "credentials", rowID: "instance-2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			otherAAD := testAAD(t, tt.realmID, tt.tableName, tt.columnName, tt.rowID)
			if got, err := Open(key, otherAAD, sealed); err == nil || got != nil {
				t.Fatalf("Open() returned data or no error: %v", err)
			}
		})
	}
}

func TestOpenRejectsDifferentMasterKey(t *testing.T) {
	firstKey := testMasterKey(t, 0x56)
	secondKey := testMasterKey(t, 0x57)
	aad := testAAD(t, "realm-1", "signing_key", "private_key", "key-1")
	sealed := testSeal(t, firstKey, aad, []byte("private material"))

	if got, err := Open(secondKey, aad, sealed); err == nil || got != nil {
		t.Fatalf("Open() returned data or no error: %v", err)
	}
}

func TestBuildAADIsInjectiveAcrossComponentBoundaries(t *testing.T) {
	first := testAAD(t, "ab", "c", "column", "row")
	second := testAAD(t, "a", "bc", "column", "row")

	if bytes.Equal(first, second) {
		t.Fatal("BuildAAD() produced equal encodings for different component tuples")
	}
}

func TestBuildAADRejectsEmptyComponents(t *testing.T) {
	tests := []struct {
		name       string
		realmID    string
		tableName  string
		columnName string
		rowID      string
	}{
		{name: "realm", tableName: "table", columnName: "column", rowID: "row"},
		{name: "table", realmID: "realm", columnName: "column", rowID: "row"},
		{name: "column", realmID: "realm", tableName: "table", rowID: "row"},
		{name: "row ID", realmID: "realm", tableName: "table", columnName: "column"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := BuildAAD(tt.realmID, tt.tableName, tt.columnName, tt.rowID); err == nil || got != nil {
				t.Fatalf("BuildAAD() = (%v, %v), want (nil, error)", got, err)
			}
		})
	}
}

func TestParseMasterKey(t *testing.T) {
	tests := []struct {
		name    string
		encoded string
		wantErr bool
	}{
		{name: "exactly 32 bytes", encoded: encodedKey(0x00, 32)},
		{name: "too short", encoded: encodedKey(0x11, 31), wantErr: true},
		{name: "too long", encoded: encodedKey(0x11, 33), wantErr: true},
		{name: "non-base64", encoded: "not base64", wantErr: true},
		{name: "empty", encoded: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := ParseMasterKey(tt.encoded)
			if tt.wantErr {
				if err == nil {
					t.Fatal("ParseMasterKey() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMasterKey() error = %v", err)
			}
			if !key.valid() {
				t.Fatal("ParseMasterKey() returned an invalid key")
			}
		})
	}
}

func TestParseMasterKeyFileTrimsWhitespace(t *testing.T) {
	encoded := encodedKey(0x66, 32)
	fromFile, err := ParseMasterKeyFile([]byte(" \t\n" + encoded + "\n\r "))
	if err != nil {
		t.Fatalf("ParseMasterKeyFile() error = %v", err)
	}
	direct, err := ParseMasterKey(encoded)
	if err != nil {
		t.Fatalf("ParseMasterKey() error = %v", err)
	}
	equal, err := fromFile.Equal(direct)
	if err != nil {
		t.Fatalf("Equal() error = %v", err)
	}
	if !equal {
		t.Fatal("file and direct parsers returned different keys")
	}
}

func TestParseMasterKeyAcceptsNonCanonicalEncoding(t *testing.T) {
	canonical := encodedKey(0x66, 32)
	nonCanonical := canonical[:len(canonical)-2] + "Z="

	canonicalKey, err := ParseMasterKey(canonical)
	if err != nil {
		t.Fatalf("ParseMasterKey(canonical) error = %v", err)
	}
	nonCanonicalKey, err := ParseMasterKey(nonCanonical)
	if err != nil {
		t.Fatalf("ParseMasterKey(non-canonical) error = %v", err)
	}
	equal, err := canonicalKey.Equal(nonCanonicalKey)
	if err != nil {
		t.Fatalf("Equal() error = %v", err)
	}
	if !equal {
		t.Fatal("canonical and non-canonical encodings returned different keys")
	}
}

func TestParseMasterKeyFileRejectsEmptyContent(t *testing.T) {
	if key, err := ParseMasterKeyFile([]byte(" \n\t")); err == nil || key.valid() {
		t.Fatalf("ParseMasterKeyFile() returned a usable key with error %v", err)
	}
}

func TestMasterKeyEqual(t *testing.T) {
	key := testMasterKey(t, 0x77)
	same := testMasterKey(t, 0x77)
	different := testMasterKey(t, 0x78)

	equal, err := key.Equal(same)
	if err != nil || !equal {
		t.Fatalf("Equal(same) = (%v, %v), want (true, nil)", equal, err)
	}
	equal, err = key.Equal(different)
	if err != nil || equal {
		t.Fatalf("Equal(different) = (%v, %v), want (false, nil)", equal, err)
	}
}

func TestZeroMasterKeyIsRejected(t *testing.T) {
	var zero MasterKey
	aad := testAAD(t, "realm-1", "signing_key", "private_key", "key-1")
	valid := testMasterKey(t, 0x88)
	sealed := testSeal(t, valid, aad, []byte("private material"))

	if got, err := Seal(zero, aad, []byte("private material")); err == nil || got != nil {
		t.Fatalf("Seal() returned data or no error: %v", err)
	}
	if got, err := Open(zero, aad, sealed); err == nil || got != nil {
		t.Fatalf("Open() returned data or no error: %v", err)
	}
	if got, err := KeyVerifier(zero); err == nil || got != nil {
		t.Fatalf("KeyVerifier() = (%v, %v), want (nil, error)", got, err)
	}
	if equal, err := zero.Equal(valid); err == nil || equal {
		t.Fatalf("Equal() = (%v, %v), want (false, error)", equal, err)
	}
	if equal, err := valid.Equal(zero); err == nil || equal {
		t.Fatalf("Equal() = (%v, %v), want (false, error)", equal, err)
	}
}

func TestKeyVerifier(t *testing.T) {
	firstKey := testMasterKey(t, 0x99)
	secondKey := testMasterKey(t, 0x9a)

	first, err := KeyVerifier(firstKey)
	if err != nil {
		t.Fatalf("KeyVerifier(first) error = %v", err)
	}
	again, err := KeyVerifier(firstKey)
	if err != nil {
		t.Fatalf("KeyVerifier(first again) error = %v", err)
	}
	second, err := KeyVerifier(secondKey)
	if err != nil {
		t.Fatalf("KeyVerifier(second) error = %v", err)
	}

	equal, err := KeyVerifiersEqual(first, again)
	if err != nil || !equal {
		t.Fatalf("KeyVerifiersEqual(same key) = (%v, %v), want (true, nil)", equal, err)
	}
	equal, err = KeyVerifiersEqual(first, second)
	if err != nil || equal {
		t.Fatalf("KeyVerifiersEqual(different keys) = (%v, %v), want (false, nil)", equal, err)
	}
}

func TestKeyVerifiersEqualRejectsWrongLengths(t *testing.T) {
	valid := make([]byte, keyVerifierSize)
	for _, tt := range []struct {
		name   string
		first  []byte
		second []byte
	}{
		{name: "empty first", first: nil, second: valid},
		{name: "short second", first: valid, second: make([]byte, keyVerifierSize-1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if equal, err := KeyVerifiersEqual(tt.first, tt.second); err == nil || equal {
				t.Fatalf("KeyVerifiersEqual() = (%v, %v), want (false, error)", equal, err)
			}
		})
	}
}

const (
	chachaNonceSizeForTest = 24
	chachaTagSizeForTest   = 16
)

func testMasterKey(t *testing.T, value byte) MasterKey {
	t.Helper()
	key, err := ParseMasterKey(encodedKey(value, 32))
	if err != nil {
		t.Fatalf("ParseMasterKey() error = %v", err)
	}
	return key
}

func encodedKey(value byte, size int) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{value}, size))
}

func testAAD(t *testing.T, realmID, tableName, columnName, rowID string) []byte {
	t.Helper()
	aad, err := BuildAAD(realmID, tableName, columnName, rowID)
	if err != nil {
		t.Fatalf("BuildAAD() error = %v", err)
	}
	return aad
}

func testSeal(t *testing.T, key MasterKey, aad, plaintext []byte) []byte {
	t.Helper()
	sealed, err := Seal(key, aad, plaintext)
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	return sealed
}

func testNameForSize(size int) string {
	if size == 0 {
		return "empty"
	}
	if size == 1 {
		return "version only"
	}
	return "missing tag byte"
}
