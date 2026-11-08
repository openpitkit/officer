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

// Package ib provides shared TWS/Gateway client connection plumbing.
package ib

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/scmhub/ibapi"
)

const (
	// ConnectDrainTimeout bounds draining Connect after a timed-out disconnect.
	ConnectDrainTimeout = time.Second
	// ForceDisconnectTimeout bounds waiting for ibapi goroutines to stop.
	ForceDisconnectTimeout = 5 * time.Second
	// DefaultFallbackClientID is used for empty IDs; hashed IDs start above it.
	DefaultFallbackClientID = 1000
	ibapiDisabledLogLevel   = 7
)

// Client is the connection surface shared by TWS/Gateway callers.
type Client interface {
	Connect(host string, port int, clientID int64) error
	Disconnect() error
}

// ForceDisconnecter tears down clients even after a partial connection failure.
//
// IB partial-connect teardown via reflect+unsafe is a deliberate, documented
// workaround for scmhub/ibapi v0.10.44, not a pattern to copy.
//
// Why no public path exists: EClient.Connect starts the EReader
// scanner/decoder and the requester goroutines and opens the TCP socket BEFORE
// startAPI(). On a handshake/startAPI failure Connect returns an error with
// connState left at CONNECTING. The public Disconnect() early-returns on
// !IsConnected() (which requires CONNECTED), so it is a no-op in that state,
// and the scanner goroutine stays blocked in scanner.Scan() - only a physical
// socket close unblocks it. Nothing needed for teardown is exported: the cancel
// func, *Connection, connState, the tcpConn pointer, the isConnected flag and
// the WaitGroup are all unexported; NewEClient takes only an EWrapper (no
// net.Conn/dialer injection); neither Connect nor
// ConnectWithGracefulShutdown accepts a context. Confirmed absent in v0.10.44
// through v0.10.46. So we drive the library's own cancel -> close socket ->
// wg.Wait sequence by reaching the private fields.
//
// What this is bound to: the exact unexported field/method names of
// EClient/Connection in v0.10.44 - cancel, conn, connState, tcpConn,
// isConnected, wg. If any is renamed or removed in a newer ibapi, these helpers
// degrade to a logged errors.Join error instead of tearing down: the socket and
// goroutines then leak again, silently. Treat the ibapi version as effectively
// pinned to this hack; an upgrade is not safe without re-validating the fields.
//
// On every ibapi upgrade, re-check and switch to the clean path if it appears -
// any one of these removes the need for reflect/unsafe and lets this whole
// block be deleted in favor of a plain Disconnect():
//   - Connect cleans up after itself on partial failure (cancels goroutines and
//     closes the socket before returning the error);
//   - an exported ForceDisconnect()/Close(), or a Disconnect() that works
//     regardless of connState;
//   - a context-aware Connect, or a constructor/option that accepts a
//     net.Conn/dialer so officer owns and can close the socket itself.
type ForceDisconnecter interface {
	ForceDisconnect() error
}

func init() {
	ibapi.SetLogLevel(ibapiDisabledLogLevel)
}

// Connect applies a deadline and disconnects and drains the client on timeout.
// A timeout <= 0 calls the client directly without a deadline.
func Connect(
	ctx context.Context, client Client, host string, port int, clientID int64,
	timeout time.Duration,
) error {
	if timeout <= 0 {
		return client.Connect(host, port, clientID)
	}

	connectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result := make(chan error, 1)
	go func() {
		result <- client.Connect(host, port, clientID)
	}()

	select {
	case err := <-result:
		return err
	case <-connectCtx.Done():
		disconnectErr := Disconnect(client)
		select {
		//nolint:staticcheck // S1005: drain; return cancellation below.
		case _ = <-result:
		case <-time.After(ConnectDrainTimeout):
		}
		if disconnectErr != nil {
			return errors.Join(
				connectCtx.Err(),
				fmt.Errorf("ib: disconnect timed-out client: %w", disconnectErr),
			)
		}
		return connectCtx.Err()
	}
}

// Disconnect prefers ForceDisconnect when the client implements it.
func Disconnect(client Client) error {
	if force, ok := client.(ForceDisconnecter); ok {
		return force.ForceDisconnect()
	}
	return client.Disconnect()
}

// ForceDisconnect cancels the client, closes its socket, then waits boundedly
// for its goroutines. A nil client is a no-op. See ForceDisconnecter for the
// ibapi v0.10.44 private-field workaround this requires.
func ForceDisconnect(client *ibapi.EClient) error {
	if client == nil {
		return nil
	}
	value := reflect.ValueOf(client)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return nil
	}
	elem := value.Elem()
	return errors.Join(
		cancelIBClient(elem),
		closeIBClientSocket(elem),
		waitIBClientShutdown(elem),
	)
}

