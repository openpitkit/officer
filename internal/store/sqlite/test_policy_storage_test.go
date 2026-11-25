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

package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func policyFixtureClassifier(provider string) (bool, error) {
	switch provider {
	case "test-fixture":
		return true, nil
	case domain.TradingProviderAlpaca, domain.TradingProviderIB,
		domain.TradingProviderBinance, domain.TradingProviderEtoro, "test-provider", "another-provider":
		return false, nil
	default:
		return false, fmt.Errorf("fixture provider %q: %w", provider, domain.ErrTradingProviderUnavailable)
	}
}

func newPolicyRealm(t *testing.T) (store.Store, *realmStore) {
	t.Helper()
	st := newClassifierStore(t)
	if err := st.BindExecutionPolicyProviders(policyFixtureClassifier); err != nil {
		t.Fatal(err)
	}
	realm, err := st.ForRealm(context.Background(), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	return st, realm.(*realmStore)
}

func createPolicyConnection(t *testing.T, realm store.RealmStore, provider, label string) domain.ExternalID {
	t.Helper()
	connection, err := realm.CreateTradingConnection(context.Background(), domain.TradingConnection{
		Provider: provider, Label: label, VenueIdentity: label,
		Mode: domain.TradingModePaper, Credentials: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	return connection.ExternalID
}

func policyFixture(t *testing.T) domain.TestExecutionPolicy {
	t.Helper()
	mask, err := domain.ParseInstrumentMask("*", "USD")
	if err != nil {
		t.Fatal(err)
	}
	fills, seconds := 2, int64(7)
	return domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{
		{Mask: mask, Mode: "split", PriceVariant: "best", Fills: &fills},
		{Mask: mask, Mode: "rest", PriceVariant: "worst", Wait: &domain.TestExecutionWait{Kind: "finite", Seconds: &seconds}},
	}}
}

func requirePolicyState(t *testing.T, realm store.RealmStore, id domain.ExternalID, want domain.TestExecutionPolicyState) {
	t.Helper()
	got, err := realm.GetTestExecutionPolicy(context.Background(), id)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("policy = %+v, err = %v; want %+v", got, err, want)
	}
}

func TestTestPolicyCreateInitialState(t *testing.T) {
	t.Parallel()
	_, realm := newPolicyRealm(t)
	ctx := context.Background()
	testID := createPolicyConnection(t, realm, "test-fixture", "test")
	requirePolicyState(t, realm, testID, domain.TestExecutionPolicyState{
		Revision: 1, Policy: domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{}},
	})
	venueID := createPolicyConnection(t, realm, domain.TradingProviderAlpaca, "venue")
	var count int
	if err := realm.rawDB().QueryRow(`SELECT COUNT(*) FROM trading_test_policy p JOIN trading_connection c ON c.id = p.connection_id WHERE c.external_id = ?`, venueID.Bytes()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("venue policy rows = %d, want 0", count)
	}
	if _, err := realm.GetTestExecutionPolicy(ctx, venueID); !errors.Is(err, domain.ErrExecutionPolicyUnsupported) {
		t.Fatalf("venue read: %v", err)
	}
	if _, _, err := realm.ReplaceTestExecutionPolicy(ctx, venueID, policyFixture(t)); !errors.Is(err, domain.ErrExecutionPolicyUnsupported) {
		t.Fatalf("venue replace: %v", err)
	}
}

