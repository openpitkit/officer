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

// Skeleton tests for the rebuilt SQLite store: schema migration and foreign-key
// enforcement, single-realm ForRealm enforcement, the shared external-id and
// engine-id helpers, and group-1 (asset, principal, account groups, account)
// round-trips by code. The table groups still stubbed are not covered here.

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	fwstore "go.openpit.dev/officer/framework/store"
)

// rawDB returns the realm's shared connection pool directly, for tests that
// reach past the RealmStore surface to seed or inspect rows with raw SQL. It is
// the test-only counterpart of the guarded db() accessor and assumes the store
// is open (these tests never race Close); the guarded accessor's closed-store
// behaviour is covered by TestRealmDataOpAfterCloseReturnsClosedError.
func (r *realmStore) rawDB() *sql.DB { return r.store.currentDB() }

// newTestStore opens a fresh migrated SQLite store in a temp file and returns it
// with its default realm handle.
func newTestStore(t *testing.T) (Store, RealmStore) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	s, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	rs, err := s.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm(default): %v", err)
	}
	return s, rs
}

func TestMigrateAppliesSchemaAndForeignKeys(t *testing.T) {
	ctx := context.Background()
	s, rs := newTestStore(t)

	version, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != 1 {
		t.Fatalf("schema version = %d, want 1", version)
	}

	// Foreign-key enforcement must be ON: inserting an account whose group link
	// points at a non-existent group surrogate id must be rejected. We reach this
	// through the public path by referencing an unknown group code, which the
	// connector rejects as ErrInvalid before any insert; to prove the PRAGMA
	// itself, run a raw insert with an impossible group_id and expect a failure.
	sq, ok := s.(*sqliteStore)
	if !ok {
		t.Fatalf("store is not *sqliteStore")
	}
	_, err = sq.currentDB().ExecContext(
		ctx,
		`INSERT INTO account (code, group_id) VALUES ('x', 999999)`,
	)
	if err == nil {
		t.Fatal("expected foreign-key violation inserting dangling group_id, got nil")
	}

	// The store handle is usable: an empty dictionary lists as a non-nil empty
	// slice.
	assets, err := rs.ListAssets(ctx)
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	if assets == nil {
		t.Fatal("ListAssets returned nil, want non-nil empty slice")
	}
	if len(assets) != 0 {
		t.Fatalf("ListAssets len = %d, want 0", len(assets))
	}
}

func TestEnumDictionariesCoverDomainCodes(t *testing.T) {
	s, _ := newTestStore(t)
	dictionaries, err := s.(*sqliteStore).enumDictionaries()
	if err != nil {
		t.Fatalf("enumDictionaries: %v", err)
	}

	assertDictionaryCodes := func(table string, want []string) {
		t.Helper()
		if got := len(dictionaries.codeToID[table]); got != len(want) {
			t.Fatalf("%s code count = %d, want %d", table, got, len(want))
		}
		for _, code := range want {
			id, err := dictionaries.id(table, table, code)
			if err != nil {
				t.Fatalf("%s id for %q: %v", table, code, err)
			}
			got, err := dictionaries.code(table, table, id)
			if err != nil {
				t.Fatalf("%s code for %d: %v", table, id, err)
			}
			if got != code {
				t.Fatalf("%s code for %d = %q, want %q", table, id, got, code)
			}
		}
	}

	assertDictionaryCodes(sourceKindTable, domainEnumCodes(t, "Source"))
	assertDictionaryCodes(orderEventTypeTable, domainEnumCodes(t, "OrderEventType"))
	assertDictionaryCodes(orderSideTable, domainEnumCodes(t, "OrderSide"))
	assertDictionaryCodes(orderAmountKindTable, domainEnumCodes(t, "OrderAmountKind"))
	assertDictionaryCodes(orderStatusTable, domainEnumCodes(t, "OrderStatus"))
	assertDictionaryCodes(adjustmentStatusTable, domainEnumCodes(t, "AdjustmentStatus"))
	auditActions := make([]string, 0, len(domain.AllAuditActions()))
	for _, action := range domain.AllAuditActions() {
		auditActions = append(auditActions, string(action))
	}
	assertDictionaryCodes(auditActionTable, auditActions)
	assertDictionaryCodes(auditActionTable, domainEnumCodes(t, "AuditAction"))
	assertDictionaryCodes(attestationAlgTable, []string{"ed25519", "none"})
	assertDictionaryCodes(
		attestationRequestTypeTable,
		domainEnumCodes(t, "AttestationRequestType"),
	)
	assertDictionaryCodes(attestationModeTable, []string{"immediate", "hold"})
}

func domainEnumCodes(t *testing.T, typeName string) []string {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller for domain enum source")
	}
	domainDir := filepath.Clean(
		filepath.Join(filepath.Dir(testFile), "../../../framework/domain"),
	)
	entries, err := os.ReadDir(domainDir)
	if err != nil {
		t.Fatalf("read domain source directory: %v", err)
	}

	codes := make([]string, 0)
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(
			fset, filepath.Join(domainDir, entry.Name()), nil, 0,
		)
		if err != nil {
			t.Fatalf("parse domain source %q: %v", entry.Name(), err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			inheritedType := ""
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Values) != len(value.Names) {
					continue
				}
				if value.Type != nil {
					ident, ok := value.Type.(*ast.Ident)
					if !ok {
						inheritedType = ""
						continue
					}
					inheritedType = ident.Name
				}
				if inheritedType != typeName {
					continue
				}
				for _, expression := range value.Values {
					literal, ok := expression.(*ast.BasicLit)
					if !ok {
						t.Fatalf(
							"%s enum has non-string constant in %q", typeName, entry.Name(),
						)
					}
					code, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatalf(
							"unquote %s enum constant in %q: %v",
							typeName,
							entry.Name(),
							err,
						)
					}
					codes = append(codes, code)
				}
			}
		}
	}
	if len(codes) == 0 {
		t.Fatalf("no %s constants found in %q", typeName, domainDir)
	}
	return codes
}

