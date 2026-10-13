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

package mcptools

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// The account tools map their JSON arguments 1:1 onto the account calls REST
// makes and leave every value rule, the single-account refusal included, to
// the core.

const (
	createAccountToolName      = "create_account"
	updateAccountToolName      = "update_account"
	deleteAccountToolName      = "delete_account"
	setAccountCurrencyToolName = "set_account_currency"
)

const (
	createAccountToolDescription = "Create an account with a code, an optional " +
		"title and an optional realized P&L currency. Without its own currency " +
		"the account inherits its group's, then the default group's; with no " +
		"tier set it tracks no realized P&L. Mutates Officer control-plane " +
		"config; protected and disabled by default."
	updateAccountToolDescription = "Change an account's code and title; the " +
		"title replaces the stored one and an empty title clears it. Mutates " +
		"Officer control-plane config; protected and disabled by default."
	deleteAccountToolDescription = "Delete an account. An account with " +
		"dependent rows is refused with has_dependents unless force deletes them " +
		"with it; a trading connection's fee account is refused with conflict, " +
		"even with force. Force-deleting removes the account's venue-fee " +
		"adjustments, so fees it received may be booked again to the " +
		"connection's current fee account on the next restart. Mutates Officer " +
		"control-plane config; protected and disabled by default."
	setAccountCurrencyToolDescription = "Set or clear an account's own " +
		"realized P&L currency; an empty currency clears it, so the account " +
		"inherits its group's, then the default group's, and tracks no realized " +
		"P&L when no tier is set. A change of the account's effective currency " +
		"is refused with currency_change_blocked while the account holds any " +
		"non-zero balance, position, cost-basis or P&L value, a P&L halt or an " +
		"active order, or while a Spot Funds P&L bound applies to it. Mutates " +
		"Officer control-plane config; protected and disabled by default."
)

type createAccountInput struct {
	Code     string `json:"code" jsonschema:"Account code"`
	Title    string `json:"title,omitempty" jsonschema:"Account title; omitted leaves the account untitled"`
	Currency string `json:"currency,omitempty" jsonschema:"Realized P&L currency asset code; omitted sets none, so the account inherits its group's, then the default group's, and tracks no realized P&L when no tier is set"`
}

type updateAccountInput struct {
	Account string `json:"account" jsonschema:"Current account code"`
	Code    string `json:"code" jsonschema:"New account code; equal to account to keep it"`
	Title   string `json:"title" jsonschema:"New account title, replacing the stored one; empty clears it"`
}

type deleteAccountInput struct {
	Account string `json:"account" jsonschema:"Account code"`
	Force   bool   `json:"force,omitempty" jsonschema:"Also delete the account's dependent rows, its venue-fee adjustments included, so fees it received may be booked again to the connection's current fee account on the next restart; without it an account with dependents is refused with has_dependents. A trading connection's fee account is refused even with force"`
}

type setAccountCurrencyInput struct {
	Account  string `json:"account" jsonschema:"Account code"`
	Currency string `json:"currency" jsonschema:"Realized P&L currency asset code; empty clears the account's own, so the account inherits its group's, then the default group's"`
}

// accountOutput is the REST account body, {"account": {...}}.
type accountOutput struct {
	Account httpx.AccountDTO `json:"account"`
}

func createAccountHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[createAccountInput, accountOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in createAccountInput,
	) (string, accountOutput, error) {
		account, err := src.CreateAccount(ctx, domain.Account{
			Code:     domain.AccountID(in.Code),
			Title:    in.Title,
			Currency: in.Currency,
		})
		if err != nil {
			return "", accountOutput{}, toolError(err)
		}
		return accountResult(ctx, src, account, "created")
	}
}

func updateAccountHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[updateAccountInput, accountOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in updateAccountInput,
	) (string, accountOutput, error) {
		account, err := src.UpdateAccount(ctx, domain.AccountID(in.Account), domain.Account{
			Code:  domain.AccountID(in.Code),
			Title: in.Title,
		})
		if err != nil {
			return "", accountOutput{}, toolError(err)
		}
		return accountResult(ctx, src, account, "updated")
	}
}

func deleteAccountHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[deleteAccountInput, deleteAccountInput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in deleteAccountInput,
	) (string, deleteAccountInput, error) {
		if err := src.DeleteAccount(ctx, domain.AccountID(in.Account), in.Force); err != nil {
			return "", deleteAccountInput{}, toolError(err)
		}
		return fmt.Sprintf("account %s deleted", in.Account), in, nil
	}
}

// setAccountCurrencyHandler sets or clears the currency and answers, as REST
// does, with the account read back under the same account argument.
func setAccountCurrencyHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[setAccountCurrencyInput, accountOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in setAccountCurrencyInput,
	) (string, accountOutput, error) {
		id := domain.AccountID(in.Account)
		if err := src.SetAccountCurrency(ctx, id, in.Currency); err != nil {
			return "", accountOutput{}, toolError(err)
		}
		account, _, err := src.GetAccountState(ctx, id)
		if err != nil {
			return "", accountOutput{}, toolError(err)
		}
		verb := "currency set"
		if in.Currency == "" {
			verb = "currency cleared"
		}
		return accountResult(ctx, src, account, verb)
	}
}

// accountResult is the result of a tool that returns the stored account, as
// REST writes it: the account's block joined with its group's.
func accountResult(
	ctx context.Context, src frameworkmcp.Source, account domain.Account, verb string,
) (string, accountOutput, error) {
	block, err := accountBlock(ctx, src, account)
	if err != nil {
		return "", accountOutput{}, toolError(err)
	}
	out := accountOutput{Account: httpx.ToAccountDTO(account, block)}
	return fmt.Sprintf("account %s %s", account.Code, verb), out, nil
}
