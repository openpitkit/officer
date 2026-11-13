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

package store

import (
	"context"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
)

// AccountPnlResetStore is an explicit capability for atomic snapshot and audit
// persistence after an engine reset. It is not a required RealmStore method.
type AccountPnlResetStore interface {
	// AccountPnlResetRecorded checks the immutable account identity and run key,
	// so a retry cannot erase fills accepted after the original reset.
	AccountPnlResetRecorded(context.Context, domain.EngineAccountID, string) (bool, error)
	// RecordAccountPnlReset atomically writes authoritative zero and the audit.
	RecordAccountPnlReset(context.Context, domain.Account, string, AuditEntry) error
}

// BackupRollbackStore captures only the scope a local restore may roll back.
// Unlike an operator export it may omit unrelated live reset receipts: this
// snapshot never transfers progress to another installation or account state.
// It is optional and does not change the shared RealmStore contract.
type BackupRollbackStore interface {
	CaptureBackupRollback(context.Context, backup.Scope) (backup.Archive, error)
}