func TestNewLoadsEnumDictionaries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	s, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := New(path)
	if err != nil {
		t.Fatalf("New(reopen): %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	dictionaries, err := reopened.(*sqliteStore).enumDictionaries()
	if err != nil {
		t.Fatalf("enumDictionaries after reopen: %v", err)
	}
	if _, err := dictionaries.id(sourceKindTable, "source", string(domain.SourceSystem)); err != nil {
		t.Fatalf("source_kind system: %v", err)
	}
}

func TestMigrateRejectsEnumDictionaryCodeWithWrongStableID(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	sq := s.(*sqliteStore)
	if _, err := sq.currentDB().ExecContext(
		ctx, `UPDATE source_kind SET id = ? WHERE code = ?`, 99, domain.SourceSystem,
	); err != nil {
		t.Fatalf("change source_kind id: %v", err)
	}

	err := sq.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "source_kind") {
		t.Fatalf("Migrate after source_kind id drift = %v, want source_kind error", err)
	}
	var id int64
	if err := sq.currentDB().QueryRowContext(
		ctx, `SELECT id FROM source_kind WHERE code = ?`, domain.SourceSystem,
	).Scan(&id); err != nil {
		t.Fatalf("read source_kind after failed Migrate: %v", err)
	}
	if id != 99 {
		t.Fatalf("source_kind id after failed Migrate = %d, want 99", id)
	}
}

// TestRealmDataOpAfterCloseReturnsClosedError proves the realm data path fails
// gracefully on a shutdown/Close race: once Close swaps the pool out, a data
// method resolves the pool through the guarded accessor and returns the same
// "store: sqlite is closed" error the lifecycle methods return, rather than
// dereferencing a nil pool and panicking. A read, a write, and a
// transaction-opening method are all exercised because they reach the pool
// through different call shapes.
func TestRealmDataOpAfterCloseReturnsClosedError(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	s, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	rs, err := s.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatalf("ForRealm(default): %v", err)
	}

	// Close the store while the realm handle is still held, mirroring a data op
	// racing a shutdown.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	const want = "store: sqlite is closed"

	// Read path (QueryContext via a helper-returning method).
	if _, err := rs.ListAssets(ctx); err == nil ||
		!strings.Contains(err.Error(), want) {
		t.Fatalf("ListAssets after close = %v, want %q", err, want)
	}

	// Write path that also passes the pool to a helper (resolveAccountID).
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-1", Asset: "USD", Available: "1",
	}); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("UpsertBalance after close = %v, want %q", err, want)
	}

	// Transaction-opening path (BeginTx).
	if _, err := rs.RecordAccountAdjustment(
		ctx, fwstore.AccountAdjustmentPersistence{},
	); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("RecordAccountAdjustment after close = %v, want %q", err, want)
	}
}

func TestForRealmSingleRealmEnforcement(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)

	// The empty realm id defaults to the served realm.
	if _, err := s.ForRealm(ctx, ""); err != nil {
		t.Fatalf("ForRealm(empty) = %v, want nil", err)
	}

	// A foreign realm id is rejected with ErrInvalid.
	_, err := s.ForRealm(ctx, domain.RealmID("other"))
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ForRealm(other) error = %v, want ErrInvalid", err)
	}
}

func TestForRealmNonDefaultBoundRealm(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "officer.db")
	s, err := New(path, WithRealm(domain.RealmID("desk-a")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := s.ForRealm(ctx, domain.RealmID("desk-a")); err != nil {
		t.Fatalf("ForRealm(desk-a) = %v, want nil", err)
	}
	// The default realm is now foreign to a connector bound to desk-a.
	if _, err := s.ForRealm(ctx, domain.DefaultRealm); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("ForRealm(default) error = %v, want ErrInvalid", err)
	}
}

func TestResetDoesNotExposeNilOrUnmigratedDBToReaders(t *testing.T) {
	ctx := context.Background()
	s, rs := newTestStore(t)

	done := make(chan struct{})
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					select {
					case errs <- fmt.Errorf("reader panic: %v", recovered):
					default:
					}
				}
			}()
			for {
				select {
				case <-done:
					return
				default:
				}
				if _, err := rs.ListMarketDataInstances(ctx); err != nil {
					select {
					case errs <- fmt.Errorf("list market-data instances: %w", err):
					default:
					}
					return
				}
				if _, err := rs.ListMcpAccess(ctx); err != nil {
					select {
					case errs <- fmt.Errorf("list mcp access: %w", err):
					default:
					}
					return
				}
			}
		}()
	}

	for i := 0; i < 50; i++ {
		if err := s.Reset(ctx); err != nil {
			close(done)
			wg.Wait()
			t.Fatalf("Reset(%d): %v", i, err)
		}
		select {
		case err := <-errs:
			close(done)
			wg.Wait()
			t.Fatal(err)
		default:
		}
	}
	close(done)
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
}

func TestExternalIDHelperRoundTrip(t *testing.T) {
	id, err := newExternalID()
	if err != nil {
		t.Fatalf("newExternalID: %v", err)
	}
	if id.IsZero() {
		t.Fatal("newExternalID returned the zero value")
	}
	s := id.String()
	if len(s) != domain.ExternalIDStringLen {
		t.Fatalf("external id string len = %d, want %d", len(s), domain.ExternalIDStringLen)
	}
	parsed, err := domain.ParseExternalID(s)
	if err != nil {
		t.Fatalf("ParseExternalID: %v", err)
	}
	if parsed != id {
		t.Fatalf("round-trip mismatch: %v != %v", parsed, id)
	}

	// Two draws differ with overwhelming probability.
	other, err := newExternalID()
	if err != nil {
		t.Fatalf("newExternalID (second): %v", err)
	}
	if other == id {
		t.Fatal("two external ids collided")
	}
}

func TestAssetRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	if err := rs.CreateAssetClass(ctx, domain.AssetClass{Code: "equity", Title: "Equity"}); err != nil {
		t.Fatalf("CreateAssetClass: %v", err)
	}
	asset := domain.Asset{Code: "AAPL", Title: "Apple", AssetClass: "equity"}
	if err := rs.CreateAsset(ctx, asset); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	got, ok, err := rs.GetAsset(ctx, "AAPL")
	if err != nil || !ok {
		t.Fatalf("GetAsset: ok=%v err=%v", ok, err)
	}
	if got != asset {
		t.Fatalf("GetAsset = %+v, want %+v", got, asset)
	}

	// An unknown class code is rejected: the link is a real foreign key.
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "MSFT", AssetClass: "ghost"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateAsset(unknown class) error = %v, want ErrInvalid", err)
	}

	// Duplicate code is ErrAlreadyExists.
	if err := rs.CreateAsset(ctx, asset); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateAsset(dup) error = %v, want ErrAlreadyExists", err)
	}

	// Update mutates title and class in place (no code change).
	asset.Title = "Apple Inc."
	asset.AssetClass = ""
	if _, err := rs.UpdateAsset(ctx, "AAPL", asset); err != nil {
		t.Fatalf("UpdateAsset: %v", err)
	}
	got, _, err = rs.GetAsset(ctx, "AAPL")
	if err != nil {
		t.Fatalf("GetAsset after update: %v", err)
	}
	if got.Title != "Apple Inc." || got.AssetClass != "" {
		t.Fatalf("UpdateAsset result = %+v", got)
	}

	// Update renames the public code; the old code no longer resolves.
	renamed := domain.Asset{Code: "AAPL.US", Title: "Apple Inc."}
	updated, err := rs.UpdateAsset(ctx, "AAPL", renamed)
	if err != nil {
		t.Fatalf("UpdateAsset rename: %v", err)
	}
	if updated.Code != "AAPL.US" {
		t.Fatalf("UpdateAsset rename result = %+v", updated)
	}
	if _, ok, _ := rs.GetAsset(ctx, "AAPL"); ok {
		t.Fatal("old asset code still resolves after rename")
	}
	if got, ok, _ := rs.GetAsset(ctx, "AAPL.US"); !ok || got.Title != "Apple Inc." {
		t.Fatalf("renamed asset = %+v ok=%v", got, ok)
	}

	// Rename onto an existing code is a conflict.
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset USD: %v", err)
	}
	if _, err := rs.UpdateAsset(ctx, "AAPL.US", domain.Asset{Code: "USD"}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("UpdateAsset(rename dup) error = %v, want ErrAlreadyExists", err)
	}
	// Rename of a missing asset is ErrNotFound.
	if _, err := rs.UpdateAsset(ctx, "GHOST", domain.Asset{Code: "GHOST2"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("UpdateAsset(missing) error = %v, want ErrNotFound", err)
	}

	// List returns the rows.
	assets, err := rs.ListAssets(ctx)
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	if len(assets) != 2 {
		t.Fatalf("ListAssets len = %d, want 2", len(assets))
	}

	// Delete removes it.
	if err := rs.DeleteAsset(ctx, "AAPL.US", true); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	if _, ok, _ := rs.GetAsset(ctx, "AAPL.US"); ok {
		t.Fatal("GetAsset after delete returned ok=true")
	}
	// Delete of a missing asset is ErrNotFound.
	if err := rs.DeleteAsset(ctx, "AAPL.US", true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeleteAsset(missing) error = %v, want ErrNotFound", err)
	}
}

func TestAssetClassRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	class := domain.AssetClass{Code: "equity", Title: "Equity", Notes: "listed shares"}
	if err := rs.CreateAssetClass(ctx, class); err != nil {
		t.Fatalf("CreateAssetClass: %v", err)
	}
	got, ok, err := rs.GetAssetClass(ctx, "equity")
	if err != nil || !ok {
		t.Fatalf("GetAssetClass: ok=%v err=%v", ok, err)
	}
	if got != class {
		t.Fatalf("GetAssetClass = %+v, want %+v", got, class)
	}

	// Duplicate code rejected.
	if err := rs.CreateAssetClass(ctx, class); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateAssetClass(dup) error = %v, want ErrAlreadyExists", err)
	}

	// In-place update mutates title and notes.
	class.Title = "Equities"
	class.Notes = "cash equities"
	updated, err := rs.UpdateAssetClass(ctx, "equity", class)
	if err != nil {
		t.Fatalf("UpdateAssetClass: %v", err)
	}
	if updated.Title != "Equities" || updated.Notes != "cash equities" {
		t.Fatalf("UpdateAssetClass result = %+v", updated)
	}

	list, err := rs.ListAssetClasses(ctx)
	if err != nil {
		t.Fatalf("ListAssetClasses: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListAssetClasses len = %d, want 1", len(list))
	}

	// Missing-code update is ErrNotFound.
	if _, err := rs.UpdateAssetClass(ctx, "ghost", domain.AssetClass{Code: "ghost"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("UpdateAssetClass(missing) error = %v, want ErrNotFound", err)
	}

	if err := rs.DeleteAssetClass(ctx, "equity", false); err != nil {
		t.Fatalf("DeleteAssetClass: %v", err)
	}
	if _, ok, _ := rs.GetAssetClass(ctx, "equity"); ok {
		t.Fatal("GetAssetClass after delete returned ok=true")
	}
	if err := rs.DeleteAssetClass(ctx, "equity", false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeleteAssetClass(missing) error = %v, want ErrNotFound", err)
	}
}

func TestAssetClassLinkIsForeignKey(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	if err := rs.CreateAssetClass(ctx, domain.AssetClass{Code: "equity"}); err != nil {
		t.Fatalf("CreateAssetClass: %v", err)
	}
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "AAPL", AssetClass: "equity"}); err != nil {
		t.Fatalf("CreateAsset AAPL: %v", err)
	}
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "MSFT", AssetClass: "equity"}); err != nil {
		t.Fatalf("CreateAsset MSFT: %v", err)
	}
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("CreateAsset USD: %v", err)
	}

	// A code rename needs no cascade: the class_id foreign key is unchanged, so
	// referencing assets resolve to the new code via the join.
	if _, err := rs.UpdateAssetClass(ctx, "equity", domain.AssetClass{Code: "stock"}); err != nil {
		t.Fatalf("UpdateAssetClass rename: %v", err)
	}
	for _, code := range []string{"AAPL", "MSFT"} {
		asset, _, err := rs.GetAsset(ctx, code)
		if err != nil {
			t.Fatalf("GetAsset %s: %v", code, err)
		}
		if asset.AssetClass != "stock" {
			t.Fatalf("asset %s class = %q, want stock", code, asset.AssetClass)
		}
	}

	// The list row aggregates the asset count for the renamed class.
	page, err := rs.ListAssetClassRows(ctx, AssetClassListFilter{})
	if err != nil {
		t.Fatalf("ListAssetClassRows: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].Class.Code != "stock" || page.Rows[0].AssetCount != 2 {
		t.Fatalf("ListAssetClassRows = %+v", page.Rows)
	}

	// Non-forced delete with referencing assets reports dependents.
	if err := rs.DeleteAssetClass(ctx, "stock", false); !errors.Is(err, domain.ErrHasDependents) {
		t.Fatalf("DeleteAssetClass(non-force) error = %v, want ErrHasDependents", err)
	}

	// Forced delete removes the class; ON DELETE SET NULL clears the link.
	if err := rs.DeleteAssetClass(ctx, "stock", true); err != nil {
		t.Fatalf("DeleteAssetClass(force): %v", err)
	}
	asset, _, err := rs.GetAsset(ctx, "AAPL")
	if err != nil {
		t.Fatalf("GetAsset AAPL after force delete: %v", err)
	}
	if asset.AssetClass != "" {
		t.Fatalf("asset class after force delete = %q, want empty", asset.AssetClass)
	}
}

