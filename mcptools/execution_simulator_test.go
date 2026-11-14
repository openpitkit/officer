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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/framework/web/httpapi/dto"
)

type testPolicyErrorSource struct {
	frameworkmcp.Source
	err error
}

func (s testPolicyErrorSource) GetTestExecutionPolicy(context.Context, string) (domain.TestExecutionPolicyState, error) {
	return domain.TestExecutionPolicyState{}, s.err
}

func (s testPolicyErrorSource) ReplaceTestExecutionPolicy(context.Context, string, domain.TestExecutionPolicy) (domain.TestExecutionPolicyState, error) {
	return domain.TestExecutionPolicyState{}, s.err
}

type policyPrivateError struct {
	cause       error
	Credentials map[string]string
}

func (e *policyPrivateError) Error() string { return e.cause.Error() }
func (e *policyPrivateError) Unwrap() error { return e.cause }
func (e *policyPrivateError) Metadata() map[string]any {
	return map[string]any{"private": "metadata-secret", "Credentials": e.Credentials}
}

func TestPolicyErrorsCarryOnlyPublicRESTBody(t *testing.T) {
	for _, cause := range []error{
		domain.NewValidationError("/rules/0/mask/baseAsset", "required", "base asset mask: asset is required"),
		domain.ErrExecutionPolicyUnsupported,
		domain.ErrTradingAccessNotification,
		errors.New("internal cause with raw-secret"),
	} {
		for _, name := range []string{"get", "set"} {
			t.Run(name+"/"+cause.Error(), func(t *testing.T) {
				private := &policyPrivateError{cause: cause, Credentials: map[string]string{"apiKey": "credential-secret"}}
				err := httpx.WithResponseHeader(private, "Retry-After", "header-secret")
				src := testPolicyErrorSource{err: err}
				var got error
				if name == "get" {
					_, _, got = getTestExecutionPolicyHandler(src)(context.Background(), nil, getTestExecutionPolicyInput{Connection: "test"})
				} else {
					_, _, got = setTestExecutionPolicyHandler(src)(context.Background(), nil, setTestExecutionPolicyInput{Connection: "test", Policy: dto.TestExecutionPolicyDTO{Rules: []dto.TestExecutionRuleDTO{}}})
				}
				var public *frameworkmcp.PublicToolError
				if !errors.As(got, &public) {
					t.Fatalf("handler returned %T: %v, want PublicToolError", got, got)
				}
				rest := httptest.NewRecorder()
				httpx.WriteErr(rest, err)
				body, marshalErr := json.Marshal(public.Body)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if !bytes.Equal(append(body, '\n'), rest.Body.Bytes()) {
					t.Fatalf("MCP body=%s REST=%s", body, rest.Body)
				}
				message := httpx.DescribeErr(err).Message
				if public.Message != message || public.Error() != message {
					t.Fatalf("message=%q want %q", public.Message, message)
				}
				wire := string(body) + public.Message
				for _, forbidden := range []string{"metadata-secret", "credential-secret", "header-secret", "raw-secret", "Credentials", "Metadata"} {
					if strings.Contains(wire, forbidden) {
						t.Fatalf("public result leaked %q: %s", forbidden, wire)
					}
				}
			})
		}
	}
}

type testPolicyWireSource struct {
	testPolicyErrorSource
}

func (testPolicyWireSource) AccountFeatures() backend.AccountFeatures {
	return backend.EveryAccount{}.Features()
}

func (testPolicyWireSource) CommandEnabled(context.Context, string) (bool, error) {
	return true, nil
}

func testPolicySession(t *testing.T, reg *frameworkmcp.ToolRegistry, src frameworkmcp.Source) *sdkmcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	server, err := frameworkmcp.Build(reg, src, nil, httpx.AllowAll{}, domain.Caller{})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Run(ctx, serverTransport) }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "policy-test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		runErr := <-serverErr
		t.Fatalf("connect: %v (server run: %v)", err, runErr)
	}
	t.Cleanup(func() {
		cancel()
		if err := <-serverErr; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("server run: %v", err)
		}
		if err := session.Close(); err != nil {
			t.Errorf("client close: %v", err)
		}
	})
	return session
}

