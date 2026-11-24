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

// Package alerts owns durable actionable events and their registered decisions.
package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// Event is immutable until a registered action resolves it. Producers assign
// stable IDs so repeated publication cannot duplicate a business event.
type Event struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	At      time.Time       `json:"at"`
	Source  domain.Source   `json:"source"`
	Payload json.RawMessage `json:"payload"`
}

// Delivery exposes a durable outcome without exposing internal channel errors.
type Delivery struct {
	Channel string `json:"channel"`
	Failed  bool   `json:"failed"`
}

// Alert adds personal state and available decisions to an open event.
type Alert struct {
	Event
	Read       bool       `json:"read"`
	Deliveries []Delivery `json:"deliveries"`
	Actions    []string   `json:"actions"`
}

// List is a consistent view of unresolved events for one caller.
type List struct {
	Alerts []Alert `json:"alerts"`
	Unread int     `json:"unread"`
	Total  int     `json:"total"`
}

// Resolution remains after the open event and personal marks are removed.
type Resolution struct {
	Event  Event           `json:"event"`
	At     time.Time       `json:"at"`
	Actor  string          `json:"actor"`
	Source domain.Source   `json:"source"`
	Action string          `json:"action"`
	Result json.RawMessage `json:"result"`
}

// Action validates input and performs a decision. A handler returns valid JSON
// only on success. External side effects need domain-owned idempotency; the
// storage transaction does not provide external exactly-once execution.
type Action struct {
	Validate func(json.RawMessage) error
	Resolve  func(context.Context, Event, json.RawMessage) (json.RawMessage, error)
}

// Type validates producer payloads and declares every supported decision.
type Type struct {
	Name     string
	Validate func(json.RawMessage) error
	Actions  map[string]Action
	// Visible opts this type into caller-scoped reads and decisions. Nil retains
	// realm-wide behavior. Publication and configured delivery remain trusted.
	Visible func(context.Context, Event) (bool, error)
}

// Channel delivers an already stored event. Registered channels are enabled.
type Channel struct {
	Name    string
	Deliver func(context.Context, Event) error
}

// Store is the independent persistence capability. ResolveAlert serialises
// decisions and commits audit, result and deletion together after decide.
// RecordAlertDelivery writes an open event's receipt and audit atomically; after
// a proven successful resolution it writes only the late outcome audit. It must
// reject an unknown ID and must never recreate a resolved event.
type Store interface {
	CreateAlert(context.Context, Event) error
	PublishAlert(context.Context, string) error
	ListAlerts(context.Context, string) ([]Alert, error)
	GetAlert(context.Context, string, string) (Alert, error)
	SetAlertRead(context.Context, string, string, bool) error
	RecordAlertDelivery(context.Context, string, Delivery, store.AuditEntry) error
	ResolveAlert(context.Context, string, func(Event) (Resolution, store.AuditEntry, error)) (Resolution, error)
}

// Service shares decisions and personal state across authenticated surfaces.
type Service struct {
	store    Store
	types    map[string]Type
	channels []Channel
}

// New fixes the type/channel registry and installs durable panel publication.
// Zero concrete business types is valid and produces no synthetic events.
func New(st Store, types []Type, channels []Channel) (*Service, error) {
	if st == nil {
		return nil, fmt.Errorf("alerts: missing store")
	}
	s := &Service{store: st, types: make(map[string]Type)}
	for _, typ := range types {
		if !validName(typ.Name) || typ.Validate == nil || len(typ.Actions) == 0 {
			return nil, fmt.Errorf("alerts: invalid type registration")
		}
		if _, exists := s.types[typ.Name]; exists {
			return nil, fmt.Errorf("alerts: duplicate type %q", typ.Name)
		}
		actions := make(map[string]Action, len(typ.Actions))
		for name, action := range typ.Actions {
			if !validName(name) || action.Validate == nil || action.Resolve == nil {
				return nil, fmt.Errorf("alerts: invalid action registration")
			}
			actions[name] = action
		}
		typ.Actions = actions
		s.types[typ.Name] = typ
	}
	s.channels = []Channel{{Name: "panel", Deliver: func(ctx context.Context, e Event) error { return st.PublishAlert(ctx, e.ID) }}}
	seen := map[string]bool{"panel": true}
	for _, ch := range channels {
		if !validName(ch.Name) || ch.Deliver == nil || seen[ch.Name] {
			return nil, fmt.Errorf("alerts: invalid channel registration")
		}
		seen[ch.Name] = true
		s.channels = append(s.channels, ch)
	}
	return s, nil
}