func TestAssetClassListFiltersAndCounts(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	if err := rs.CreateAssetClass(ctx, domain.AssetClass{Code: "equity", Title: "Equity", Notes: "shares"}); err != nil {
		t.Fatalf("CreateAssetClass equity: %v", err)
	}
	if err := rs.CreateAssetClass(ctx, domain.AssetClass{Code: "fx", Title: "Foreign Exchange", Notes: "currency pairs"}); err != nil {
		t.Fatalf("CreateAssetClass fx: %v", err)
	}
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "AAPL", AssetClass: "equity"}); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	// Code/title search matches either column.
	page, err := rs.ListAssetClassRows(ctx, AssetClassListFilter{
		Code: ExactTextMatcher("fx"),
	})
	if err != nil {
		t.Fatalf("ListAssetClassRows code: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].Class.Code != "fx" {
		t.Fatalf("ListAssetClassRows code = %+v", page.Rows)
	}

	// Notes search narrows.
	page, err = rs.ListAssetClassRows(ctx, AssetClassListFilter{
		Notes: TextMatcher{Fragments: []string{"shares"}},
	})
	if err != nil {
		t.Fatalf("ListAssetClassRows notes: %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0].Class.Code != "equity" || page.Rows[0].AssetCount != 1 {
		t.Fatalf("ListAssetClassRows notes = %+v", page.Rows)
	}

	// Unfiltered lists both, ordered by code, with per-class asset counts.
	page, err = rs.ListAssetClassRows(ctx, AssetClassListFilter{})
	if err != nil {
		t.Fatalf("ListAssetClassRows all: %v", err)
	}
	if page.Total != 2 || len(page.Rows) != 2 {
		t.Fatalf("ListAssetClassRows all total=%d rows=%d", page.Total, len(page.Rows))
	}
	if page.Rows[0].Class.Code != "equity" || page.Rows[0].AssetCount != 1 {
		t.Fatalf("ListAssetClassRows all[0] = %+v", page.Rows[0])
	}
	if page.Rows[1].Class.Code != "fx" || page.Rows[1].AssetCount != 0 {
		t.Fatalf("ListAssetClassRows all[1] = %+v", page.Rows[1])
	}
}

func TestAssetListFilters(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	if err := rs.CreateAssetClass(ctx, domain.AssetClass{Code: "equity", Title: "Equity"}); err != nil {
		t.Fatalf("CreateAssetClass equity: %v", err)
	}
	if err := rs.CreateAssetClass(ctx, domain.AssetClass{Code: "crypto", Title: "Crypto"}); err != nil {
		t.Fatalf("CreateAssetClass crypto: %v", err)
	}
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "AAPL", Title: "Apple Inc.", AssetClass: "equity"}); err != nil {
		t.Fatalf("CreateAsset AAPL: %v", err)
	}
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "BTC", Title: "Bitcoin", AssetClass: "crypto"}); err != nil {
		t.Fatalf("CreateAsset BTC: %v", err)
	}

	page, err := rs.ListAssetRows(ctx, AssetListFilter{
		Code: TextMatcher{Fragments: []string{"Apple"}},
	})
	if err != nil {
		t.Fatalf("ListAssetRows code/title: %v", err)
	}
	if page.Total != 1 || len(page.Rows) != 1 || page.Rows[0].Code != "AAPL" {
		t.Fatalf("code/title filtered page = %+v", page)
	}

	page, err = rs.ListAssetRows(ctx, AssetListFilter{
		Class: ExactTextMatcher("crypto"),
	})
	if err != nil {
		t.Fatalf("ListAssetRows class: %v", err)
	}
	if page.Total != 1 || len(page.Rows) != 1 || page.Rows[0].Code != "BTC" {
		t.Fatalf("class filtered page = %+v", page)
	}

	page, err = rs.ListAssetRows(ctx, AssetListFilter{
		Sort: SortSpec{Column: "code"},
		Page: PageSpec{Limit: 1, Offset: 1},
	})
	if err != nil {
		t.Fatalf("ListAssetRows page: %v", err)
	}
	if page.Total != 2 || len(page.Rows) != 1 || page.Rows[0].Code != "BTC" {
		t.Fatalf("paged rows = %+v", page)
	}
}

func TestPrincipalRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	p := domain.Principal{Code: "operator", Title: "Desk Operator"}
	if err := rs.CreatePrincipal(ctx, p); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	got, ok, err := rs.GetPrincipal(ctx, "operator")
	if err != nil || !ok {
		t.Fatalf("GetPrincipal: ok=%v err=%v", ok, err)
	}
	if got != p {
		t.Fatalf("GetPrincipal = %+v, want %+v", got, p)
	}
	if err := rs.CreatePrincipal(ctx, p); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreatePrincipal(dup) error = %v, want ErrAlreadyExists", err)
	}
	p.Title = "Senior Operator"
	if err := rs.UpdatePrincipal(ctx, p); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}
	list, err := rs.ListPrincipals(ctx)
	if err != nil {
		t.Fatalf("ListPrincipals: %v", err)
	}
	if len(list) != 1 || list[0].Title != "Senior Operator" {
		t.Fatalf("ListPrincipals = %+v", list)
	}
	if err := rs.DeletePrincipal(ctx, "operator"); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}
	if err := rs.DeletePrincipal(ctx, "operator"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeletePrincipal(missing) error = %v, want ErrNotFound", err)
	}
}

func TestGroupRoundTripAndEngineID(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	g1, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "alpha", Title: "Alpha"})
	if err != nil {
		t.Fatalf("CreateGroup(alpha): %v", err)
	}
	if err := domain.ValidateEngineGroupID(g1.EngineGroupID); err != nil {
		t.Fatalf("assigned engine group id out of range: %v", err)
	}

	g2, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "beta"})
	if err != nil {
		t.Fatalf("CreateGroup(beta): %v", err)
	}
	if g1.EngineGroupID == g2.EngineGroupID {
		t.Fatalf("engine group ids collided: %d", g1.EngineGroupID)
	}

	// Duplicate code rejected.
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "alpha"}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateGroup(dup) error = %v, want ErrAlreadyExists", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: ""}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateGroup(empty) error = %v, want ErrInvalid", err)
	}

	got, ok, err := rs.GetGroup(ctx, "alpha")
	if err != nil || !ok {
		t.Fatalf("GetGroup: ok=%v err=%v", ok, err)
	}
	if got.EngineGroupID != g1.EngineGroupID {
		t.Fatalf("GetGroup engine id = %d, want %d", got.EngineGroupID, g1.EngineGroupID)
	}

	if err := rs.SetGroupNotes(ctx, "alpha", "vip desk"); err != nil {
		t.Fatalf("SetGroupNotes: %v", err)
	}
	if err := rs.SetGroupBlocked(ctx, "alpha", true, "risk review"); err != nil {
		t.Fatalf("SetGroupBlocked: %v", err)
	}
	got, _, _ = rs.GetGroup(ctx, "alpha")
	if got.Notes != "vip desk" || !got.Blocked || got.BlockReason != "risk review" {
		t.Fatalf("group after updates = %+v", got)
	}

	groups, err := rs.ListGroups(ctx)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("ListGroups len = %d, want 2", len(groups))
	}

	if err := rs.SetGroupNotes(ctx, "ghost", "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetGroupNotes(missing) error = %v, want ErrNotFound", err)
	}
	if _, err := rs.UpdateGroup(ctx, "alpha", domain.AccountGroup{Code: ""}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("UpdateGroup(empty) error = %v, want ErrInvalid", err)
	}
}