func TestPolicyWireErrorsMatchREST(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"validation", domain.NewValidationError("/rules/0/mask/baseAsset", "required", "base asset mask: asset is required"), http.StatusUnprocessableEntity, "validation"},
		{"not_found", fmt.Errorf("connection missing: %w", domain.ErrNotFound), http.StatusNotFound, "not_found"},
		{"execution_policy_unsupported", fmt.Errorf("venue connection: %w", domain.ErrExecutionPolicyUnsupported), http.StatusConflict, "execution_policy_unsupported"},
		{"trading_access_notification_failed", fmt.Errorf("committed warning: %w", domain.ErrTradingAccessNotification), http.StatusConflict, "trading_access_notification_failed"},
		{"internal", errors.New("private internal failure"), http.StatusInternalServerError, "internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := frameworkmcp.NewToolRegistry()
			RegisterTools(reg, nil)
			session := testPolicySession(t, reg, testPolicyWireSource{testPolicyErrorSource{err: tc.err}})
			rest := httptest.NewRecorder()
			httpx.WriteErr(rest, tc.err)
			if rest.Code != tc.status {
				t.Fatalf("REST status=%d, want %d", rest.Code, tc.status)
			}
			var body struct {
				Detail string               `json:"detail"`
				Errors []httpx.ProblemError `json:"errors"`
				Error  struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rest.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			message := body.Error.Message
			if tc.code == "validation" {
				message = body.Detail
				if len(body.Errors) != 1 || body.Errors[0].Code != tc.code || body.Errors[0].Pointer != "/rules/0/mask/baseAsset" || body.Errors[0].Constraint != "required" {
					t.Fatalf("validation metadata=%+v", body.Errors)
				}
			} else if body.Error.Code != tc.code {
				t.Fatalf("REST code=%q, want %q", body.Error.Code, tc.code)
			}
			for _, name := range []string{"get_test_execution_policy", "set_test_execution_policy"} {
				t.Run(name, func(t *testing.T) {
					args := map[string]any{"connection": "test"}
					if name == "set_test_execution_policy" {
						args["policy"] = map[string]any{"rules": []any{}}
					}
					result, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: name, Arguments: args})
					if err != nil {
						t.Fatal(err)
					}
					if !result.IsError {
						t.Fatal("policy error reported as success")
					}
					raw, err := json.Marshal(result.StructuredContent)
					if err != nil {
						t.Fatal(err)
					}
					// Canonicalize the REST JSON because the SDK decodes structuredContent into maps.
					var restBody any
					if err := json.Unmarshal(rest.Body.Bytes(), &restBody); err != nil {
						t.Fatal(err)
					}
					want, err := json.Marshal(restBody)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(raw, want) {
						t.Fatalf("structuredContent=%s, REST=%s", raw, want)
					}
					if len(result.Content) != 1 {
						t.Fatalf("content=%+v, want one text item", result.Content)
					}
					text, ok := result.Content[0].(*sdkmcp.TextContent)
					if !ok || text.Text != message {
						t.Fatalf("text=%+v, want %q", result.Content[0], message)
					}
				})
			}
		})
	}
}

func plainPolicyTestSchema[In, Out any](t *testing.T, _ func(frameworkmcp.Source) frameworkmcp.ToolBody[In, Out]) []byte {
	t.Helper()
	schema, err := outputSchemaFor[Out]()
	if err != nil {
		t.Fatal(err)
	}
	return policyTestSchemaJSON(t, schema)
}