func validName(name string) bool {
	return strings.TrimSpace(name) == name && name != "" && len(name) <= 128
}
func personalCaller(ctx context.Context) (domain.Caller, error) {
	c, err := auth.CallerFromContext(ctx)
	if err != nil {
		return domain.Caller{}, err
	}
	if err := domain.ValidateSource(c.Source); err != nil {
		return domain.Caller{}, domain.Internal(err)
	}
	if c.Source == domain.SourceSystem || strings.TrimSpace(c.Principal) == "" {
		return domain.Caller{}, fmt.Errorf("alerts: trusted user identity required")
	}
	return c, nil
}
func (s *Service) typ(e Event) (Type, error) {
	t, ok := s.types[e.Type]
	if !ok {
		return Type{}, fmt.Errorf("alerts: unknown type %q", e.Type)
	}
	if !json.Valid(e.Payload) {
		return Type{}, fmt.Errorf("alerts: malformed payload")
	}
	if err := t.Validate(e.Payload); err != nil {
		return Type{}, fmt.Errorf("alerts: invalid payload: %w", domain.Internal(err))
	}
	return t, nil
}
func (s *Service) decorate(a Alert) (Alert, error) {
	typ, err := s.typ(a.Event)
	if err != nil {
		return Alert{}, err
	}
	a.Actions = make([]string, 0, len(typ.Actions))
	for name := range typ.Actions {
		a.Actions = append(a.Actions, name)
	}
	sort.Strings(a.Actions)
	return a, nil
}

func (s *Service) visible(ctx context.Context, e Event) (bool, error) {
	typ, err := s.typ(e)
	if err != nil {
		return false, err
	}
	if typ.Visible == nil {
		return true, nil
	}
	return typ.Visible(ctx, e)
}

// Publish persists before attempting every channel. A delivery error means the
// stable event already exists; producers must not invent another ID on retry.
// A decision may resolve the event while another channel is still delivering;
// the late channel outcome remains in audit without reopening the event.
func (s *Service) Publish(ctx context.Context, e Event) error {
	caller, err := auth.CallerFromContext(ctx)
	if err != nil {
		return err
	}
	if err := domain.ValidateSource(caller.Source); err != nil {
		return domain.Internal(err)
	}
	if caller.Source != domain.SourceSystem && strings.TrimSpace(caller.Principal) == "" {
		return fmt.Errorf("alerts: missing producer identity")
	}
	if err := domain.ValidateExternalID(e.ID); err != nil {
		return domain.Internal(err)
	}
	if e.At.IsZero() {
		return fmt.Errorf("alerts: missing timestamp")
	}
	if err := domain.ValidateSource(e.Source); err != nil {
		return domain.Internal(err)
	}
	if _, err := s.typ(e); err != nil {
		return err
	}
	if err := s.store.CreateAlert(ctx, e); err != nil {
		return err
	}
	var failures []error
	for _, ch := range s.channels {
		deliveryErr := ch.Deliver(ctx, e)
		d := Delivery{Channel: ch.Name, Failed: deliveryErr != nil}
		code := domain.AuditDetailAlertDelivery
		if d.Failed {
			code = domain.AuditDetailAlertDeliveryFailed
		}
		audit := store.AuditEntry{Actor: caller.Principal, Source: caller.Source, Action: domain.AuditActionAlertDelivery, Detail: domain.NewAuditDetail(code, domain.AuditDetailParams{"alert": e.ID, "channel": ch.Name})}
		if err := s.store.RecordAlertDelivery(ctx, e.ID, d, audit); err != nil {
			failures = append(failures, fmt.Errorf("alerts: recording delivery %s: %w", ch.Name, err))
		}
		if deliveryErr != nil {
			failures = append(failures, fmt.Errorf("alerts: delivery failed for channel %s", ch.Name))
		}
	}
	return errors.Join(failures...)
}