func TestTestPolicyCreateIsAtomic(t *testing.T) {
	t.Parallel()
	_, realm := newPolicyRealm(t)
	if _, err := realm.rawDB().Exec(`CREATE TRIGGER fail_test_policy BEFORE INSERT ON trading_test_policy BEGIN SELECT RAISE(ABORT, 'policy insert refused'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := realm.CreateTradingConnection(context.Background(), domain.TradingConnection{
		Provider: "test-fixture", Label: "atomic", VenueIdentity: "atomic",
		Mode: domain.TradingModePaper, Credentials: "{}",
	})
	if err == nil {
		t.Fatal("policy INSERT failure was accepted")
	}
	var count int
	if err := realm.rawDB().QueryRow(`SELECT COUNT(*) FROM trading_connection`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed policy insert left %d connection(s)", count)
	}
}

func TestTestPolicyCreateRequiresClassifier(t *testing.T) {
	t.Parallel()
	for _, bound := range []bool{false, true} {
		t.Run(fmt.Sprint(bound), func(t *testing.T) {
			t.Parallel()
			st := newClassifierStore(t)
			if bound {
				if err := st.BindExecutionPolicyProviders(policyFixtureClassifier); err != nil {
					t.Fatal(err)
				}
			}
			realm, err := st.ForRealm(context.Background(), domain.DefaultRealm)
			if err != nil {
				t.Fatal(err)
			}
			_, err = realm.CreateTradingConnection(context.Background(), domain.TradingConnection{
				Provider: "unregistered", Label: "unknown", VenueIdentity: "unknown",
				Mode: domain.TradingModePaper, Credentials: "{}",
			})
			if err == nil || (bound && !errors.Is(err, domain.ErrTradingProviderUnavailable)) {
				t.Fatalf("create without classification: %v", err)
			}
			var count int
			if err := realm.(*realmStore).rawDB().QueryRow(`SELECT COUNT(*) FROM trading_connection`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("connections = %d, err = %v", count, err)
			}
		})
	}
}

func TestTestPolicyReplaceRevisionsAndCopies(t *testing.T) {
	t.Parallel()
	_, realm := newPolicyRealm(t)
	id := createPolicyConnection(t, realm, "test-fixture", "replace")
	ctx := context.Background()
	input := policyFixture(t)
	// Duplicates and overlaps are valid stored sets; selection is send-time.
	input.Rules = append(input.Rules, input.Rules[0])
	old, next, err := realm.ReplaceTestExecutionPolicy(ctx, id, input)
	if err != nil {
		t.Fatal(err)
	}
	if old.Revision != 1 || old.Policy.Rules == nil || len(old.Policy.Rules) != 0 || next.Revision != 2 || !reflect.DeepEqual(next.Policy, input) {
		t.Fatalf("first replace = %+v -> %+v", old, next)
	}
	previous, identical, err := realm.ReplaceTestExecutionPolicy(ctx, id, input)
	if err != nil || !reflect.DeepEqual(previous, next) || identical.Revision != 3 || !reflect.DeepEqual(identical.Policy, input) {
		t.Fatalf("identical replace = %+v -> %+v, err = %v", previous, identical, err)
	}
	want, err := realm.GetTestExecutionPolicy(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	*input.Rules[0].Fills = 3
	*input.Rules[1].Wait.Seconds = 11
	input.Rules[1].Wait.Kind = "infinite"
	input.Rules[0].Mode = "reject_even"
	if !reflect.DeepEqual(identical, want) {
		t.Fatal("returned state aliases caller input")
	}
	*previous.Policy.Rules[0].Fills = 9
	*previous.Policy.Rules[1].Wait.Seconds = 29
	if !reflect.DeepEqual(identical, want) {
		t.Fatal("old and new state alias each other")
	}
	*identical.Policy.Rules[0].Fills = 8
	*identical.Policy.Rules[1].Wait.Seconds = 19
	identical.Policy.Rules[1].Wait.Kind = "infinite"
	requirePolicyState(t, realm, id, want)
	_, empty, err := realm.ReplaceTestExecutionPolicy(ctx, id, domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{}})
	if err != nil || empty.Revision != 4 || empty.Policy.Rules == nil || len(empty.Policy.Rules) != 0 {
		t.Fatalf("empty replace = %+v, err = %v", empty, err)
	}
	var stored string
	if err := realm.rawDB().QueryRow(`SELECT rules FROM trading_test_policy`).Scan(&stored); err != nil || stored != "[]" {
		t.Fatalf("stored empty rules = %q, err = %v", stored, err)
	}
}

func TestTestPolicyReplaceOverflowAndValidation(t *testing.T) {
	t.Parallel()
	_, realm := newPolicyRealm(t)
	id := createPolicyConnection(t, realm, "test-fixture", "overflow")
	ctx := context.Background()
	if _, _, err := realm.ReplaceTestExecutionPolicy(ctx, id, domain.TestExecutionPolicy{}); err == nil {
		t.Fatal("nil rules accepted")
	}
	invalid := policyFixture(t)
	*invalid.Rules[0].Fills = 1
	if _, _, err := realm.ReplaceTestExecutionPolicy(ctx, id, invalid); err == nil {
		t.Fatal("invalid rule accepted")
	}
	if _, err := realm.rawDB().Exec(`UPDATE trading_test_policy SET revision = ?`, int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := realm.ReplaceTestExecutionPolicy(ctx, id, policyFixture(t)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("revision overflow = %v, want ErrConflict", err)
	}
	requirePolicyState(t, realm, id, domain.TestExecutionPolicyState{
		Revision: math.MaxInt64, Policy: domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{}},
	})
	if _, _, err := realm.ReplaceTestExecutionPolicy(ctx, "absent", policyFixture(t)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown connection = %v", err)
	}
	if _, err := realm.rawDB().Exec(`UPDATE trading_connection SET provider = 'unregistered'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := realm.ReplaceTestExecutionPolicy(ctx, id, policyFixture(t)); !errors.Is(err, domain.ErrTradingProviderUnavailable) {
		t.Fatalf("unknown provider = %v", err)
	}
}

func TestTestPolicyStrictStoredRead(t *testing.T) {
	t.Parallel()
	for _, rules := range []string{
		`[{"mask":{"baseAsset":"*","quoteAsset":"USD"},"mode":"execute","priceVariant":"normal","unknown":true}]`, `[] []`, `[] invalid`, `null`,
		`[{"mask":{"baseAsset":"*","quoteAsset":"USD"},"mode":"split","priceVariant":"normal","fills":1}]`,
	} {
		t.Run(rules, func(t *testing.T) {
			t.Parallel()
			_, realm := newPolicyRealm(t)
			id := createPolicyConnection(t, realm, "test-fixture", "strict")
			if _, err := realm.rawDB().Exec(`UPDATE trading_test_policy SET rules = ?`, rules); err != nil {
				t.Fatal(err)
			}
			if _, err := realm.GetTestExecutionPolicy(context.Background(), id); err == nil {
				t.Fatal("Get accepted corrupt stored rules")
			}
			if _, _, err := realm.ReplaceTestExecutionPolicy(context.Background(), id, policyFixture(t)); err == nil {
				t.Fatal("Replace accepted corrupt old rules")
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		_, realm := newPolicyRealm(t)
		id := createPolicyConnection(t, realm, "test-fixture", "missing")
		if _, err := realm.rawDB().Exec(`DELETE FROM trading_test_policy`); err != nil {
			t.Fatal(err)
		}
		if _, err := realm.GetTestExecutionPolicy(context.Background(), id); err == nil {
			t.Fatal("missing policy became a default")
		}
		if _, _, err := realm.ReplaceTestExecutionPolicy(context.Background(), id, policyFixture(t)); err == nil {
			t.Fatal("Replace created a missing policy")
		}
	})
}

func TestTestPolicyReplaceTransaction(t *testing.T) {
	t.Parallel()
	_, realm := newPolicyRealm(t)
	id := createPolicyConnection(t, realm, "test-fixture", "transaction")
	if _, err := realm.rawDB().Exec(`CREATE TRIGGER fail_policy_rules BEFORE UPDATE OF rules ON trading_test_policy BEGIN SELECT RAISE(ABORT, 'rules refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := realm.ReplaceTestExecutionPolicy(context.Background(), id, policyFixture(t)); err == nil {
		t.Fatal("UPDATE trigger failure was accepted")
	}
	requirePolicyState(t, realm, id, domain.TestExecutionPolicyState{
		Revision: 1, Policy: domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{}},
	})
	if _, err := realm.rawDB().Exec(`DROP TRIGGER fail_policy_rules;
		CREATE TRIGGER rewrite_policy AFTER UPDATE OF rules ON trading_test_policy BEGIN UPDATE trading_test_policy SET rules = '[]' WHERE connection_id = NEW.connection_id; END`); err != nil {
		t.Fatal(err)
	}
	old, next, err := realm.ReplaceTestExecutionPolicy(context.Background(), id, policyFixture(t))
	if err != nil || old.Revision != 1 || next.Revision != 2 || len(next.Policy.Rules) != 0 || next.Policy.Rules == nil {
		t.Fatalf("states must reflect transaction reads: %+v -> %+v, err = %v", old, next, err)
	}
}

func TestTestPolicyConcurrentReplace(t *testing.T) {
	t.Parallel()
	_, realm := newPolicyRealm(t)
	id := createPolicyConnection(t, realm, "test-fixture", "concurrent")
	ctx := t.Context()
	type result struct {
		old, next domain.TestExecutionPolicyState
		err       error
	}
	want := domain.TestExecutionPolicyState{Revision: 1, Policy: domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{}}}
	for range 30 {
		start := make(chan struct{})
		results := make(chan result, 2)
		for _, policy := range []domain.TestExecutionPolicy{policyFixture(t), {Rules: []domain.TestExecutionRule{}}} {
			go func() {
				<-start
				old, next, err := realm.ReplaceTestExecutionPolicy(ctx, id, policy)
				results <- result{old, next, err}
			}()
		}
		close(start)
		a, b := <-results, <-results
		if a.err != nil || b.err != nil {
			t.Fatalf("concurrent replaces failed: %v / %v", a.err, b.err)
		}
		if a.next.Revision > b.next.Revision {
			a, b = b, a
		}
		if !reflect.DeepEqual(a.old, want) || a.next.Revision != want.Revision+1 ||
			b.next.Revision != want.Revision+2 || !reflect.DeepEqual(b.old, a.next) {
			t.Fatalf("concurrent pairs = %+v -> %+v / %+v -> %+v", a.old, a.next, b.old, b.next)
		}
		requirePolicyState(t, realm, id, b.next)
		want = b.next
	}
}

func TestTestPolicyIndependentHandleWriteConflict(t *testing.T) {
	t.Parallel()
	st, realm := newPolicyRealm(t)
	id := createPolicyConnection(t, realm, "test-fixture", "locked")
	second, err := New(st.Path(), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.BindExecutionPolicyProviders(policyFixtureClassifier); err != nil {
		t.Fatal(err)
	}
	other, err := second.ForRealm(context.Background(), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	db := other.(*realmStore).rawDB()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("UPDATE trading_test_policy SET revision = revision WHERE connection_id = (SELECT id FROM trading_connection WHERE external_id = ?)", id.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := realm.ReplaceTestExecutionPolicy(context.Background(), id, policyFixture(t)); err == nil {
		t.Fatal("replace succeeded while an independent handle held the writer lock")
	} else {
		t.Logf("independent writer: %v", err)
	}
	requirePolicyState(t, realm, id, domain.TestExecutionPolicyState{
		Revision: 1, Policy: domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{}},
	})
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	old, next, err := realm.ReplaceTestExecutionPolicy(context.Background(), id, policyFixture(t))
	if err != nil || old.Revision != 1 || next.Revision != 2 {
		t.Fatalf("replace after rollback = %+v -> %+v, %v", old, next, err)
	}
	requirePolicyState(t, realm, id, next)
}

func TestTestPolicyDeleteCascades(t *testing.T) {
	t.Parallel()
	_, realm := newPolicyRealm(t)
	id := createPolicyConnection(t, realm, "test-fixture", "delete")
	var foreignKeys int
	if err := realm.rawDB().QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, err = %v", foreignKeys, err)
	}
	if _, err := realm.rawDB().Exec(`DELETE FROM trading_connection WHERE external_id = ?`, id.Bytes()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := realm.rawDB().QueryRow(`SELECT COUNT(*) FROM trading_test_policy`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("policy rows after connection delete = %d, err = %v", count, err)
	}
}

func exportPolicyArchive(t *testing.T, realm store.RealmStore) backup.Archive {
	t.Helper()
	archive, err := realm.ExportBackup(context.Background(), backup.Scope{All: true})
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func TestTestPolicyBackupRoundTripAndModes(t *testing.T) {
	t.Parallel()
	_, source := newPolicyRealm(t)
	id := createPolicyConnection(t, source, "test-fixture", "source")
	venue := createPolicyConnection(t, source, domain.TradingProviderAlpaca, "venue")
	ctx := context.Background()
	if _, _, err := source.ReplaceTestExecutionPolicy(ctx, id, policyFixture(t)); err != nil {
		t.Fatal(err)
	}
	archive := exportPolicyArchive(t, source)
	if len(archive.Data.TestExecutionPolicies) != 1 || archive.Data.TestExecutionPolicies[0].Connection != id || archive.Data.TestExecutionPolicies[0].Revision != 2 {
		t.Fatalf("exported policies = %+v", archive.Data.TestExecutionPolicies)
	}
	t.Run("canonical rules", func(t *testing.T) {
		t.Parallel()
		_, target := newPolicyRealm(t)
		formatted := cloneBackupArchive(t, archive)
		var indented bytes.Buffer
		if err := json.Indent(&indented, formatted.Data.TestExecutionPolicies[0].Rules, "", "  "); err != nil {
			t.Fatal(err)
		}
		formatted.Data.TestExecutionPolicies[0].Rules = indented.Bytes()
		if _, err := target.RestoreBackup(ctx, formatted, backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll}); err != nil {
			t.Fatal(err)
		}
		if got := exportPolicyArchive(t, target).Data.TestExecutionPolicies; !reflect.DeepEqual(got, archive.Data.TestExecutionPolicies) {
			t.Fatalf("canonical policies = %+v, want %+v", got, archive.Data.TestExecutionPolicies)
		}
	})
	for _, mode := range []backup.RestoreMode{backup.RestoreModeReplaceAll, backup.RestoreModeOverwrite, backup.RestoreModeInsertMissing} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			_, target := newPolicyRealm(t)
			opts := backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll}
			if _, err := target.RestoreBackup(ctx, archive, opts); err != nil {
				t.Fatal(err)
			}
			if got := exportPolicyArchive(t, target).Data.TestExecutionPolicies; !reflect.DeepEqual(got, archive.Data.TestExecutionPolicies) {
				t.Fatalf("round trip policies = %+v, want %+v", got, archive.Data.TestExecutionPolicies)
			}
			_, before, err := target.ReplaceTestExecutionPolicy(ctx, id, domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{}})
			if err != nil {
				t.Fatal(err)
			}
			other := createPolicyConnection(t, target, "test-fixture", "target-only")
			opts.Mode = mode
			if _, err := target.RestoreBackup(ctx, archive, opts); err != nil {
				t.Fatal(err)
			}
			want := domain.TestExecutionPolicyState{Revision: 2, Policy: policyFixture(t)}
			if mode == backup.RestoreModeInsertMissing {
				want = before
			}
			requirePolicyState(t, target, id, want)
			_, err = target.GetTestExecutionPolicy(ctx, other)
			if mode == backup.RestoreModeReplaceAll {
				if !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("replace-all kept target-only connection: %v", err)
				}
				var rows int
				if err := target.rawDB().QueryRow(`SELECT COUNT(*) FROM trading_test_policy`).Scan(&rows); err != nil || rows != 1 {
					t.Fatalf("replace-all policy rows = %d, err = %v", rows, err)
				}
			} else if err != nil {
				t.Fatalf("merge lost target-only policy: %v", err)
			}
			if _, err := target.GetTestExecutionPolicy(ctx, venue); !errors.Is(err, domain.ErrExecutionPolicyUnsupported) {
				t.Fatalf("venue after restore = %v", err)
			}
		})
	}

	t.Run("venue-only archive", func(t *testing.T) {
		t.Parallel()
		_, venueSource := newPolicyRealm(t)
		createPolicyConnection(t, venueSource, domain.TradingProviderIB, "venue-only")
		venueArchive := exportPolicyArchive(t, venueSource)
		_, target := newPolicyRealm(t)
		if _, err := target.RestoreBackup(ctx, venueArchive, backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll}); err != nil {
			t.Fatal(err)
		}
		if got := exportPolicyArchive(t, target).Data; !reflect.DeepEqual(got, venueArchive.Data) {
			t.Fatal("venue-only archive did not restore exactly")
		}
	})
}

func TestTestPolicyBackupRestoreRejectsInvalidAndRollsBack(t *testing.T) {
	t.Parallel()
	_, source := newPolicyRealm(t)
	id := createPolicyConnection(t, source, "test-fixture", "source")
	venue := createPolicyConnection(t, source, domain.TradingProviderAlpaca, "venue")
	archive := exportPolicyArchive(t, source)
	for _, mode := range []backup.RestoreMode{backup.RestoreModeReplaceAll, backup.RestoreModeOverwrite, backup.RestoreModeInsertMissing} {
		for _, defect := range []string{"missing", "venue", "unknown-provider", "unknown-field", "case-alias", "optional-null", "trailing", "null", "invalid-rule", "zero-revision", "revision-overflow", "duplicate"} {
			t.Run(string(mode)+"/"+defect, func(t *testing.T) {
				t.Parallel()
				_, target := newPolicyRealm(t)
				targetID := createPolicyConnection(t, target, "test-fixture", "keep")
				if _, _, err := target.ReplaceTestExecutionPolicy(context.Background(), targetID, policyFixture(t)); err != nil {
					t.Fatal(err)
				}
				before := exportPolicyArchive(t, target).Data
				broken := cloneBackupArchive(t, archive)
				switch defect {
				case "missing":
					broken.Data.TestExecutionPolicies = nil
				case "venue":
					broken.Data.TestExecutionPolicies = append(broken.Data.TestExecutionPolicies, backup.TestExecutionPolicy{Connection: venue, Revision: 1, Rules: json.RawMessage(`[]`)})
				case "unknown-provider":
					broken.Data.TradingConnections[len(broken.Data.TradingConnections)-1].Provider = "unregistered"
				case "unknown-field":
					broken.Data.TestExecutionPolicies[0].Rules = json.RawMessage(`[{"mask":{"baseAsset":"*","quoteAsset":"USD"},"mode":"execute","priceVariant":"normal","unknown":true}]`)
				case "case-alias":
					broken.Data.TestExecutionPolicies[0].Rules = json.RawMessage(`[{"mask":{"BaseAsset":"*","quoteAsset":"USD"},"mode":"execute","priceVariant":"normal"}]`)
				case "optional-null":
					broken.Data.TestExecutionPolicies[0].Rules = json.RawMessage(`[{"mask":{"baseAsset":"*","quoteAsset":"USD"},"mode":"execute","priceVariant":"normal","fills":null}]`)
				case "trailing":
					broken.Data.TestExecutionPolicies[0].Rules = json.RawMessage(`[] []`)
				case "null":
					broken.Data.TestExecutionPolicies[0].Rules = json.RawMessage(`null`)
				case "invalid-rule":
					broken.Data.TestExecutionPolicies[0].Rules = json.RawMessage(`[{"mask":{"baseAsset":"*","quoteAsset":"USD"},"mode":"split","priceVariant":"normal","fills":1}]`)
				case "zero-revision":
					broken.Data.TestExecutionPolicies[0].Revision = 0
				case "revision-overflow":
					broken.Data.TestExecutionPolicies[0].Revision = uint64(math.MaxInt64) + 1
				case "duplicate":
					broken.Data.TestExecutionPolicies = append(broken.Data.TestExecutionPolicies, broken.Data.TestExecutionPolicies[0])
				}
				if _, err := target.RestoreBackup(context.Background(), broken, backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: mode}); err == nil {
					t.Fatalf("restore accepted %s policy for %s", defect, id)
				}
				if after := exportPolicyArchive(t, target).Data; !reflect.DeepEqual(after, before) {
					t.Fatalf("failed restore changed realm: before %+v, after %+v", before, after)
				}
			})
		}
	}
}

func TestTestPolicyRestoreChecksExistingAffectedConnections(t *testing.T) {
	t.Parallel()
	for _, defect := range []string{"missing", "corrupt", "venue", "unknown-provider"} {
		t.Run(defect, func(t *testing.T) {
			t.Parallel()
			_, target := newPolicyRealm(t)
			id := createPolicyConnection(t, target, "test-fixture", "affected")
			archive := exportPolicyArchive(t, target)
			switch defect {
			case "missing":
				if _, err := target.rawDB().Exec(`DELETE FROM trading_test_policy`); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if _, err := target.rawDB().Exec(`UPDATE trading_test_policy SET rules = 'null'`); err != nil {
					t.Fatal(err)
				}
			case "venue":
				if _, err := target.rawDB().Exec(`UPDATE trading_connection SET provider = ?`, domain.TradingProviderAlpaca); err != nil {
					t.Fatal(err)
				}
			case "unknown-provider":
				if _, err := target.rawDB().Exec(`UPDATE trading_connection SET provider = 'unregistered'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := target.RestoreBackup(context.Background(), archive, backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: backup.RestoreModeInsertMissing}); err == nil {
				// A missing policy is inserted from this valid archive. Every other
				// invalid existing row must refuse even though its policy was skipped.
				if defect != "missing" {
					t.Fatalf("restore skipped validation of %s for %s", defect, id)
				}
			} else if defect == "missing" {
				t.Fatal(err)
			}
			if defect == "missing" {
				requirePolicyState(t, target, id, domain.TestExecutionPolicyState{Revision: 1, Policy: domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{}}})
			}
		})
	}
}