func policyTestSchemaJSON(t *testing.T, schema any) []byte {
	t.Helper()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPolicyRegistrationOutputSchemas(t *testing.T) {
	plain := map[string][]byte{
		"health":                         plainPolicyTestSchema(t, healthHandler),
		"get_account_state":              plainPolicyTestSchema(t, getAccountStateHandler),
		"get_limits":                     plainPolicyTestSchema(t, getLimitsHandler),
		"get_spot_funds_limit_mode":      plainPolicyTestSchema(t, getSpotFundsLimitModeHandler),
		"get_order":                      plainPolicyTestSchema(t, getOrderHandler),
		"get_audit":                      plainPolicyTestSchema(t, getAuditHandler),
		"list_orders":                    plainPolicyTestSchema(t, listOrdersHandler),
		"list_trades":                    plainPolicyTestSchema(t, listTradesHandler),
		"list_accounts":                  plainPolicyTestSchema(t, listAccountsHandler),
		"list_groups":                    plainPolicyTestSchema(t, listGroupsHandler),
		"check_order":                    plainPolicyTestSchema(t, checkOrderHandler),
		"set_limit":                      plainPolicyTestSchema(t, setLimitHandler),
		"create_account":                 plainPolicyTestSchema(t, createAccountHandler),
		"update_account":                 plainPolicyTestSchema(t, updateAccountHandler),
		"delete_account":                 plainPolicyTestSchema(t, deleteAccountHandler),
		"set_account_currency":           plainPolicyTestSchema(t, setAccountCurrencyHandler),
		"apply_adjustment":               plainPolicyTestSchema(t, applyAdjustmentHandler),
		"create_asset":                   plainPolicyTestSchema(t, createAssetHandler),
		"update_asset":                   plainPolicyTestSchema(t, updateAssetHandler),
		"delete_asset":                   plainPolicyTestSchema(t, deleteAssetHandler),
		"create_group":                   plainPolicyTestSchema(t, createGroupHandler),
		"update_group":                   plainPolicyTestSchema(t, updateGroupHandler),
		"delete_group":                   plainPolicyTestSchema(t, deleteGroupHandler),
		"set_market_data_instrument":     plainPolicyTestSchema(t, setMarketDataInstrumentHandler),
		"submit_order":                   plainPolicyTestSchema(t, submitOrderHandler),
		"submit_drop_copy_order":         plainPolicyTestSchema(t, submitDropCopyOrderHandler),
		"confirm_execution":              plainPolicyTestSchema(t, confirmExecutionHandler),
		"cancel":                         plainPolicyTestSchema(t, cancelHandler),
		"report_fill":                    plainPolicyTestSchema(t, reportFillHandler),
		"send_paper_order":               plainPolicyTestSchema(t, sendVenueOrderHandler(frameworkmcp.Source.SendPaperOrder)),
		"send_live_order":                plainPolicyTestSchema(t, sendVenueOrderHandler(frameworkmcp.Source.SendLiveOrder)),
		"get_trading":                    plainPolicyTestSchema(t, getTradingHandler),
		"get_credential_storage":         plainPolicyTestSchema(t, getCredentialStorageHandler),
		"create_trading_connection":      plainPolicyTestSchema(t, createTradingConnectionHandler),
		"update_trading_connection":      plainPolicyTestSchema(t, updateTradingConnectionHandler),
		"set_trading_connection_enabled": plainPolicyTestSchema(t, setTradingConnectionEnabledHandler),
		"check_trading_connection":       plainPolicyTestSchema(t, checkTradingConnectionHandler),
		"set_trading_instrument":         plainPolicyTestSchema(t, setTradingInstrumentHandler),
		"delete_trading_instrument":      plainPolicyTestSchema(t, deleteTradingInstrumentHandler),
		"verify_trading_symbol":          plainPolicyTestSchema(t, verifyTradingSymbolHandler),
		"search_trading_symbols":         plainPolicyTestSchema(t, searchTradingSymbolsHandler),
		"grant_trading_access":           plainPolicyTestSchema(t, grantTradingAccessHandler),
		"revoke_trading_access":          plainPolicyTestSchema(t, revokeTradingAccessHandler),
		"set_trading_fee_account":        plainPolicyTestSchema(t, setTradingFeeAccountHandler),
		"clear_trading_fee_account":      plainPolicyTestSchema(t, clearTradingFeeAccountHandler),
		"list_trading_destinations":      plainPolicyTestSchema(t, listTradingDestinationsHandler),
	}
	state, err := outputSchemaFor[dto.TestExecutionPolicyStateDTO]()
	if err != nil {
		t.Fatal(err)
	}
	union := policyTestSchemaJSON(t, testExecutionPolicyOutputSchema(state))
	reg := frameworkmcp.NewToolRegistry()
	RegisterTools(reg, nil)
	session := testPolicySession(t, reg, testPolicyWireSource{})
	list, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != len(plain)+2 {
		t.Fatalf("published %d tools, want %d", len(list.Tools), len(plain)+2)
	}
	for _, tool := range list.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			want, ok := plain[tool.Name]
			if tool.Name == "get_test_execution_policy" || tool.Name == "set_test_execution_policy" {
				want, ok = union, true
			}
			if !ok {
				t.Fatalf("unexpected tool %q", tool.Name)
			}
			if got := policyTestSchemaJSON(t, tool.OutputSchema); !bytes.Equal(got, want) {
				t.Fatalf("OutputSchema=%s, want %s", got, want)
			}
		})
	}
}

