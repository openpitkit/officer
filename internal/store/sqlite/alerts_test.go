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
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/alerts"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func alertCaller(name string) context.Context {
	return auth.ContextWithCaller(context.Background(), domain.Caller{Source: domain.SourcePanel, Principal: name})
}
func alertTestType(calls *atomic.Int32) alerts.Type {
	validate := func(raw json.RawMessage) error {
		if string(raw) != "{}" {
			return domain.ErrInvalid
		}
		return nil
	}
	return alerts.Type{Name: "test-decision", Validate: validate, Actions: map[string]alerts.Action{
		"accept": {Validate: validate, Resolve: func(context.Context, alerts.Event, json.RawMessage) (json.RawMessage, error) {
			calls.Add(1)
			return json.RawMessage(`{"accepted":true}`), nil
		}},
		"fail": {Validate: validate, Resolve: func(context.Context, alerts.Event, json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("decision refused")
		}},
	}}
}
func alertEvent(id string) alerts.Event {
	return alerts.Event{ID: id, Type: "test-decision", At: time.Now().UTC(), Source: domain.SourceSystem, Payload: json.RawMessage(`{}`)}
}
func alertTestService(t *testing.T, r *realmStore, calls *atomic.Int32, channels ...alerts.Channel) *alerts.Service {
	t.Helper()
	s, err := alerts.New(r, []alerts.Type{alertTestType(calls)}, channels)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAlertsPersonalStateFanoutAndResolution(t *testing.T) {
	_, rs := newTestStore(t)
	r := rs.(*realmStore)
	var calls atomic.Int32
	var attempted []string
	s := alertTestService(t, r, &calls,
		alerts.Channel{Name: "failed", Deliver: func(ctx context.Context, e alerts.Event) error {
			if _, err := r.GetAlert(ctx, e.ID, "alice"); err != nil {
				t.Fatalf("delivery before persistence: %v", err)
			}
			attempted = append(attempted, "failed")
			return errors.New("secret must not escape")
		}},
		alerts.Channel{Name: "other", Deliver: func(context.Context, alerts.Event) error { attempted = append(attempted, "other"); return nil }},
	)
	ctx := alertCaller("alice")
	if err := s.Publish(ctx, alertEvent("event-1")); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("safe delivery error = %v", err)
	}
	if strings.Join(attempted, ",") != "failed,other" {
		t.Fatalf("fanout = %v", attempted)
	}
	list, err := s.List(ctx)
	if err != nil || list.Total != 1 || list.Unread != 1 || len(list.Alerts[0].Deliveries) != 3 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if !list.Alerts[0].Deliveries[0].Failed {
		t.Fatalf("failed channel missing: %+v", list.Alerts[0].Deliveries)
	}
	if _, err := s.Detail(ctx, "event-1"); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List(ctx)
	if list.Unread != 1 {
		t.Fatal("reading list/detail marked read")
	}
	if err := s.SetRead(ctx, "event-1", true); err != nil {
		t.Fatal(err)
	}
	// A second Service is another HTTP/MCP session over the same durable store.
	session := alertTestService(t, r, &calls)
	alice, _ := session.List(alertCaller("alice"))
	bob, _ := session.List(alertCaller("bob"))
	if alice.Unread != 0 || bob.Unread != 1 || alice.Total != 1 {
		t.Fatalf("personal marks leaked: alice=%+v bob=%+v", alice, bob)
	}
	if err := session.SetRead(alertCaller("bob"), "event-1", false); err != nil {
		t.Fatal(err)
	}
	alice, _ = s.List(ctx)
	if alice.Unread != 0 {
		t.Fatal("foreign user's mutation changed Alice")
	}
	if err := session.SetRead(ctx, "event-1", false); err != nil {
		t.Fatal(err)
	}
	alice, _ = s.List(ctx)
	if alice.Unread != 1 {
		t.Fatal("mark unread not shared")
	}
	for _, bad := range []struct{ action, input string }{{"missing", "{}"}, {"accept", "null"}, {"fail", "{}"}, {"accept", "{"}} {
		if _, err := s.Resolve(ctx, "event-1", bad.action, json.RawMessage(bad.input)); err == nil {
			t.Fatalf("accepted %s %s", bad.action, bad.input)
		}
		if _, err := s.Detail(ctx, "event-1"); err != nil {
			t.Fatalf("refused action removed event: %v", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input invoked action")
	}
	var wg sync.WaitGroup
	out := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := session.Resolve(ctx, "event-1", "accept", json.RawMessage(`{}`))
			out <- err
		}()
	}
	wg.Wait()
	close(out)
	successes := 0
	for err := range out {
		if err == nil {
			successes++
		}
	}
	if successes != 1 || calls.Load() != 1 {
		t.Fatalf("duplicate resolution: successes=%d calls=%d", successes, calls.Load())
	}
	list, err = s.List(ctx)
	if err != nil || list.Total != 0 {
		t.Fatalf("resolved event remains: %+v %v", list, err)
	}
	var retained, actor string
	if err := r.rawDB().QueryRow(`SELECT actor_code,detail_params FROM audit au JOIN audit_action aa ON aa.id=au.action_id WHERE aa.code='resolve_alert'`).Scan(&actor, &retained); err != nil {
		t.Fatal(err)
	}
	var resolution map[string]string
	if err := json.Unmarshal([]byte(retained), &resolution); err != nil {
		t.Fatal(err)
	}
	if actor != "alice" || resolution["action"] != "accept" || resolution["result"] != `{"accepted":true}` || resolution["alert"] != "event-1" {
		t.Fatalf("result attribution = %+v", resolution)
	}
	for _, table := range []string{"alert_read_state", "alert_delivery"} {
		var count int
		if err := r.rawDB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s cleanup = %d %v", table, count, err)
		}
	}
	if err := s.Publish(ctx, alertEvent("event-1")); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("resolved ID republished: %v", err)
	}
}