func TestAccountRoundTripEngineIDAndGroupLink(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	for _, code := range []string{"EUR", "USD"} {
		if err := rs.CreateAsset(ctx, domain.Asset{Code: code}); err != nil {
			t.Fatalf("CreateAsset(%s): %v", code, err)
		}
	}
	if err := rs.SetGroupCurrency(ctx, "", "USD"); err != nil {
		t.Fatalf("SetGroupCurrency(default): %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{
		Code: "alpha", Currency: "EUR",
	}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	a1, err := rs.CreateAccount(ctx, domain.Account{
		Code: "acc-1", Title: "Account 1", GroupCode: "alpha", Notes: "n", Pnl: "12.5",
		Blocked: true, BlockReason: "risk",
		PnlHaltReason: domain.PnlHaltReasonMissingAccountCurrency,
	})
	if err != nil {
		t.Fatalf("CreateAccount(acc-1): %v", err)
	}
	if err := domain.ValidateEngineAccountID(a1.EngineAccountID); err != nil {
		t.Fatalf("assigned engine account id out of range: %v", err)
	}
	if a1.Title != "Account 1" || a1.Pnl != "" ||
		a1.PnlHaltReason != domain.PnlHaltReasonMissingAccountCurrency ||
		a1.GroupCode != "alpha" || a1.Notes != "n" ||
		!a1.Blocked || a1.BlockReason != "risk" {
		t.Fatalf("CreateAccount result lost stored fields: %+v", a1)
	}
	if a1.Currency != "" || a1.GroupCurrency != "EUR" ||
		a1.DefaultCurrency != "USD" || a1.EffectiveCurrency != "EUR" ||
		a1.CurrencyOrigin != domain.CurrencyOriginGroup {
		t.Fatalf("CreateAccount result currency cascade = %+v, want group EUR", a1)
	}

	a2, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2"})
	if err != nil {
		t.Fatalf("CreateAccount(acc-2): %v", err)
	}
	if a1.EngineAccountID == a2.EngineAccountID {
		t.Fatalf("engine account ids collided: %d", a1.EngineAccountID)
	}
	// A fresh account has no P&L value at all: the column is NULL, which reads
	// back as empty, and is never the zero a reported P&L would carry.
	if a2.Pnl != "" || a2.DefaultCurrency != "USD" ||
		a2.EffectiveCurrency != "USD" ||
		a2.CurrencyOrigin != domain.CurrencyOriginDefault {
		t.Fatalf("CreateAccount default-currency result = %+v, want default USD", a2)
	}

	// Read back surfaces the group code, not a surrogate id.
	got, ok, err := rs.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("GetAccount: ok=%v err=%v", ok, err)
	}
	if got.GroupCode != "alpha" {
		t.Fatalf("GetAccount group code = %q, want alpha", got.GroupCode)
	}
	if got.PnlHaltReason != domain.PnlHaltReasonMissingAccountCurrency {
		t.Fatalf("GetAccount pnl halt reason = %q", got.PnlHaltReason)
	}
	if got.EngineAccountID != a1.EngineAccountID {
		t.Fatalf("GetAccount engine id = %d, want %d", got.EngineAccountID, a1.EngineAccountID)
	}

	// acc-2 has no group.
	got2, _, _ := rs.GetAccount(ctx, "acc-2")
	if got2.GroupCode != "" {
		t.Fatalf("acc-2 group code = %q, want empty", got2.GroupCode)
	}

	// Duplicate account code rejected.
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1"}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("CreateAccount(dup) error = %v, want ErrAlreadyExists", err)
	}

	// Unknown group code on create is ErrInvalid.
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-3", GroupCode: "ghost"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateAccount(unknown group) error = %v, want ErrInvalid", err)
	}

	// ListGroupAccounts returns only acc-1.
	members, err := rs.ListGroupAccounts(ctx, "alpha")
	if err != nil {
		t.Fatalf("ListGroupAccounts: %v", err)
	}
	if len(members) != 1 || members[0].Code != "acc-1" {
		t.Fatalf("ListGroupAccounts = %+v", members)
	}

	// SetAccountGroup to an unknown group is ErrInvalid; valid clear works.
	if err := rs.SetAccountGroup(ctx, "acc-2", "ghost"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SetAccountGroup(unknown) error = %v, want ErrInvalid", err)
	}
	if err := rs.SetAccountGroup(ctx, "acc-1", ""); err != nil {
		t.Fatalf("SetAccountGroup(clear): %v", err)
	}
	got, _, _ = rs.GetAccount(ctx, "acc-1")
	if got.GroupCode != "" {
		t.Fatalf("after clear, group code = %q, want empty", got.GroupCode)
	}

	// Block and notes round-trip.
	if err := rs.SetAccountBlocked(ctx, "acc-2", true, "kill"); err != nil {
		t.Fatalf("SetAccountBlocked: %v", err)
	}
	if err := rs.SetAccountNotes(ctx, "acc-2", "watch"); err != nil {
		t.Fatalf("SetAccountNotes: %v", err)
	}
	got2, _, _ = rs.GetAccount(ctx, "acc-2")
	if !got2.Blocked || got2.BlockReason != "kill" || got2.Notes != "watch" {
		t.Fatalf("acc-2 after updates = %+v", got2)
	}

	all, err := rs.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAccounts len = %d, want 2", len(all))
	}

	// Missing account on a setter is ErrNotFound.
	if err := rs.SetAccountNotes(ctx, "ghost", "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetAccountNotes(missing) error = %v, want ErrNotFound", err)
	}
}

