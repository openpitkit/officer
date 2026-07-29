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

package backend_test

import (
	"context"
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
)

func dropCopyOrder(id domain.ExternalID) domain.Order {
	return domain.Order{
		ExternalID:  id,
		Account:     "acc-1",
		BaseAsset:   "AAPL",
		QuoteAsset:  "USD",
		Side:        domain.OrderSideBuy,
		AmountKind:  domain.OrderAmountKindQuantity,
		AmountValue: "1",
		Price:       "100",
	}
}

func TestService_SubmitDropCopyOrderGeneratesExternalIDWhenAbsent(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	ctx := auth.ContextWithCaller(context.Background(), domain.Caller{
		Source: domain.SourcePanel, Principal: "operator",
	})

	created, err := svc.SubmitDropCopyOrder(ctx, dropCopyOrder(""))
	if err != nil {
		t.Fatalf("SubmitDropCopyOrder: %v", err)
	}
	if created.ExternalID.IsZero() {
		t.Fatal("SubmitDropCopyOrder returned an empty generated external id")
	}
	if !created.DropCopy || created.Source != domain.SourcePanel ||
		created.Principal != "operator" ||
		created.Status != domain.OrderStatusCommitted {
		t.Fatalf("created = %+v", created)
	}
	if len(fn.orders) != 1 {
		t.Fatalf("orders = %+v, want exactly one", fn.orders)
	}
	if _, ok := fn.orders[created.ExternalID]; !ok {
		t.Fatalf("generated order id %q was not stored", created.ExternalID)
	}
	if len(fn.attestations) != 0 {
		t.Fatalf("attestations = %+v, want none", fn.attestations)
	}
}

func TestService_SubmitOrderTokenRefusesDropCopy(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	order := dropCopyOrder(mdID("signed-drop-copy"))
	order.DropCopy = true

	if _, err := svc.SubmitOrderToken(
		context.Background(), order, "hold",
	); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SubmitOrderToken error = %v, want ErrInvalid", err)
	}
	if len(fn.orders) != 0 || len(fn.attestations) != 0 {
		t.Fatalf("orders = %+v, attestations = %+v, want none", fn.orders, fn.attestations)
	}
}

func TestService_SubmitDropCopyOrderPreservesSuppliedIDAndConflictsOnDuplicate(
	t *testing.T,
) {
	t.Parallel()
	svc, fn := newTestService()
	id := mdID("drop-copy-order")
	o := dropCopyOrder(id)
	ctx := auth.ContextWithCaller(context.Background(), domain.Caller{
		Source: domain.SourcePanel, Principal: "operator",
	})

	created, err := svc.SubmitDropCopyOrder(ctx, o)
	if err != nil {
		t.Fatalf("SubmitDropCopyOrder: %v", err)
	}
	if created.ExternalID != id {
		t.Fatalf("created external id = %q, want supplied %q", created.ExternalID, id)
	}
	if !created.DropCopy || created.Source != domain.SourcePanel ||
		created.Principal != "operator" ||
		created.Status != domain.OrderStatusCommitted {
		t.Fatalf("created = %+v", created)
	}
	if len(fn.attestations) != 0 {
		t.Fatalf("attestations = %+v, want none", fn.attestations)
	}
	eventCount := len(fn.orderEvents[id])

	_, err = svc.SubmitDropCopyOrder(ctx, o)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate supplied id error = %v, want ErrAlreadyExists", err)
	}
	if got := len(fn.orderEvents[id]); got != eventCount {
		t.Fatalf("event count after duplicate = %d, want %d", got, eventCount)
	}
}

func TestService_DropCopyExecutionReportRemainsUnsignedAndKeepsCaller(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{signErr: errors.New("signer must not be called")}
	svc, fn := newTestServiceWithSigner(signer)
	id := mdID("drop-copy-execution-report")
	order := dropCopyOrder(id)
	order.DropCopy = true
	order.Source = domain.SourcePanel
	order.Principal = "submitter"
	order.Status = domain.OrderStatusCommitted
	fn.orders[id] = order
	ctx := auth.ContextWithCaller(context.Background(), domain.Caller{
		Source: domain.SourcePanel, Principal: "operator",
	})

	result, attestation, err := svc.ApplyExecutionReport(
		ctx,
		domain.ExecutionReportInput{
			Order: id, Account: "acc-1", BaseAsset: "AAPL", QuoteAsset: "USD",
			Side: domain.OrderSideBuy, FillQuantity: "1", FillPrice: "100",
			LeavesQuantity: "0", OrderStatus: domain.OrderStatusFilled,
		},
	)
	if err != nil {
		t.Fatalf("ApplyExecutionReport: %v", err)
	}
	if result.Persistence == nil {
		t.Fatal("ApplyExecutionReport persistence = nil")
	}
	if attestation.Token != "" || attestation.Signed ||
		len(signer.signed) != 0 || len(fn.attestations) != 0 ||
		len(fn.persistAttestationCalls) != 0 {
		t.Fatalf(
			"attestation = %+v, signer calls = %+v, stored = %+v, persistence calls = %+v",
			attestation, signer.signed, fn.attestations, fn.persistAttestationCalls,
		)
	}
	if len(fn.orderEvents[id]) == 0 {
		t.Fatal("drop-copy execution report was not applied")
	}
	if len(fn.execReportCallers) != 1 ||
		fn.execReportCallers[0].Source != domain.SourcePanel ||
		fn.execReportCallers[0].Principal != "operator" {
		t.Fatalf("execution-report callers = %+v", fn.execReportCallers)
	}
}

func TestService_DropCopySigningShortcutsAreRefused(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService()
	id := mdID("drop-copy-shortcuts")
	order := dropCopyOrder(id)
	order.DropCopy = true
	order.Source = domain.SourcePanel
	order.Status = domain.OrderStatusCommitted
	fn.orders[id] = order

	for name, call := range map[string]func() error{
		"confirm": func() error {
			_, _, err := svc.ConfirmExecution(context.Background(), id.String(), "token")
			return err
		},
		"cancel": func() error {
			_, _, err := svc.CancelOrder(context.Background(), id.String(), "token", "")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
	if len(fn.attestations) != 0 {
		t.Fatalf("attestations = %+v, want none", fn.attestations)
	}
}