// List never changes read state.
func (s *Service) List(ctx context.Context) (List, error) {
	caller, err := personalCaller(ctx)
	if err != nil {
		return List{}, err
	}
	rows, err := s.store.ListAlerts(ctx, caller.Principal)
	if err != nil {
		return List{}, err
	}
	out := List{Alerts: make([]Alert, 0, len(rows))}
	for _, row := range rows {
		visible, err := s.visible(ctx, row.Event)
		if err != nil {
			return List{}, err
		}
		if !visible {
			continue
		}
		a, err := s.decorate(row)
		if err != nil {
			return List{}, err
		}
		out.Alerts = append(out.Alerts, a)
		out.Total++
		if !a.Read {
			out.Unread++
		}
	}
	return out, nil
}

// Detail reads one event without implicitly changing personal state.
func (s *Service) Detail(ctx context.Context, id string) (Alert, error) {
	caller, err := personalCaller(ctx)
	if err != nil {
		return Alert{}, err
	}
	if err := domain.ValidateExternalID(id); err != nil {
		return Alert{}, err
	}
	a, err := s.store.GetAlert(ctx, id, caller.Principal)
	if err != nil {
		return Alert{}, err
	}
	visible, err := s.visible(ctx, a.Event)
	if err != nil {
		return Alert{}, err
	}
	if !visible {
		return Alert{}, domain.ErrNotFound
	}
	return s.decorate(a)
}

// SetRead changes only the authenticated caller's mark.
func (s *Service) SetRead(ctx context.Context, id string, read bool) error {
	caller, err := personalCaller(ctx)
	if err != nil {
		return err
	}
	if err := domain.ValidateExternalID(id); err != nil {
		return err
	}
	if _, err := s.Detail(ctx, id); err != nil {
		return err
	}
	return s.store.SetAlertRead(ctx, id, caller.Principal, read)
}

// Resolve runs a registered action while storage serialises event decisions.
// Invalid input, a failed handler or a failed transaction leaves the event open.
func (s *Service) Resolve(ctx context.Context, id, name string, input json.RawMessage) (Resolution, error) {
	caller, err := personalCaller(ctx)
	if err != nil {
		return Resolution{}, err
	}
	if err := domain.ValidateExternalID(id); err != nil {
		return Resolution{}, err
	}
	if _, err := s.Detail(ctx, id); err != nil {
		return Resolution{}, err
	}
	if !json.Valid(input) {
		return Resolution{}, fmt.Errorf("alerts: malformed action input: %w", domain.ErrInvalid)
	}
	return s.store.ResolveAlert(ctx, id, func(e Event) (Resolution, store.AuditEntry, error) {
		typ, err := s.typ(e)
		if err != nil {
			return Resolution{}, store.AuditEntry{}, err
		}
		action, ok := typ.Actions[name]
		if !ok {
			return Resolution{}, store.AuditEntry{}, fmt.Errorf("alerts: unknown action %q: %w", name, domain.ErrInvalid)
		}
		if err := action.Validate(input); err != nil {
			return Resolution{}, store.AuditEntry{}, fmt.Errorf("alerts: invalid action input: %w", errors.Join(domain.ErrInvalid, err))
		}
		result, err := action.Resolve(ctx, e, input)
		if err != nil {
			return Resolution{}, store.AuditEntry{}, err
		}
		if !json.Valid(result) {
			return Resolution{}, store.AuditEntry{}, fmt.Errorf("alerts: invalid handler result")
		}
		resolution := Resolution{Event: e, At: time.Now().UTC(), Actor: caller.Principal, Source: caller.Source, Action: name, Result: result}
		audit := store.AuditEntry{Actor: caller.Principal, Source: caller.Source, Action: domain.AuditActionResolveAlert, Detail: domain.NewAuditDetail(domain.AuditDetailResolveAlert, domain.AuditDetailParams{"alert": id, "action": name, "result": string(result)})}
		return resolution, audit, nil
	})
}