func TestAlertsIdentityValidationAndTransactionFailures(t *testing.T) {
	_, rs := newTestStore(t)
	r := rs.(*realmStore)
	var calls atomic.Int32
	s := alertTestService(t, r, &calls)
	ctx := alertCaller("alice")
	if err := s.Publish(ctx, alertEvent("kept")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []context.Context{context.Background(), auth.ContextWithCaller(context.Background(), domain.Caller{Source: domain.SourcePanel}), auth.ContextWithCaller(context.Background(), auth.SystemCaller()), auth.ContextWithCaller(context.Background(), domain.Caller{Source: "fake", Principal: "alice"})} {
		if _, err := s.List(bad); err == nil {
			t.Fatal("list accepted missing identity")
		}
		if _, err := s.Detail(bad, "kept"); err == nil {
			t.Fatal("detail accepted missing identity")
		}
		if err := s.SetRead(bad, "kept", true); err == nil {
			t.Fatal("mark accepted missing identity")
		}
		if _, err := s.Resolve(bad, "kept", "accept", json.RawMessage(`{}`)); err == nil {
			t.Fatal("action accepted missing identity")
		}
	}
	for _, id := range []string{"", "absent"} {
		if err := s.SetRead(ctx, id, true); err == nil {
			t.Fatal("invalid/missing ID accepted")
		}
	}
	for _, edit := range []func(*alerts.Event){func(e *alerts.Event) { e.Type = "unknown" }, func(e *alerts.Event) { e.Payload = json.RawMessage(`null`) }, func(e *alerts.Event) { e.At = time.Time{} }, func(e *alerts.Event) { e.Source = "fake" }} {
		e := alertEvent("invalid")
		edit(&e)
		if err := s.Publish(ctx, e); err == nil {
			t.Fatal("invalid event stored")
		}
	}
	// Audit or delete failure must roll back result, personal cleanup and deletion.
	for _, table := range []string{"audit", "actionable_alert"} {
		operation := "INSERT"
		if table == "actionable_alert" {
			operation = "DELETE"
		}
		if _, err := r.rawDB().Exec(`CREATE TRIGGER fail_alert BEFORE ` + operation + ` ON ` + table + ` BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Resolve(ctx, "kept", "accept", json.RawMessage(`{}`)); err == nil {
			t.Fatal("injected failure succeeded")
		}
		if _, err := s.Detail(ctx, "kept"); err != nil {
			t.Fatalf("transaction lost event: %v", err)
		}
		var n int
		if err := r.rawDB().QueryRow(`SELECT COUNT(*) FROM audit au JOIN audit_action aa ON aa.id=au.action_id WHERE aa.code='resolve_alert'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("partial result committed: %d %v", n, err)
		}
		if _, err := r.rawDB().Exec(`DROP TRIGGER fail_alert`); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := s.List(ctx)
	if list.Total != 1 || list.Unread != 1 {
		t.Fatalf("negative requests changed state: %+v", list)
	}
}

// A generated operation sequence checks isolation through repeated mark/read
// transitions, including independent sessions and the terminal resolved state.
func TestAlertsReadStateSequences(t *testing.T) {
	_, rs := newTestStore(t)
	r := rs.(*realmStore)
	var calls atomic.Int32
	s := alertTestService(t, r, &calls)
	if err := s.Publish(alertCaller("alice"), alertEvent("sequence")); err != nil {
		t.Fatal(err)
	}
	expected := map[string]bool{"alice": false, "bob": false}
	random := rand.New(rand.NewPCG(349, 533))
	names := []string{"alice", "bob"}
	for step := range 80 {
		name := names[random.IntN(len(names))]
		read := random.IntN(2) == 1
		if err := s.SetRead(alertCaller(name), "sequence", read); err != nil {
			t.Fatal(err)
		}
		expected[name] = read
		session := alertTestService(t, r, &calls)
		for _, principal := range names {
			a, err := session.Detail(alertCaller(principal), "sequence")
			if err != nil {
				t.Fatal(err)
			}
			list, err := session.List(alertCaller(principal))
			if err != nil {
				t.Fatal(err)
			}
			unread := 1
			if expected[principal] {
				unread = 0
			}
			if a.Read != expected[principal] || list.Unread != unread || list.Total != 1 {
				t.Fatalf("step %d principal %s: detail=%+v list=%+v expected read=%v", step, principal, a, list, expected[principal])
			}
		}
	}
	if _, err := s.Resolve(alertCaller("bob"), "sequence", "accept", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	for _, principal := range names {
		if err := s.SetRead(alertCaller(principal), "sequence", false); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("terminal mark recreated event: %v", err)
		}
		list, err := s.List(alertCaller(principal))
		if err != nil || list.Total != 0 || list.Unread != 0 {
			t.Fatalf("terminal state = %+v %v", list, err)
		}
	}
}

func TestAlertsSurviveDatabaseReopen(t *testing.T) {
	st, rs := newTestStore(t)
	r := rs.(*realmStore)
	path := r.store.path
	var calls atomic.Int32
	s := alertTestService(t, r, &calls)
	ctx := alertCaller("alice")
	if err := s.Publish(ctx, alertEvent("restart")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRead(ctx, "restart", true); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(path, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := reopened.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	realm, err := reopened.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	s = alertTestService(t, realm.(*realmStore), &calls)
	list, err := s.List(ctx)
	if err != nil || list.Total != 1 || list.Unread != 0 || len(list.Alerts[0].Deliveries) != 1 {
		t.Fatalf("restart lost state: %+v %v", list, err)
	}
}

func TestAlertsHandlerCanUseDomainStore(t *testing.T) {
	_, rs := newTestStore(t)
	r := rs.(*realmStore)
	var calls atomic.Int32
	typ := alertTestType(&calls)
	action := typ.Actions["accept"]
	action.Resolve = func(ctx context.Context, e alerts.Event, _ json.RawMessage) (json.RawMessage, error) {
		// A real decision may use the same store through Node; its sole SQL
		// connection must remain available to the domain operation.
		if err := r.SetAlertRead(ctx, e.ID, "alice", true); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"accepted":true}`), nil
	}
	typ.Actions["accept"] = action
	s, err := alerts.New(r, []alerts.Type{typ}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(alertCaller("alice"), 2*time.Second)
	defer cancel()
	if err := s.Publish(ctx, alertEvent("reentrant")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, "reentrant", "accept", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("handler could not use store: %v", err)
	}
}

func TestAlertsPortableBackupModesAndRollback(t *testing.T) {
	_, sourceRealm := newTestStore(t)
	source := sourceRealm.(*realmStore)
	var calls atomic.Int32
	s := alertTestService(t, source, &calls, alerts.Channel{Name: "failed", Deliver: func(context.Context, alerts.Event) error { return errors.New("transport failure") }})
	ctx := alertCaller("alice")
	if err := s.Publish(ctx, alertEvent("portable")); err == nil {
		t.Fatal("delivery failure missing")
	}
	if err := s.SetRead(ctx, "portable", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRead(alertCaller("bob"), "portable", false); err != nil {
		t.Fatal(err)
	}
	archive, err := source.ExportBackup(ctx, backup.Scope{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Data.Alerts) != 1 || !archive.Data.Alerts[0].ReadStates["alice"] || !archive.Data.Alerts[0].Deliveries["failed"] {
		t.Fatalf("archive lost personal/delivery state: %+v", archive.Data.Alerts)
	}
	excluded, err := source.ExportBackup(ctx, backup.Scope{Sections: []backup.Section{backup.SectionGeneralSettings}})
	if err != nil {
		t.Fatal(err)
	}
	if len(excluded.Data.Alerts) != 0 {
		t.Fatal("activity-excluded archive carries alerts")
	}
	for _, mode := range []backup.RestoreMode{backup.RestoreModeReplaceAll, backup.RestoreModeOverwrite, backup.RestoreModeInsertMissing} {
		t.Run(string(mode), func(t *testing.T) {
			_, targetRealm := newTestStore(t)
			target := targetRealm.(*realmStore)
			session := alertTestService(t, target, &calls)
			for _, id := range []string{"portable", "target-only"} {
				if err := session.Publish(ctx, alertEvent(id)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: mode}); err != nil {
				t.Fatal(err)
			}
			a, err := session.Detail(ctx, "portable")
			if err != nil {
				t.Fatal(err)
			}
			if mode == backup.RestoreModeInsertMissing {
				if a.Read || len(a.Deliveries) != 1 {
					t.Fatalf("insert-missing overwrote personal state: %+v", a)
				}
			} else if !a.Read || len(a.Deliveries) != 2 || !a.Deliveries[0].Failed {
				t.Fatalf("restore lost state: %+v", a)
			}
			_, err = session.Detail(ctx, "target-only")
			if (mode == backup.RestoreModeReplaceAll) != errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("replace-all scope: %v", err)
			}
			before, err := session.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			malformed := archive
			malformed.Data = backup.FilterData(archive.Data, backup.Scope{All: true})
			malformed.Data.Alerts[0].ReadStates[""] = true
			if _, err := target.RestoreBackup(ctx, malformed, backup.RestoreOptions{Scope: backup.Scope{All: true}, Mode: backup.RestoreModeReplaceAll}); err == nil {
				t.Fatal("malformed personal state restored")
			}
			after, err := session.List(ctx)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("failed restore changed alert state: before=%+v after=%+v err=%v", before, after, err)
			}
		})
	}
}

func TestAlertsLateDeliveryAfterResolution(t *testing.T) {
	_, rs := newTestStore(t)
	r := rs.(*realmStore)
	var calls atomic.Int32
	started := make(chan struct{})
	finish := make(chan struct{})
	s := alertTestService(t, r, &calls, alerts.Channel{Name: "slow", Deliver: func(ctx context.Context, _ alerts.Event) error {
		close(started)
		select {
		case <-finish:
			return errors.New("late transport failure")
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	ctx, cancel := context.WithTimeout(alertCaller("alice"), 2*time.Second)
	defer cancel()
	published := make(chan error, 1)
	go func() { published <- s.Publish(ctx, alertEvent("late")) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("slow channel did not start")
	}
	if _, err := s.Resolve(ctx, "late", "accept", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	close(finish)
	err := <-published
	if err == nil || strings.Contains(err.Error(), "not found") || !strings.Contains(err.Error(), "delivery failed for channel slow") {
		t.Fatalf("late delivery outcome = %v", err)
	}
	list, err := s.List(ctx)
	if err != nil || list.Total != 0 {
		t.Fatalf("late delivery resurrected event: %+v %v", list, err)
	}
	var failures int
	if err := r.rawDB().QueryRow(`SELECT COUNT(*) FROM audit au JOIN audit_action aa ON aa.id=au.action_id WHERE aa.code='alert_delivery' AND au.detail_code='alert_delivery_failed' AND json_extract(au.detail_params,'$.alert')='late' AND json_extract(au.detail_params,'$.channel')='slow'`).Scan(&failures); err != nil || failures != 1 {
		t.Fatalf("late failure not audited once: %d %v", failures, err)
	}
	for _, table := range []string{"actionable_alert", "alert_read_state", "alert_delivery"} {
		var n int
		if err := r.rawDB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("late receipt recreated %s: %d %v", table, n, err)
		}
	}
	audit := store.AuditEntry{Actor: "alice", Source: domain.SourcePanel, Action: domain.AuditActionAlertDelivery, Detail: domain.NewAuditDetail(domain.AuditDetailAlertDeliveryFailed, domain.AuditDetailParams{"alert": "unknown", "channel": "slow"})}
	if err := r.RecordAlertDelivery(ctx, "unknown", alerts.Delivery{Channel: "slow", Failed: true}, audit); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown event receipt accepted: %v", err)
	}
	var unknown int
	if err := r.rawDB().QueryRow(`SELECT COUNT(*) FROM audit WHERE json_extract(detail_params,'$.alert')='unknown'`).Scan(&unknown); err != nil || unknown != 0 {
		t.Fatalf("unknown receipt created audit: %d %v", unknown, err)
	}
}