// Touches EClient.cancel (context.CancelFunc).
func cancelIBClient(client reflect.Value) error {
	cancelField := client.FieldByName("cancel")
	if !cancelField.IsValid() {
		return errors.New("ib: ibapi EClient.cancel field not found")
	}
	if cancelField.IsNil() {
		return nil
	}
	unsafeReflectValue(cancelField).Call(nil)
	return nil
}

// Touches EClient.conn, Connection.tcpConn, and Connection.isConnected.
func closeIBClientSocket(client reflect.Value) error {
	setIBInt32Field(client, "connState", int32(ibapi.DISCONNECTED))

	connField := client.FieldByName("conn")
	if !connField.IsValid() {
		return errors.New("ib: ibapi EClient.conn field not found")
	}
	if connField.IsNil() {
		return nil
	}
	conn := unsafeReflectValue(connField)
	connElem := conn.Elem()
	setIBInt32Field(connElem, "isConnected", 0)

	tcpField := connElem.FieldByName("tcpConn")
	if !tcpField.IsValid() {
		return errors.New("ib: ibapi Connection.tcpConn field not found")
	}
	swap := unsafeReflectValue(tcpField).Addr().MethodByName("Swap")
	if !swap.IsValid() {
		return errors.New("ib: ibapi Connection.tcpConn swap method not found")
	}
	result := swap.Call([]reflect.Value{
		reflect.Zero(reflect.TypeOf((*net.TCPConn)(nil))),
	})
	if len(result) != 1 || result[0].IsNil() {
		return nil
	}
	tcpConn, ok := result[0].Interface().(*net.TCPConn)
	if !ok {
		return errors.New("ib: ibapi Connection.tcpConn has unexpected type")
	}
	return tcpConn.Close()
}

// Touches EClient.wg, bounded by ForceDisconnectTimeout.
func waitIBClientShutdown(client reflect.Value) error {
	wgField := client.FieldByName("wg")
	if !wgField.IsValid() {
		return errors.New("ib: ibapi EClient.wg field not found")
	}
	wg := unsafeReflectValue(wgField)
	wait := wg.Addr().MethodByName("Wait")
	if !wait.IsValid() {
		return errors.New("ib: ibapi EClient.wg wait method not found")
	}
	done := make(chan struct{})
	go func() {
		wait.Call(nil)
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(ForceDisconnectTimeout):
		return errors.New("ib: timed out waiting for ibapi shutdown")
	}
}

// Generic private int32 access used for connState and isConnected.
func setIBInt32Field(value reflect.Value, name string, next int32) {
	field := value.FieldByName(name)
	if !field.IsValid() {
		return
	}
	atomic.StoreInt32((*int32)(unsafe.Pointer(field.UnsafeAddr())), next)
}

// Generic addressable access used for cancel, conn, tcpConn, and wg.
func unsafeReflectValue(value reflect.Value) reflect.Value {
	return reflect.NewAt(value.Type(), unsafe.Pointer(value.UnsafeAddr())).Elem()
}

// ClientID hashes the trimmed ID with FNV-32a into 1001..900999.
// An empty or whitespace-only ID returns DefaultFallbackClientID.
func ClientID(instanceID string) int64 {
	trimmed := strings.TrimSpace(instanceID)
	if trimmed == "" {
		return DefaultFallbackClientID
	}
	hash := fnv.New32a()
	hash.Write([]byte(trimmed))
	return DefaultFallbackClientID + 1 + int64(hash.Sum32()%899999)
}

// FatalError reports connection errors that prevent the client from continuing.
func FatalError(code int64) bool {
	switch code {
	case 502, 504, 1300:
		return true
	default:
		return false
	}
}

// InformationalError reports TWS status codes that do not indicate a failure.
func InformationalError(code int64) bool {
	switch code {
	case 1102, 2104, 2106, 2107, 2108, 2158:
		return true
	default:
		return false
	}
}

// RecoverableConnectionError reports connection loss codes handled by reconnect.
func RecoverableConnectionError(code int64) bool {
	switch code {
	case 1100, 1101, 2103, 2105, 2110:
		return true
	default:
		return false
	}
}

// ConnectError wraps a connection error with the endpoint and TWS setup advice.
func ConnectError(host string, port int, err error) error {
	return fmt.Errorf(
		"ib: connect to TWS/Gateway at %s:%d failed: %w; ensure the API socket is enabled, trusted for localhost, and listening on the configured paper/live port",
		host, port, err,
	)
}