type testPolicySchemaSource struct {
	testPolicyWireSource
	features backend.AccountFeatures
}

func (s testPolicySchemaSource) AccountFeatures() backend.AccountFeatures { return s.features }

func TestPolicyRegistrationInputSchema(t *testing.T) {
	for _, account := range []domain.AccountID{"", "main"} {
		t.Run(string(account), func(t *testing.T) {
			reg := frameworkmcp.NewToolRegistry()
			RegisterTools(reg, nil)
			session := testPolicySession(t, reg, testPolicySchemaSource{features: backend.AccountFeatures{Account: account}})
			list, err := session.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			var schema jsonschema.Schema
			found := false
			for _, tool := range list.Tools {
				if tool.Name == "set_test_execution_policy" {
					found = true
					for _, meaning := range []string{"complete execution policy", "current quotes", "historical replay", "Exactly one rule", "empty policy"} {
						if !strings.Contains(tool.Description, meaning) {
							t.Fatalf("served description omits %q: %s", meaning, tool.Description)
						}
					}
					if err := json.Unmarshal(policyTestSchemaJSON(t, tool.InputSchema), &schema); err != nil {
						t.Fatal(err)
					}
				}
			}
			if !found {
				t.Fatal("set_test_execution_policy not published")
			}
			rule := schema.Properties["policy"].Properties["rules"].Items
			for _, description := range []struct {
				name, text, meaning string
			}{
				{"connection", schema.Properties["connection"].Description, "test trading connection id"},
				{"policy", schema.Properties["policy"].Description, "Complete replacement"},
				{"rules", schema.Properties["policy"].Properties["rules"].Description, "first matching"},
				{"mask", rule.Properties["mask"].Description, "'*' for both"},
				{"mode", rule.Properties["mode"].Description, "connection and revision"},
				{"priceVariant", rule.Properties["priceVariant"].Description, "best buys at bid and sells at ask"},
				{"fills", rule.Properties["fills"].Description, "submitted quantity's decimal precision"},
				{"wait", rule.Properties["wait"].Description, "runtime restart"},
			} {
				if !strings.Contains(description.text, description.meaning) {
					t.Fatalf("served %s description omits %q: %s", description.name, description.meaning, description.text)
				}
			}
			for _, field := range []struct {
				name   string
				values []any
			}{
				{"mode", []any{"execute", "split", "reject_even", "rest"}},
				{"priceVariant", []any{"normal", "best", "worst"}},
				{"fills", []any{2, 3}},
			} {
				if !jsonschema.Equal(rule.Properties[field.name].Enum, field.values) {
					t.Fatalf("%s enum=%v, want %v", field.name, rule.Properties[field.name].Enum, field.values)
				}
			}
			if !jsonschema.Equal(rule.Properties["wait"].Properties["kind"].Enum, []any{"finite", "infinite"}) {
				t.Fatal("wait kind enum is missing")
			}
			resolved, err := schema.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				name, policy string
				valid        bool
			}{
				{"empty rules", `{"rules":[]}`, true},
				{"missing rules", `{}`, false},
				{"null rules", `{"rules":null}`, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var input any
					if err := json.Unmarshal([]byte(`{"connection":"test","policy":`+tc.policy+`}`), &input); err != nil {
						t.Fatal(err)
					}
					if err := resolved.Validate(input); (err == nil) != tc.valid {
						t.Fatalf("schema validation=%v, want valid=%v", err, tc.valid)
					}
				})
			}
			for _, tc := range []struct {
				name, fields string
				valid        bool
			}{
				{"execute", `"mode":"execute","priceVariant":"normal"`, true},
				{"reject even", `"mode":"reject_even","priceVariant":"best"`, true},
				{"split two", `"mode":"split","priceVariant":"worst","fills":2`, true},
				{"split three", `"mode":"split","priceVariant":"normal","fills":3`, true},
				{"finite", `"mode":"rest","priceVariant":"best","wait":{"kind":"finite","seconds":1}`, true},
				{"finite maximum", `"mode":"rest","priceVariant":"worst","wait":{"kind":"finite","seconds":9223372036}`, true},
				{"infinite", `"mode":"rest","wait":{"kind":"infinite"}`, true},
				{"missing mode", `"priceVariant":"normal"`, false},
				{"unknown mode", `"mode":"other","priceVariant":"normal"`, false},
				{"unknown price variant", `"mode":"execute","priceVariant":"other"`, false},
				{"execute missing variant", `"mode":"execute"`, false},
				{"reject even missing variant", `"mode":"reject_even"`, false},
				{"split missing variant", `"mode":"split","fills":2`, false},
				{"split missing fills", `"mode":"split","priceVariant":"normal"`, false},
				{"split one fill", `"mode":"split","priceVariant":"normal","fills":1`, false},
				{"split four fills", `"mode":"split","priceVariant":"normal","fills":4`, false},
				{"split fractional fills", `"mode":"split","priceVariant":"normal","fills":2.5`, false},
				{"execute forbids fills", `"mode":"execute","priceVariant":"normal","fills":2`, false},
				{"execute forbids null fills", `"mode":"execute","priceVariant":"normal","fills":null`, false},
				{"reject even forbids fills", `"mode":"reject_even","priceVariant":"normal","fills":3`, false},
				{"execute forbids wait", `"mode":"execute","priceVariant":"normal","wait":{"kind":"infinite"}`, false},
				{"reject even forbids wait", `"mode":"reject_even","priceVariant":"normal","wait":{"kind":"finite","seconds":1}`, false},
				{"split forbids wait", `"mode":"split","priceVariant":"normal","fills":2,"wait":{"kind":"infinite"}`, false},
				{"rest requires wait", `"mode":"rest","priceVariant":"normal"`, false},
				{"rest forbids fills", `"mode":"rest","wait":{"kind":"infinite"},"fills":2`, false},
				{"finite rest forbids fills", `"mode":"rest","priceVariant":"normal","wait":{"kind":"finite","seconds":1},"fills":3`, false},
				{"finite requires variant", `"mode":"rest","wait":{"kind":"finite","seconds":1}`, false},
				{"finite requires seconds", `"mode":"rest","priceVariant":"normal","wait":{"kind":"finite"}`, false},
				{"finite positive seconds", `"mode":"rest","priceVariant":"normal","wait":{"kind":"finite","seconds":0}`, false},
				{"finite overflow seconds", `"mode":"rest","priceVariant":"normal","wait":{"kind":"finite","seconds":9223372037}`, false},
				{"finite integral seconds", `"mode":"rest","priceVariant":"normal","wait":{"kind":"finite","seconds":1.5}`, false},
				{"infinite forbids variant", `"mode":"rest","priceVariant":"normal","wait":{"kind":"infinite"}`, false},
				{"infinite forbids null variant", `"mode":"rest","priceVariant":null,"wait":{"kind":"infinite"}`, false},
				{"infinite forbids seconds", `"mode":"rest","wait":{"kind":"infinite","seconds":1}`, false},
				{"infinite forbids null seconds", `"mode":"rest","wait":{"kind":"infinite","seconds":null}`, false},
				{"unknown wait kind", `"mode":"rest","wait":{"kind":"other"}`, false},
				{"null wait", `"mode":"rest","wait":null`, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					raw := []byte(`{"connection":"test","policy":{"rules":[{"mask":{"baseAsset":"*","quoteAsset":"*"},` + tc.fields + `}]}}`)
					var input any
					if err := json.Unmarshal(raw, &input); err != nil {
						t.Fatal(err)
					}
					if err := resolved.Validate(input); (err == nil) != tc.valid {
						t.Fatalf("schema validation=%v, want valid=%v", err, tc.valid)
					}
					var request setTestExecutionPolicyInput
					err := json.Unmarshal(raw, &request)
					if err == nil {
						_, err = dto.FromTestExecutionPolicyDTO(request.Policy)
					}
					if (err == nil) != tc.valid {
						t.Fatalf("backend validation=%v, want valid=%v", err, tc.valid)
					}
				})
			}
		})
	}
}
