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
	"context"

	"go.openpit.dev/openpit/asyncengine"
)

func assignedWithoutFinally() {
	builder := asyncengine.Chain(source, begin)
	runner := builder.Then(settle)
	// ruleid: account-chain-finally
	runner.Run(ctx, engine).Await(ctx)
}

func directWithoutFinally() {
	builder := asyncengine.Chain(source, begin)
	// ruleid: account-chain-finally
	builder.Then(settle).Run(ctx, engine)
}

func inlineWithoutFinally() {
	// ruleid: account-chain-finally
	asyncengine.Chain(source, begin).Then(settle).Then(audit).Run(ctx, engine)
}

func bareBuilderWithoutFinally() {
	runner := asyncengine.Chain(source, begin)
	// ruleid: account-chain-finally
	runner.Run(ctx, engine)
}

func inlineAssignedWithoutFinally() {
	runner := asyncengine.Chain(source, begin).Then(settle)
	// ruleid: account-chain-finally
	runner.Run(ctx, engine)
}

func aliasedWithoutFinally() {
	builder := asyncengine.Chain(source, begin)
	runner := builder
	// ruleid: account-chain-finally
	runner.Run(ctx, engine)
}

func inlineAssignedWithFinally() {
	// ruleid: account-chain-finally-outcome
	runner := asyncengine.Chain(source, begin).Then(settle).Finally(terminal)
	// ok: account-chain-finally
	runner.Run(ctx, engine)
}

func assignedWithFinally() {
	builder := asyncengine.Chain(source, begin)
	// ruleid: account-chain-finally-outcome
	runner := builder.Finally(terminal)
	// ok: account-chain-finally
	runner.Run(ctx, engine)
}

func assignedWithStepsAndFinally() {
	builder := asyncengine.Chain(source, begin)
	// ruleid: account-chain-finally-outcome
	runner := builder.Then(settle).Then(audit).Finally(terminal)
	// ok: account-chain-finally
	runner.Run(ctx, engine)
}

func directWithFinally() {
	builder := asyncengine.Chain(source, begin)
	// ok: account-chain-finally
	// ruleid: account-chain-finally-outcome
	builder.Then(settle).Finally(terminal).Run(ctx, engine)
}

func checkOrder() {
	chain := asyncengine.Chain(source, begin).CheckOrder(checked)
	// ok: account-chain-finally
	chain.Run(ctx, engine).Await(ctx)
}

func inlineCheckOrder() {
	// ok: account-chain-finally
	asyncengine.Chain(source, begin).CheckOrder(checked).Run(ctx, engine)
}

func directCheckOrder() {
	builder := asyncengine.Chain(source, begin)
	// ruleid: account-chain-finally
	builder.CheckOrder(checked).Run(ctx, engine)
}

func assignedCheckOrder() {
	builder := asyncengine.Chain(source, begin)
	runner := builder.CheckOrder(checked)
	// ruleid: account-chain-finally
	runner.Run(ctx, engine)
}

func splitCheckOrderThen() {
	chain := asyncengine.Chain(source, begin).CheckOrder(checked)
	chain.Then(settle)
	// ruleid: account-chain-finally
	chain.Run(ctx, engine)
}

func nestedCheckOrderThen(extend bool) {
	chain := asyncengine.Chain(source, begin).CheckOrder(checked)
	if extend {
		chain.Then(settle)
	}
	// ruleid: account-chain-finally
	chain.Run(ctx, engine)
}

func splitCheckOrderExecutePreTrade() {
	chain := asyncengine.Chain(source, begin).CheckOrder(checked)
	chain.ExecutePreTrade(preTrade)
	// ruleid: account-chain-finally
	chain.Run(ctx, engine)
}

func splitCheckOrderApplyDropCopy() {
	chain := asyncengine.Chain(source, begin).CheckOrder(checked)
	chain.ApplyDropCopy(dropCopy)
	// ruleid: account-chain-finally
	chain.Run(ctx, engine)
}

func splitCheckOrderApplyExecutionReport() {
	chain := asyncengine.Chain(source, begin).CheckOrder(checked)
	chain.ApplyExecutionReport(report)
	// ruleid: account-chain-finally
	chain.Run(ctx, engine)
}

func splitCheckOrderApplyAccountAdjustment() {
	chain := asyncengine.Chain(source, begin).CheckOrder(checked)
	chain.ApplyAccountAdjustment(adjustment)
	// ruleid: account-chain-finally
	chain.Run(ctx, engine)
}

func splitCheckOrderSetAccountCurrency() {
	chain := asyncengine.Chain(source, begin).CheckOrder(checked)
	chain.SetAccountCurrency(currencyHooks)
	// ruleid: account-chain-finally
	chain.Run(ctx, engine)
}

func checkOrderFollowedByWrite() {
	// ruleid: account-chain-finally
	asyncengine.Chain(source, begin).CheckOrder(checked).Then(settle).Run(ctx, engine)
}

