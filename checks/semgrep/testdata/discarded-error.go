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

package fixture

import "io"

func f() error { return nil }

// ruleid: discarded-error
var _ = f()

func discardedError(f func() error) {
	// ruleid: discarded-error
	_ = f()
}

func discardedPair(f func() (int, error)) {
	// ruleid: discarded-error
	_, _ = f()
}

func discardedTriple(f func() (int, int, error)) {
	// ruleid: discarded-error
	_, _, _ = f()
}

func discardedMethod(w io.Writer, b []byte) {
	// ruleid: discarded-error
	_, _ = w.Write(b)
}

func discardedMultiline(f func(string) (int, error)) {
	// ruleid: discarded-error
	_, _ = f(
		"data",
	)
}

func discardedInGoroutine(f func() (int, error)) {
	go func() {
		// ruleid: discarded-error
		_, _ = f()
	}()
}

func discardedInDefer(f func() (int, error)) {
	defer func() {
		// ruleid: discarded-error
		_, _ = f()
	}()
}

func discardedQuadruple(f func() (int, int, int, error)) {
	// ruleid: discarded-error
	_, _, _, _ = f()
}

func discardedCalls(first, second func() error) {
	// ruleid: discarded-error
	_, _ = first(), second()
}

func discardedVar(f func() (int, error)) {
	// ruleid: discarded-error
	var _, _ = f()
}

func discardedSingleVar(f func() error) {
	// ruleid: discarded-error
	var _ = f()
}

func nonCallDiscards(ch <-chan int, m map[string]int, k string, x any) {
	// ok: discarded-error
	_, _ = <-ch
	// ok: discarded-error
	_, _ = m[k]
	// ok: discarded-error
	_, _ = x.(int)
}

func nonCallDiscardsWithCalls(done func() <-chan int, m map[int]int,
	key func(int) int, k int, f func() any, dst, src []int,
	values func() []int) {
	// ok: discarded-error
	_ = <-done()
	// ok: discarded-error
	_, _ = <-done()
	// ok: discarded-error
	_ = m[key(k)]
	// ok: discarded-error
	_, _ = m[key(k)]
	// ok: discarded-error
	_ = f().(int)
	// ok: discarded-error
	_, _ = f().(int)
	// ok: discarded-error
	_ = dst[len(src)-1]
	// ok: discarded-error
	_ = src[key(k):len(src)]
	// ok: discarded-error
	_ = src[key(k):]
	// ok: discarded-error
	_ = src[:len(src)]
	// ok: discarded-error
	_ = values()[:]
	// ok: discarded-error
	_ = src[key(k):len(src):cap(src)]
	// ok: discarded-error
	_ = src[:len(src):cap(src)]
}

func partialDiscards(f func() (int, error)) {
	var n int
	var err error
	// ok: discarded-error
	n, _ = f()
	// ok: discarded-error
	_, err = f()
	_, _ = n, err
}

func exemptClose(f io.Closer) {
	// ok: discarded-error
	_ = f.Close()
}

func exemptCloseWithArguments(conn interface{ Close(int, string) error }) {
	// ok: discarded-error
	_ = conn.Close(1000, "done")
}

func blankDeclaration(f func() io.Closer) {
	// ok: discarded-error
	var _ io.Closer = f()
}

func handledError(f func() error) error {
	// ok: discarded-error
	err := f()
	if err != nil {
		return err
	}
	return nil
}
