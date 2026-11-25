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

import (
	"errors"
	"io/fs"
)

type commitFailure struct{}

func (commitFailure) Error() string { return "commit failed" }

type commitFailureX struct{}

func (commitFailureX) Error() string { return "commit failed x" }

type readFailure struct{}

func (readFailure) Error() string { return "read failed" }

type otherFailure struct{}

func (otherFailure) Error() string { return "other failed" }

type node struct{}

func mixedCaseAfterMultiCase(err error) error {
	switch err.(type) {
	// ok: error-type-one-form
	case readFailure, otherFailure:
		return nil
	// ruleid: error-type-one-form
	case commitFailure, *commitFailure:
		return err
	}
	return nil
}

func qualifiedPair(err error) error {
	switch err.(type) {
	// ruleid: error-type-one-form
	case fs.PathError, *fs.PathError:
		return err
	}
	return nil
}

func prefixName(err error) error {
	switch err.(type) {
	// ok: error-type-one-form
	case commitFailure, *commitFailureX:
		return err
	}
	return nil
}

func funcLiteralPair() func(error) bool {
	return func(err error) bool {
		// ruleid: error-type-one-form
		var value commitFailure
		if errors.As(err, &value) {
			return true
		}
		var pointer *commitFailure
		return errors.As(err, &pointer)
	}
}

func wrapCommitFailure(err error) error {
	switch err.(type) {
	// ruleid: error-type-one-form
	case commitFailure,
		*commitFailure:
		return err
	default:
		return commitFailure{}
	}
}

func (node) administrativeFailure(err error) error {
	switch err.(type) {
	// ruleid: error-type-one-form
	case commitFailure, *commitFailure:
		return err
	}
	return nil
}

func (node) executionFailure(err error) error {
	switch err.(type) {
	// ruleid: error-type-one-form
	case commitFailure, *commitFailure:
		return err
	}
	return nil
}

func reverseCaseOrder(err error) error {
	switch failure := err.(type) {
	// ruleid: error-type-one-form
	case *commitFailure, readFailure, commitFailure:
		return err
	default:
		return failure
	}
}

func isCommitFailure(err error) bool {
	// ruleid: error-type-one-form
	var value commitFailure
	if errors.As(err, &value) {
		return true
	}
	var pointer *commitFailure
	return errors.As(err, &pointer)
}

func (node) isCommitFailurePointerFirst(err error) bool {
	// ruleid: error-type-one-form
	var pointer *commitFailure
	if errors.As(err, &pointer) {
		return true
	}
	var value commitFailure
	return errors.As(err, &value)
}

func singleForm(err error) bool {
	switch err.(type) {
	// ok: error-type-one-form
	case commitFailure:
		return true
	}
	return false
}

func distinctForms(err error) bool {
	switch err.(type) {
	// ok: error-type-one-form
	case commitFailure, *readFailure:
		return true
	}
	return false
}

func nestedValueSwitch(err error) bool {
	switch err.(type) {
	// ok: error-type-one-form
	case readFailure, *commitFailure:
		value := &commitFailure{}
		switch err {
		// ok: error-type-one-form
		case value, *value:
			return true
		}
	}
	return false
}

func singleProbe(err error) bool {
	// ok: error-type-one-form
	var value commitFailure
	return errors.As(err, &value)
}

func distinctProbes(err error) bool {
	// ok: error-type-one-form
	var value commitFailure
	var pointer *readFailure
	return errors.As(err, &value) || errors.As(err, &pointer)
}

func unprobedPointer(err error) *commitFailure {
	// ok: error-type-one-form
	var value commitFailure
	var pointer *commitFailure
	var other *readFailure
	if errors.As(err, &value) || errors.As(err, &other) {
		return nil
	}
	return pointer
}