func TestTestPolicySnapshotAndExportValidation(t *testing.T) {
	t.Parallel()
	_, realm := newPolicyRealm(t)
	id := createPolicyConnection(t, realm, "test-fixture", "snapshot")
	_, want, err := realm.ReplaceTestExecutionPolicy(context.Background(), id, policyFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, cleanup, err := realm.backupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})
	if _, _, err := realm.ReplaceTestExecutionPolicy(context.Background(), id, domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{}}); err != nil {
		t.Fatal(err)
	}
	requirePolicyState(t, snapshot, id, want)
	for _, rules := range []string{`null`, `[] []`, `[{"mask":{"baseAsset":"*","quoteAsset":"USD"},"mode":"execute","priceVariant":"normal","unknown":true}]`} {
		if _, err := realm.rawDB().Exec(`UPDATE trading_test_policy SET rules = ?`, rules); err != nil {
			t.Fatal(err)
		}
		if _, err := realm.ExportBackup(context.Background(), backup.Scope{All: true}); err == nil {
			t.Fatalf("export accepted %s", rules)
		}
	}
}

func TestTestPolicyExportDoesNotClassify(t *testing.T) {
	t.Parallel()
	_, realm := newPolicyRealm(t)
	id := createPolicyConnection(t, realm, "test-fixture", "stored")
	_, want, err := realm.ReplaceTestExecutionPolicy(context.Background(), id, policyFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	createPolicyConnection(t, realm, domain.TradingProviderAlpaca, "unavailable")
	if _, err := realm.rawDB().Exec("UPDATE trading_connection SET provider = 'unregistered'"); err != nil {
		t.Fatal(err)
	}
	archive := exportPolicyArchive(t, realm)
	if len(archive.Data.TestExecutionPolicies) != 1 {
		t.Fatalf("exported policies = %+v", archive.Data.TestExecutionPolicies)
	}
	row := archive.Data.TestExecutionPolicies[0]
	got, err := decodeStoredTestExecutionPolicy(row.Revision, string(row.Rules))
	if err != nil || row.Connection != id || !reflect.DeepEqual(got, want) {
		t.Fatalf("exported row = %+v, decoded = %+v, error = %v", row, got, err)
	}
	snapshot, cleanup, err := realm.backupSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})
	if got := exportPolicyArchive(t, snapshot).Data.TestExecutionPolicies; !reflect.DeepEqual(got, archive.Data.TestExecutionPolicies) {
		t.Fatalf("snapshot policies = %+v, want %+v", got, archive.Data.TestExecutionPolicies)
	}
}