func TestDeleteGroupClearsAccountLink(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "alpha"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-1", GroupCode: "alpha"}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// Deleting the group must clear the member's link (ON DELETE SET NULL), not
	// cascade-delete the account.
	if err := rs.DeleteGroup(ctx, "alpha"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	got, ok, err := rs.GetAccount(ctx, "acc-1")
	if err != nil || !ok {
		t.Fatalf("account should survive group delete: ok=%v err=%v", ok, err)
	}
	if got.GroupCode != "" {
		t.Fatalf("account group code after group delete = %q, want empty", got.GroupCode)
	}

	if err := rs.DeleteGroup(ctx, "alpha"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DeleteGroup(missing) error = %v, want ErrNotFound", err)
	}
}

func TestListAccountRowsFiltersAndCounts(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "desk-alpha"}); err != nil {
		t.Fatalf("CreateGroup alpha: %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "desk-beta"}); err != nil {
		t.Fatalf("CreateGroup beta: %v", err)
	}
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "AAPL"}); err != nil {
		t.Fatalf("CreateAsset AAPL: %v", err)
	}
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "MSFT"}); err != nil {
		t.Fatalf("CreateAsset MSFT: %v", err)
	}
	for _, account := range []domain.Account{
		{
			Code:      "acc_%_alpha",
			GroupCode: "desk-alpha",
			Notes:     "primary equity desk",
		},
		{
			Code:        "acc-star-beta",
			GroupCode:   "desk-beta",
			Blocked:     true,
			BlockReason: "risk halt",
		},
		{Code: "cash-default", Notes: "cash desk"},
	} {
		if _, err := rs.CreateAccount(ctx, account); err != nil {
			t.Fatalf("CreateAccount %s: %v", account.Code, err)
		}
	}
	now := time.Now().UTC()
	for _, balance := range []domain.Balance{
		{Account: "acc_%_alpha", Asset: "AAPL", UpdatedAt: now},
		{Account: "acc_%_alpha", Asset: "MSFT", UpdatedAt: now},
		{Account: "acc-star-beta", Asset: "AAPL", UpdatedAt: now},
	} {
		if err := rs.UpsertBalance(ctx, balance); err != nil {
			t.Fatalf("UpsertBalance %+v: %v", balance, err)
		}
	}

	rows, err := rs.ListAccountRows(ctx, AccountListFilter{
		Code: TextMatcher{Fragments: []string{"acc_%"}},
	})
	if err != nil {
		t.Fatalf("ListAccountRows code literal: %v", err)
	}
	if got := accountRowCodes(rows.Rows); len(got) != 1 || got[0] != "acc_%_alpha" {
		t.Fatalf("code literal rows = %v, want [acc_%%_alpha]", got)
	}
	if rows.Rows[0].PositionCount != 2 {
		t.Fatalf("position count = %d, want 2", rows.Rows[0].PositionCount)
	}

	group := "desk-beta"
	zero := 0
	rows, err = rs.ListAccountRows(ctx, AccountListFilter{
		GroupCode:   &group,
		Status:      StatusFilterBlocked,
		BlockReason: TextMatcher{Fragments: []string{"risk"}},
		Position: CountRangeFilter{
			Min:          &zero,
			MinExclusive: true,
		},
	})
	if err != nil {
		t.Fatalf("ListAccountRows combined: %v", err)
	}
	if got := accountRowCodes(rows.Rows); len(got) != 1 || got[0] != "acc-star-beta" {
		t.Fatalf("combined rows = %v, want [acc-star-beta]", got)
	}

	ungrouped := ""
	rows, err = rs.ListAccountRows(ctx, AccountListFilter{
		GroupCode: &ungrouped,
		Position: CountRangeFilter{
			Min: &zero,
			Max: &zero,
		},
	})
	if err != nil {
		t.Fatalf("ListAccountRows ungrouped no positions: %v", err)
	}
	if got := accountRowCodes(rows.Rows); len(got) != 1 || got[0] != "cash-default" {
		t.Fatalf("ungrouped rows = %v, want [cash-default]", got)
	}

	two := 2
	rows, err = rs.ListAccountRows(ctx, AccountListFilter{
		Position: CountRangeFilter{
			Min: &two,
			Max: &two,
		},
	})
	if err != nil {
		t.Fatalf("ListAccountRows position range: %v", err)
	}
	if got := accountRowCodes(rows.Rows); len(got) != 1 || got[0] != "acc_%_alpha" {
		t.Fatalf("position range rows = %v, want [acc_%%_alpha]", got)
	}
}

// The engine rejects every order from a member of a blocked group, so the
// account status axis must resolve the group tier too: a member of a blocked
// group is blocked, not active.
func TestListAccountRowsStatusFilterIsEffective(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "desk"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	for _, account := range []domain.Account{
		{Code: "group-member", GroupCode: "desk"},
		{Code: "own-block", Blocked: true, BlockReason: "risk halt"},
		{Code: "clear"},
	} {
		if _, err := rs.CreateAccount(ctx, account); err != nil {
			t.Fatalf("CreateAccount %s: %v", account.Code, err)
		}
	}
	if err := rs.SetGroupBlocked(ctx, "desk", true, "desk halt"); err != nil {
		t.Fatalf("SetGroupBlocked: %v", err)
	}

	blocked, err := rs.ListAccountRows(ctx, AccountListFilter{
		Status: StatusFilterBlocked,
	})
	if err != nil {
		t.Fatalf("ListAccountRows blocked: %v", err)
	}
	got := accountRowCodes(blocked.Rows)
	slices.Sort(got)
	if !slices.Equal(got, []string{"group-member", "own-block"}) {
		t.Fatalf("blocked rows = %v, want [group-member own-block]", got)
	}
	if blocked.Total != 2 {
		t.Fatalf("blocked total = %d, want 2", blocked.Total)
	}

	active, err := rs.ListAccountRows(ctx, AccountListFilter{
		Status: StatusFilterActive,
	})
	if err != nil {
		t.Fatalf("ListAccountRows active: %v", err)
	}
	if got := accountRowCodes(active.Rows); !slices.Equal(got, []string{"clear"}) {
		t.Fatalf("active rows = %v, want [clear]", got)
	}
	if active.Total != 1 {
		t.Fatalf("active total = %d, want 1", active.Total)
	}
}