func writeBeforeInlineCheckOrder() {
	// ruleid: account-chain-finally
	asyncengine.Chain(source, begin).Then(settle).CheckOrder(checked).Run(ctx, engine)
}

type reviewState struct{}
type reviewNode struct{}

func parameterWithoutFinally(builder *asyncengine.ChainBuilder[*reviewState]) {
	// ruleid: account-chain-finally
	builder.Then(settle).Run(ctx, engine)
}

func parameterWithFinally(builder *asyncengine.ChainBuilder[*reviewState]) {
	// ok: account-chain-finally
	// ruleid: account-chain-finally-outcome
	builder.Then(settle).Finally(terminal).Run(ctx, engine)
}

func (n *reviewNode) parameterWithoutFinally(builder *asyncengine.ChainBuilder[*reviewState]) error {
	runner := builder.Then(settle)
	// ruleid: account-chain-finally
	runner.Run(ctx, engine).Await(ctx)
	return nil
}

func (n *reviewNode) parameterWithFinally(builder *asyncengine.ChainBuilder[*reviewState]) error {
	// ruleid: account-chain-finally-outcome
	runner := builder.Then(settle).Finally(terminal)
	// ok: account-chain-finally
	runner.Run(ctx, engine).Await(ctx)
	return nil
}

func unrelatedRun() {
	builder := asyncengine.Chain(source, begin)
	// ruleid: account-chain-finally-outcome
	builder.Finally(terminal).Run(ctx, engine)
	runner := task.Then(work)
	// ok: account-chain-finally
	runner.Run(ctx, engine)
}

func finallyBlankOutcome() {
	// ruleid: account-chain-finally-outcome
	asyncengine.Chain(source, begin).Finally(func(_ context.Context, state *reviewState, _ asyncengine.ChainOutcome) error {
		return nil
	}).Run(ctx, engine)
}

func finallyUnnamedOutcome() {
	// ruleid: account-chain-finally-outcome
	asyncengine.Chain(source, begin).Finally(func(context.Context, *reviewState, asyncengine.ChainOutcome) error {
		return nil
	}).Run(ctx, engine)
}

func finallyUnusedOutcome() {
	// ruleid: account-chain-finally-outcome
	asyncengine.Chain(source, begin).Finally(func(_ context.Context, state *reviewState, outcome asyncengine.ChainOutcome) error {
		return nil
	}).Run(ctx, engine)
}

func finallyOutcomePassedToHelper() {
	// ok: account-chain-finally-outcome
	asyncengine.Chain(source, begin).Finally(func(_ context.Context, state *reviewState, outcome asyncengine.ChainOutcome) error {
		return terminalOutcome(outcome)
	}).Run(ctx, engine)
}

func finallyOutcomeErrorRead() {
	// ok: account-chain-finally-outcome
	asyncengine.Chain(source, begin).Finally(func(_ context.Context, state *reviewState, outcome asyncengine.ChainOutcome) error {
		if outcome.Err != nil {
			return terminalFailure()
		}
		return nil
	}).Run(ctx, engine)
}

func finallyOutcomeErrorAssigned() {
	// ok: account-chain-finally-outcome
	asyncengine.Chain(source, begin).Finally(func(_ context.Context, state *reviewState, outcome asyncengine.ChainOutcome) error {
		state.err = outcome.Err
		return nil
	}).Run(ctx, engine)
}

func finallyOutcomePassedToMultilineHelper() {
	// ok: account-chain-finally-outcome
	asyncengine.Chain(source, begin).Finally(func(_ context.Context, state *reviewState, outcome asyncengine.ChainOutcome) error {
		state.engineApplied, state.err = runtimeChainTerminalError(
			state.err, state.engineApplied, outcome,
		)
		return nil
	}).Run(ctx, engine)
}

func finallyNamedFunction() {
	// ruleid: account-chain-finally-outcome
	builder.Finally(terminal)
}

func finallyMethodValue(n *reviewNode) {
	// ruleid: account-chain-finally-outcome
	builder.Finally(n.finish)
}

func finallyFunctionVariable() {
	finally := func(_ context.Context, state *reviewState, _ asyncengine.ChainOutcome) error {
		return nil
	}
	// ruleid: account-chain-finally-outcome
	builder.Finally(finally)
}

func finallyOutcomeInNestedBlock() {
	// ok: account-chain-finally-outcome
	builder.Finally(func(_ context.Context, state *reviewState, outcome asyncengine.ChainOutcome) error {
		if state != nil {
			return terminalOutcome(outcome)
		}
		return nil
	})
}

func finallyOutcomeReferencedWithoutEscalation() {
	// ok: account-chain-finally-outcome
	builder.Finally(func(_ context.Context, state *reviewState, outcome asyncengine.ChainOutcome) error {
		_ = outcome
		return nil
	})
}

func finallyMethodAlias() {
	f := builder.Finally
	// ok: account-chain-finally-outcome
	f(func(_ context.Context, state *reviewState, _ asyncengine.ChainOutcome) error {
		return nil
	})
}
