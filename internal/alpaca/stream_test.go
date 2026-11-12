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

package alpaca

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/coder/websocket"
)

// recordingWriter keeps the last message written.
type recordingWriter struct {
	writes  int
	typ     websocket.MessageType
	payload []byte
}

func (w *recordingWriter) Write(_ context.Context, typ websocket.MessageType, p []byte) error {
	w.writes++
	w.typ, w.payload = typ, p
	return nil
}

func TestWriteStreamAuth(t *testing.T) {
	var conn recordingWriter
	if err := WriteStreamAuth(context.Background(), &conn, testCredentials); err != nil {
		t.Fatal(err)
	}
	if conn.writes != 1 || conn.typ != websocket.MessageText {
		t.Fatal("auth was not written as one text message")
	}
	var frame map[string]string
	want := map[string]string{"action": "auth", "key": testCredentials.APIKey, "secret": testCredentials.APISecret}
	if json.Unmarshal(conn.payload, &frame) != nil || !reflect.DeepEqual(frame, want) {
		t.Fatal("auth frame is not exactly action, key and secret with the given credentials")
	}
}