// TestListAccountRowsBlockReasonFilterIsEffective holds the reason axis to the
// same effective join as the status axis. The account DTO publishes the group's
// reason for a group-blocked member, so a filter on that exact text must return
// the row that displays it.
func TestListAccountRowsBlockReasonFilterIsEffective(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "desk"}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	for _, account := range []domain.Account{
		{Code: "group-member", GroupCode: "desk"},
		{Code: "own-block", Blocked: true, BlockReason: "risk halt"},
		{Code: "clear"},
	} {
		if _, err := rs.CreateAccount(ctx, account); err != nil {
			t.Fatalf("CreateAccount %s: %v", account.Code, err)
		}
	}
	if err := rs.SetGroupBlocked(ctx, "desk", true, "desk halt"); err != nil {
		t.Fatalf("SetGroupBlocked: %v", err)
	}

	page, err := rs.ListAccountRows(ctx, AccountListFilter{
		BlockReason: ExactTextMatcher("desk halt"),
	})
	if err != nil {
		t.Fatalf("ListAccountRows group reason: %v", err)
	}
	if got := accountRowCodes(page.Rows); !slices.Equal(got, []string{"group-member"}) {
		t.Fatalf("group reason rows = %v, want [group-member]", got)
	}
	if page.Total != 1 {
		t.Fatalf("group reason total = %d, want 1", page.Total)
	}

	// The account's own block still wins attribution, as in the domain.
	page, err = rs.ListAccountRows(ctx, AccountListFilter{
		BlockReason: ExactTextMatcher("risk halt"),
	})
	if err != nil {
		t.Fatalf("ListAccountRows own reason: %v", err)
	}
	if got := accountRowCodes(page.Rows); !slices.Equal(got, []string{"own-block"}) {
		t.Fatalf("own reason rows = %v, want [own-block]", got)
	}
}

func TestListRowsMatchTitleAndGroupCode(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{
		Code:  "g-eq",
		Title: "Equity Desk",
	}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{
		Code:      "acc-1",
		Title:     "Alpha Trader",
		GroupCode: "g-eq",
	}); err != nil {
		t.Fatalf("CreateAccount acc-1: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "acc-2"}); err != nil {
		t.Fatalf("CreateAccount acc-2: %v", err)
	}

	// Account code search also matches the title.
	rows, err := rs.ListAccountRows(ctx, AccountListFilter{
		Code: TextMatcher{Fragments: []string{"Trader"}},
	})
	if err != nil {
		t.Fatalf("ListAccountRows title: %v", err)
	}
	if got := accountRowCodes(rows.Rows); len(got) != 1 || got[0] != "acc-1" {
		t.Fatalf("title match rows = %v, want [acc-1]", got)
	}

	// Exact group code narrows accounts by their assigned group.
	groupCode := "g-eq"
	rows, err = rs.ListAccountRows(ctx, AccountListFilter{
		GroupCode: &groupCode,
	})
	if err != nil {
		t.Fatalf("ListAccountRows group code: %v", err)
	}
	if got := accountRowCodes(rows.Rows); len(got) != 1 || got[0] != "acc-1" {
		t.Fatalf("group code rows = %v, want [acc-1]", got)
	}

	// Group list code search also matches the group title.
	groupPage, err := rs.ListGroupRows(ctx, GroupListFilter{
		Code: TextMatcher{Fragments: []string{"Equity"}},
	})
	if err != nil {
		t.Fatalf("ListGroupRows title: %v", err)
	}
	if got := groupRowCodes(groupPage.Rows); len(got) != 1 || got[0] != "g-eq" {
		t.Fatalf("group title rows = %v, want [g-eq]", got)
	}

	// Group account-count range selects groups by their member count: g-eq has
	// one account, so a two-account floor excludes it.
	two := 2
	groupPage, err = rs.ListGroupRows(ctx, GroupListFilter{
		Account: CountRangeFilter{Min: &two},
	})
	if err != nil {
		t.Fatalf("ListGroupRows account range: %v", err)
	}
	if got := groupRowCodes(groupPage.Rows); len(got) != 0 {
		t.Fatalf("account range rows = %v, want []", got)
	}
	one := 1
	groupPage, err = rs.ListGroupRows(ctx, GroupListFilter{
		Code:    TextMatcher{Fragments: []string{"g-eq"}},
		Account: CountRangeFilter{Min: &one},
	})
	if err != nil {
		t.Fatalf("ListGroupRows account range floor: %v", err)
	}
	if got := groupRowCodes(groupPage.Rows); len(got) != 1 || got[0] != "g-eq" {
		t.Fatalf("account range floor rows = %v, want [g-eq]", got)
	}
}

func TestListGroupRowsFiltersAndCounts(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)
	zero := 0

	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{
		Code:  "desk-alpha",
		Notes: "primary group",
	}); err != nil {
		t.Fatalf("CreateGroup alpha: %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{
		Code:        "desk-beta",
		Blocked:     true,
		BlockReason: "risk halt",
	}); err != nil {
		t.Fatalf("CreateGroup beta: %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{Code: "empty"}); err != nil {
		t.Fatalf("CreateGroup empty: %v", err)
	}
	if err := rs.CreateAsset(ctx, domain.Asset{Code: "AAPL"}); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	for _, account := range []domain.Account{
		{Code: "acc-alpha-1", GroupCode: "desk-alpha"},
		{Code: "acc-alpha-2", GroupCode: "desk-alpha"},
		{Code: "acc-beta-1", GroupCode: "desk-beta"},
		{Code: "acc-default"},
	} {
		if _, err := rs.CreateAccount(ctx, account); err != nil {
			t.Fatalf("CreateAccount %s: %v", account.Code, err)
		}
	}
	now := time.Now().UTC()
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-alpha-1", Asset: "AAPL", UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertBalance alpha: %v", err)
	}
	if err := rs.UpsertBalance(ctx, domain.Balance{
		Account: "acc-default", Asset: "AAPL", UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertBalance default: %v", err)
	}

	page, err := rs.ListGroupRows(ctx, GroupListFilter{})
	if err != nil {
		t.Fatalf("ListGroupRows all: %v", err)
	}
	defaultRow, ok := findGroupRow(page.Rows, "")
	if !ok {
		t.Fatalf("default group row missing from %v", groupRowCodes(page.Rows))
	}
	if defaultRow.AccountCount != 1 || defaultRow.PositionCount != 1 {
		t.Fatalf(
			"default counts = accounts %d positions %d, want 1/1",
			defaultRow.AccountCount, defaultRow.PositionCount,
		)
	}
	// Total counts the three real groups; the synthetic default is excluded.
	if page.Total != 3 {
		t.Fatalf("total = %d, want 3", page.Total)
	}
	// The default group is pinned first on the first page.
	if page.Rows[0].Group.Code != "" {
		t.Fatalf("default group not pinned first: %v", groupRowCodes(page.Rows))
	}

	page, err = rs.ListGroupRows(ctx, GroupListFilter{
		Code:    TextMatcher{Fragments: []string{"desk", "alpha"}},
		Account: CountRangeFilter{Min: &zero, MinExclusive: true},
		Position: CountRangeFilter{
			Min:          &zero,
			MinExclusive: true,
		},
	})
	if err != nil {
		t.Fatalf("ListGroupRows alpha: %v", err)
	}
	if got := groupRowCodes(page.Rows); len(got) != 1 || got[0] != "desk-alpha" {
		t.Fatalf("alpha rows = %v, want [desk-alpha]", got)
	}
	if page.Rows[0].AccountCount != 2 || page.Rows[0].PositionCount != 1 {
		t.Fatalf(
			"alpha counts = accounts %d positions %d, want 2/1",
			page.Rows[0].AccountCount, page.Rows[0].PositionCount,
		)
	}
	if page.Total != 1 {
		t.Fatalf("alpha total = %d, want 1", page.Total)
	}

	page, err = rs.ListGroupRows(ctx, GroupListFilter{
		Status:      StatusFilterBlocked,
		BlockReason: TextMatcher{Fragments: []string{"risk"}},
		Account:     CountRangeFilter{Min: &zero, MinExclusive: true},
	})
	if err != nil {
		t.Fatalf("ListGroupRows blocked notes: %v", err)
	}
	if got := groupRowCodes(page.Rows); len(got) != 1 || got[0] != "desk-beta" {
		t.Fatalf("blocked rows = %v, want [desk-beta]", got)
	}

	page, err = rs.ListGroupRows(ctx, GroupListFilter{
		Account: CountRangeFilter{Min: &zero, Max: &zero},
	})
	if err != nil {
		t.Fatalf("ListGroupRows no accounts: %v", err)
	}
	if got := groupRowCodes(page.Rows); len(got) != 1 || got[0] != "empty" {
		t.Fatalf("empty rows = %v, want [empty]", got)
	}

	page, err = rs.ListGroupRows(ctx, GroupListFilter{
		Code: TextMatcher{Fragments: []string{"desk"}},
	})
	if err != nil {
		t.Fatalf("ListGroupRows code filter: %v", err)
	}
	if got := groupRowCodes(page.Rows); slices.Contains(got, "") {
		t.Fatalf("default row bypassed code filter: %v", got)
	}

	page, err = rs.ListGroupRows(ctx, GroupListFilter{Status: StatusFilterBlocked})
	if err != nil {
		t.Fatalf("ListGroupRows blocked filter: %v", err)
	}
	if got := groupRowCodes(page.Rows); slices.Contains(got, "") {
		t.Fatalf("default row bypassed blocked filter: %v", got)
	}

	page, err = rs.ListGroupRows(ctx, GroupListFilter{
		BlockReason: TextMatcher{Fragments: []string{"risk"}},
	})
	if err != nil {
		t.Fatalf("ListGroupRows notes filter: %v", err)
	}
	if got := groupRowCodes(page.Rows); slices.Contains(got, "") {
		t.Fatalf("default row bypassed notes filter: %v", got)
	}
}

func TestListGroupRowsSortAndPage(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	// Three real groups with distinct account counts so an accountCount sort has
	// a deterministic order independent of code.
	for _, group := range []domain.AccountGroup{
		{Code: "desk-bravo"},
		{Code: "desk-alpha"},
		{Code: "desk-charlie"},
	} {
		if _, err := rs.CreateGroup(ctx, group); err != nil {
			t.Fatalf("CreateGroup %s: %v", group.Code, err)
		}
	}
	for _, account := range []domain.Account{
		{Code: "acc-a1", GroupCode: "desk-alpha"},
		{Code: "acc-a2", GroupCode: "desk-alpha"},
		{Code: "acc-b1", GroupCode: "desk-bravo"},
		{Code: "acc-ungrouped"},
	} {
		if _, err := rs.CreateAccount(ctx, account); err != nil {
			t.Fatalf("CreateAccount %s: %v", account.Code, err)
		}
	}

	// Descending code sort: the default group still pins first; the real groups
	// follow in descending code order.
	page, err := rs.ListGroupRows(ctx, GroupListFilter{
		Sort: SortSpec{Column: "code", Descending: true},
	})
	if err != nil {
		t.Fatalf("ListGroupRows sort desc: %v", err)
	}
	if got := groupRowCodes(page.Rows); !slices.Equal(
		got, []string{"", "desk-charlie", "desk-bravo", "desk-alpha"},
	) {
		t.Fatalf("code desc order = %v", got)
	}
	if page.Total != 3 {
		t.Fatalf("total = %d, want 3", page.Total)
	}

	// accountCount ascending: charlie(0) < bravo(1) < alpha(2); default pinned.
	page, err = rs.ListGroupRows(ctx, GroupListFilter{
		Sort: SortSpec{Column: "accountCount"},
	})
	if err != nil {
		t.Fatalf("ListGroupRows sort accountCount: %v", err)
	}
	if got := groupRowCodes(page.Rows); !slices.Equal(
		got, []string{"", "desk-charlie", "desk-bravo", "desk-alpha"},
	) {
		t.Fatalf("accountCount asc order = %v", got)
	}

	// First page: default group plus the first real group; Total still 3.
	page, err = rs.ListGroupRows(ctx, GroupListFilter{
		Page: PageSpec{Limit: 1, Offset: 0},
	})
	if err != nil {
		t.Fatalf("ListGroupRows page 0: %v", err)
	}
	if got := groupRowCodes(page.Rows); !slices.Equal(got, []string{"", "desk-alpha"}) {
		t.Fatalf("page 0 = %v, want [\"\" desk-alpha]", got)
	}
	if page.Total != 3 {
		t.Fatalf("page 0 total = %d, want 3", page.Total)
	}

	// Second page (offset 1): default group excluded, real groups continue from
	// the offset window.
	page, err = rs.ListGroupRows(ctx, GroupListFilter{
		Page: PageSpec{Limit: 1, Offset: 1},
	})
	if err != nil {
		t.Fatalf("ListGroupRows page 1: %v", err)
	}
	if got := groupRowCodes(page.Rows); !slices.Equal(got, []string{"desk-bravo"}) {
		t.Fatalf("page 1 = %v, want [desk-bravo]", got)
	}
	if page.Total != 3 {
		t.Fatalf("page 1 total = %d, want 3", page.Total)
	}
}

func accountRowCodes(rows []AccountListRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Account.Code.String())
	}
	return out
}

func groupRowCodes(rows []GroupListRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Group.Code)
	}
	return out
}

func findGroupRow(rows []GroupListRow, code string) (GroupListRow, bool) {
	for _, row := range rows {
		if row.Group.Code == code {
			return row, true
		}
	}
	return GroupListRow{}, false
}
