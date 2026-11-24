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

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/trading"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/framework/web/httpapi/dto"
	"go.openpit.dev/officer/internal/store/sqlite"
	"go.openpit.dev/officer/mcptools"
)

const testPolicyConnection = "00000000-0000-4000-8000-000000000001"
const testPolicyPath = "/trading/connections/" + testPolicyConnection + "/test-policy"
const testPolicyExample = `{"rules":[{"mask":{"baseAsset":"A*","quoteAsset":"USD"},"mode":"split","priceVariant":"normal","fills":3},{"mask":{"baseAsset":"MSFT","quoteAsset":"USD"},"mode":"rest","priceVariant":"best","wait":{"kind":"finite","seconds":10}}]}`

type testPolicyRuntime struct {
	backend.TradingRuntime
	registry *trading.Registry
}

func (r testPolicyRuntime) Providers() []trading.Provider { return r.registry.Providers() }

func (r testPolicyRuntime) SupportsExecutionPolicy(provider string) (bool, error) {
	return r.registry.SupportsExecutionPolicy(provider)
}

func newTestPolicyService(t *testing.T) *backend.Service {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.New(filepath.Join(t.TempDir(), "policy.db"), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	registry := trading.NewRegistry()
	for _, provider := range []trading.Provider{
		{Type: "policy-fixture", ExecutionPolicy: true},
		{Type: "venue-fixture", ExecutionPolicy: false},
	} {
		provider.Build = func(trading.BuildContext) (trading.Connector, error) {
			return nil, errors.New("fixture connector must not be built")
		}
		if err := registry.Register(provider); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.BindExecutionPolicyProviders(registry.SupportsExecutionPolicy); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	n, _, err := node.NewLocalNode(ctx, domain.DefaultRealm, st, engine.NewOpenPitEngineBuildFunc(), func(err error) { t.Errorf("fatal: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []domain.TradingConnection{
		{ExternalID: testPolicyConnection, Provider: "policy-fixture", Label: "Policy fixture", VenueIdentity: "policy-fixture", Mode: domain.TradingModePaper, Credentials: "{}"},
		{ExternalID: "venue-connection", Provider: "venue-fixture", Label: "Venue fixture", VenueIdentity: "venue-fixture", Mode: domain.TradingModePaper, Credentials: "{}"},
	} {
		if _, err := realm.CreateTradingConnection(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := backend.New(n, nil, nil, func([]byte, domain.Order) (string, error) {
		return "", errors.New("settlement decoder must not be called")
	}, backend.WithTradingRuntime(testPolicyRuntime{registry: registry}))
	if err != nil {
		t.Fatal(err)
	}
	return svc
}
func callTestPolicy(t *testing.T, router http.Handler, prefix, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, prefix+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}
func requireTestPolicyJSON(t *testing.T, rec *httptest.ResponseRecorder, status int, want string) {
	t.Helper()
	var gotValue, wantValue any
	if rec.Code != status {
		t.Fatalf("status=%d body=%s want status=%d", rec.Code, rec.Body.String(), status)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("body=%s want %s", rec.Body.String(), want)
	}
}

type policyParitySource struct {
	frameworkmcp.Source
	enabled bool
}

func (s *policyParitySource) CommandEnabled(context.Context, string) (bool, error) {
	return s.enabled, nil
}

func policyParitySession(t *testing.T, source frameworkmcp.Source) *sdkmcp.ClientSession {
	t.Helper()
	reg := frameworkmcp.NewToolRegistry()
	mcptools.RegisterTools(reg, source)
	server, err := frameworkmcp.Build(reg, source, nil, httpx.AllowAll{}, domain.Caller{Principal: domain.PrincipalOperator})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, serverTransport) }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "policy-parity"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	})
	return session
}

func policyCanonicalJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestTestPolicyRESTMCPParityI10(t *testing.T) {
	ctx := t.Context()
	service := newTestPolicyService(t)
	router, err := newRouter(service)
	if err != nil {
		t.Fatal(err)
	}
	source := &policyParitySource{Source: frameworkmcp.SourceFor(service)}
	session := policyParitySession(t, source)
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Resolved{}
	for _, tool := range listed.Tools {
		if tool.Name != "get_test_execution_policy" && tool.Name != "set_test_execution_policy" {
			continue
		}
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		if len(schema.AnyOf) != 4 {
			t.Fatalf("%s output must admit state, problem, envelope and zero Out: %s", tool.Name, raw)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		schemas[tool.Name] = resolved
		raw, err = json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var input jsonschema.Schema
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		want := []string{"connection"}
		if tool.Name == "set_test_execution_policy" {
			want = append(want, "policy")
		}
		if !reflect.DeepEqual(input.Required, want) {
			t.Fatalf("%s required=%v want=%v", tool.Name, input.Required, want)
		}
	}
	if len(schemas) != 2 {
		t.Fatalf("policy schemas=%v", schemas)
	}
	call := func(t *testing.T, name, connection, policy string) *sdkmcp.CallToolResult {
		t.Helper()
		args := map[string]any{"connection": connection}
		if name == "set_test_execution_policy" {
			args["policy"] = json.RawMessage(policy)
		}
		result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if err := schemas[name].Validate(result.StructuredContent); err != nil {
			t.Fatalf("%s result violates explicit schema: %+v: %v", name, result, err)
		}
		return result
	}
	compare := func(t *testing.T, result *sdkmcp.CallToolResult, rest *httptest.ResponseRecorder) {
		t.Helper()
		var body any
		if err := json.Unmarshal(rest.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if result.IsError != (rest.Code >= 400) || !bytes.Equal(policyCanonicalJSON(t, result.StructuredContent), policyCanonicalJSON(t, body)) {
			t.Fatalf("MCP=%+v REST=%d %s", result, rest.Code, rest.Body)
		}
		if len(result.Content) != 1 {
			t.Fatalf("content=%+v", result.Content)
		}
		text, ok := result.Content[0].(*sdkmcp.TextContent)
		if !ok {
			t.Fatalf("content=%+v", result.Content)
		}
		if result.IsError {
			view := body.(map[string]any)
			message, ok := view["detail"].(string)
			if !ok {
				message = view["error"].(map[string]any)["message"].(string)
			}
			if text.Text != message {
				t.Fatalf("message=%q want=%q", text.Text, message)
			}
		} else if !strings.Contains(text.Text, "\n") {
			t.Fatalf("success lacks toolOK JSON: %q", text.Text)
		}
	}
	initial := callTestPolicy(t, router, "/api/v1", http.MethodGet, testPolicyPath, "")
	disabled := call(t, "set_test_execution_policy", testPolicyConnection, testPolicyExample)
	if !disabled.IsError {
		t.Fatal("disabled set succeeded")
	}
	requireTestPolicyJSON(t, callTestPolicy(t, router, "/api/v1", http.MethodGet, testPolicyPath, ""), 200, initial.Body.String())
	source.enabled = true
	compare(t, call(t, "get_test_execution_policy", testPolicyConnection, ""), initial)
	put := callTestPolicy(t, router, "/api/v1", http.MethodPut, testPolicyPath, testPolicyExample)
	compare(t, call(t, "get_test_execution_policy", testPolicyConnection, ""), put)
	set := call(t, "set_test_execution_policy", testPolicyConnection, testPolicyExample)
	compare(t, set, callTestPolicy(t, router, "/api/v1", http.MethodGet, testPolicyPath, ""))
	if !strings.HasPrefix(set.Content[0].(*sdkmcp.TextContent).Text, "Policy replaced\n") {
		t.Fatal("set success summary changed")
	}
	stable := callTestPolicy(t, router, "/api/v1", http.MethodGet, testPolicyPath, "")
	for _, tc := range []struct{ name, body, constraint, pointer string }{
		{"required", strings.Replace(testPolicyExample, `"A*"`, `""`, 1), "required", "/rules/0/mask/baseAsset"},
		{"format", strings.Replace(testPolicyExample, `"A*"`, `"A B"`, 1), "format", "/rules/0/mask/baseAsset"},
		{"enum second rule", `{"rules":[{"mask":{"baseAsset":"A*","quoteAsset":"USD"},"mode":"execute","priceVariant":"normal"},{"mask":{"baseAsset":"MSFT","quoteAsset":"USD"},"mode":"split","priceVariant":"normal","fills":1}]}`, "enum", "/rules/1/fills"},
		{"enum", strings.Replace(testPolicyExample, `"fills":3`, `"fills":4`, 1), "enum", "/rules/0/fills"},
		{"range", strings.Replace(testPolicyExample, `"seconds":10`, `"seconds":0`, 1), "range", "/rules/1/wait/seconds"},
		{"forbidden", strings.Replace(testPolicyExample, `"kind":"finite","seconds":10`, `"kind":"infinite"`, 1), "forbidden", "/rules/1/priceVariant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rest := callTestPolicy(t, router, "/api/v1", http.MethodPut, testPolicyPath, tc.body)
			compare(t, call(t, "set_test_execution_policy", testPolicyConnection, tc.body), rest)
			var problem httpx.ProblemDetails
			if err := json.Unmarshal(rest.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			if rest.Code != 422 || len(problem.Errors) != 1 || problem.Errors[0].Code != "validation" || problem.Errors[0].Constraint != tc.constraint || problem.Errors[0].Pointer != tc.pointer {
				t.Fatalf("problem=%+v", problem)
			}
			requireTestPolicyJSON(t, callTestPolicy(t, router, "/api/v1", http.MethodGet, testPolicyPath, ""), 200, stable.Body.String())
		})
	}
	for _, connection := range []string{"venue-connection", "missing"} {
		path := "/trading/connections/" + connection + "/test-policy"
		compare(t, call(t, "get_test_execution_policy", connection, ""), callTestPolicy(t, router, "/api/v1", http.MethodGet, path, ""))
		compare(t, call(t, "set_test_execution_policy", connection, testPolicyExample), callTestPolicy(t, router, "/api/v1", http.MethodPut, path, testPolicyExample))
	}
	for _, bad := range []any{
		map[string]any{},
		map[string]any{"error": map[string]any{"message": "missing code"}},
		map[string]any{"connection": "x", "revision": "1", "rules": nil},
		map[string]any{"error": map[string]any{"code": "internal", "message": "internal error", "Credentials": "secret"}},
	} {
		for name, schema := range schemas {
			if schema.Validate(bad) == nil {
				t.Fatalf("%s schema admitted invalid body: %+v", name, bad)
			}
		}
	}
}

func TestTestPolicyMCPStructuralErrorsAreProtocolErrors(t *testing.T) {
	source := &policyParitySource{Source: frameworkmcp.SourceFor(newTestPolicyService(t))}
	session := policyParitySession(t, source)
	for _, raw := range []string{
		`{"connection":"test","extra":true,"policy":{"rules":[]}}`,
		`{"connection":42,"policy":{"rules":[]}}`,
		`{"connection":"test","policy":{"rules":[{"foo":1}]}}`,
		`{"connection":"test","policy":{"rules":[{"wait":{"kind":"finite","seconds":1.5}}]}}`,
	} {
		result, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "set_test_execution_policy", Arguments: json.RawMessage(raw)})
		var protocol *jsonrpc.Error
		if result != nil || !errors.As(err, &protocol) || protocol.Code != -32602 || !strings.HasPrefix(protocol.Message, "unmarshaling: ") {
			t.Fatalf("args=%s result=%+v err=%v", raw, result, err)
		}
	}
}

func TestTestPolicyStrictJSONFields(t *testing.T) {
	cases := []struct{ name, body, field, pointer string }{
		{"rules case", `{"Rules":[]}`, "Rules", "/Rules"},
		{"rules alias beside canonical", `{"rules":[],"Rules":null}`, "Rules", "/Rules"},
		{"unknown policy field", `{"rules":[],"extra":true}`, "extra", "/extra"},
		{"mode case", strings.Replace(testPolicyExample, `"mode":"split"`, `"Mode":"split"`, 1), "Mode", "/rules/0/Mode"},
		{"mask case", strings.Replace(testPolicyExample, `"mask":`, `"Mask":`, 1), "Mask", "/rules/0/Mask"},
		{"asset case", strings.Replace(testPolicyExample, `"baseAsset":`, `"BaseAsset":`, 1), "BaseAsset", "/rules/0/mask/BaseAsset"},
		{"unknown rule field", strings.Replace(testPolicyExample, `"fills":3`, `"fills":3,"extra":true`, 1), "extra", "/rules/0/extra"},
		{"unknown mask field", strings.Replace(testPolicyExample, `"baseAsset":"A*"`, `"baseAsset":"A*","extra":true`, 1), "extra", "/rules/0/mask/extra"},
		{"unknown wait field", strings.Replace(testPolicyExample, `"seconds":10`, `"seconds":10,"extra":true`, 1), "extra", "/rules/1/wait/extra"},
		{"null price alias", strings.Replace(testPolicyExample, `"priceVariant":"normal"`, `"PriceVariant":null`, 1), "PriceVariant", "/rules/0/PriceVariant"},
		{"null fills alias", strings.Replace(testPolicyExample, `"fills":3`, `"Fills":null`, 1), "Fills", "/rules/0/Fills"},
		{"null wait alias", strings.Replace(testPolicyExample, `"wait":{"kind":"finite","seconds":10}`, `"Wait":null`, 1), "Wait", "/rules/1/Wait"},
		{"null seconds alias", strings.Replace(testPolicyExample, `"seconds":10`, `"Seconds":null`, 1), "Seconds", "/rules/1/wait/Seconds"},
		{"price alias overwrites null", strings.Replace(testPolicyExample, `"priceVariant":"normal"`, `"priceVariant":null,"PriceVariant":"normal"`, 1), "PriceVariant", "/rules/0/PriceVariant"},
		{"fills alias overwrites null", strings.Replace(testPolicyExample, `"fills":3`, `"fills":null,"Fills":3`, 1), "Fills", "/rules/0/Fills"},
		{"wait alias overwrites null", strings.Replace(testPolicyExample, `"wait":`, `"wait":null,"Wait":`, 1), "Wait", "/rules/1/Wait"},
		{"seconds alias overwrites null", strings.Replace(testPolicyExample, `"seconds":10`, `"seconds":null,"Seconds":10`, 1), "Seconds", "/rules/1/wait/Seconds"},
		{"lowercase empty rules", `{"rules":[]}`, "", ""},
	}
	for _, transport := range []string{"REST", "MCP"} {
		t.Run(transport, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					service := newTestPolicyService(t)
					router, err := newRouter(service)
					if err != nil {
						t.Fatal(err)
					}
					seed := callTestPolicy(t, router, "/api/v1", http.MethodPut, testPolicyPath, testPolicyExample)
					if seed.Code != http.StatusOK {
						t.Fatalf("seed=%s", seed.Body.String())
					}
					if transport == "REST" {
						rest := callTestPolicy(t, router, "/api/v1", http.MethodPut, testPolicyPath, tc.body)
						if tc.field == "" {
							if rest.Code != http.StatusOK {
								t.Fatalf("valid empty policy: status=%d body=%s", rest.Code, rest.Body.String())
							}
						} else {
							var problem httpx.ProblemDetails
							if err := json.Unmarshal(rest.Body.Bytes(), &problem); err != nil {
								t.Fatal(err)
							}
							if rest.Code != 422 || problem.Status != 422 || len(problem.Errors) != 1 || problem.Errors[0].Code != "validation" || problem.Errors[0].Constraint != "unknown_field" || problem.Errors[0].Pointer != tc.pointer {
								t.Errorf("status=%d problem=%+v body=%s", rest.Code, problem, rest.Body.String())
							}
						}
					} else {
						source := &policyParitySource{Source: frameworkmcp.SourceFor(service), enabled: true}
						session := policyParitySession(t, source)
						result, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
							Name:      "set_test_execution_policy",
							Arguments: map[string]any{"connection": testPolicyConnection, "policy": json.RawMessage(tc.body)},
						})
						if tc.field == "" {
							if err != nil || result == nil || result.IsError {
								t.Fatalf("valid empty policy: result=%+v err=%v", result, err)
							}
						} else {
							var protocol *jsonrpc.Error
							if result != nil || !errors.As(err, &protocol) || protocol.Code != -32602 || !strings.Contains(protocol.Message, `json: unknown field "`+tc.field+`"`) {
								t.Errorf("result=%+v err=%v", result, err)
							}
						}
					}
					after := callTestPolicy(t, router, "/api/v1", http.MethodGet, testPolicyPath, "")
					if tc.field != "" {
						requireTestPolicyJSON(t, after, http.StatusOK, seed.Body.String())
						return
					}
					var beforeState, afterState dto.TestExecutionPolicyStateDTO
					if err := json.Unmarshal(seed.Body.Bytes(), &beforeState); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(after.Body.Bytes(), &afterState); err != nil {
						t.Fatal(err)
					}
					if after.Code != http.StatusOK || afterState.Revision == beforeState.Revision || afterState.Rules == nil || len(afterState.Rules) != 0 {
						t.Fatalf("valid empty policy did not replace state: before=%s after=%s", seed.Body.String(), after.Body.String())
					}
				})
			}
		})
	}
}

func TestTestPolicyRESTBothPrefixes(t *testing.T) {
	for _, prefix := range []string{"/api/v1", "/app/api/v1"} {
		t.Run(prefix, func(t *testing.T) {
			router, err := newRouter(newTestPolicyService(t))
			if err != nil {
				t.Fatal(err)
			}
			rec := callTestPolicy(t, router, prefix, http.MethodGet, testPolicyPath, "")
			requireTestPolicyJSON(t, rec, 200, `{"connection":"`+testPolicyConnection+`","revision":"1","rules":[]}`)
			padded := strings.Replace(testPolicyPath, testPolicyConnection, "%20"+testPolicyConnection+"%20", 1)
			rec = callTestPolicy(t, router, prefix, http.MethodGet, padded, "")
			requireTestPolicyJSON(t, rec, 200, `{"connection":"`+testPolicyConnection+`","revision":"1","rules":[]}`)
			rec = callTestPolicy(t, router, prefix, http.MethodPut, testPolicyPath, testPolicyExample)
			want := `{"connection":"` + testPolicyConnection + `","revision":"2",` + testPolicyExample[1:]
			requireTestPolicyJSON(t, rec, 200, want)
			rec = callTestPolicy(t, router, prefix, http.MethodGet, testPolicyPath, "")
			requireTestPolicyJSON(t, rec, 200, want)
			rec = callTestPolicy(t, router, prefix, http.MethodPut, testPolicyPath, testPolicyExample)
			requireTestPolicyJSON(t, rec, 200, strings.Replace(want, `"revision":"2"`, `"revision":"3"`, 1))
			for _, valid := range []struct{ revision, rule string }{
				{"4", `"mode":"rest","wait":{"kind":"infinite"}`},
				{"5", `"mode":"execute","priceVariant":"normal"`},
				{"6", `"mode":"reject_even","priceVariant":"best"`},
			} {
				body := `{"rules":[{"mask":{"baseAsset":"*","quoteAsset":"*"},` + valid.rule + `}]}`
				rec = callTestPolicy(t, router, prefix, http.MethodPut, testPolicyPath, body)
				requireTestPolicyJSON(t, rec, 200, `{"connection":"`+testPolicyConnection+`","revision":"`+valid.revision+`",`+body[1:])
			}
		})
	}
}
func TestTestPolicyRESTValidationLeavesRevision(t *testing.T) {
	cases := []struct {
		name, body, code, constraint, pointer string
		status                                int
	}{
		{"empty base", strings.Replace(testPolicyExample, `"A*"`, `""`, 1), "validation", "required", "/rules/0/mask/baseAsset", 422},
		{"fills enum", strings.Replace(testPolicyExample, `"fills":3`, `"fills":4`, 1), "validation", "enum", "/rules/0/fills", 422},
		{"unknown rule member", strings.Replace(testPolicyExample, `"fills":3`, `"fills":3,"foo":1`, 1), "validation", "unknown_field", "/rules/0/foo", 422},
		{"fractional seconds", strings.Replace(testPolicyExample, `"seconds":10`, `"seconds":1.5`, 1), "validation", "type", "/rules/1/wait/seconds", 422},
		{"malformed JSON", `{"rules":[`, "bad_request", "malformed_json", "", 400},
		{"request revision", `{"revision":"7","rules":[]}`, "validation", "unknown_field", "/revision", 422},
		{"null variant on infinite rest", `{"rules":[{"mask":{"baseAsset":"*","quoteAsset":"*"},"mode":"rest","wait":{"kind":"infinite"},"priceVariant":null}]}`, "validation", "type", "/rules/0/priceVariant", 422},
		{"null fills on execute", `{"rules":[{"mask":{"baseAsset":"*","quoteAsset":"*"},"mode":"execute","priceVariant":"normal","fills":null}]}`, "validation", "type", "/rules/0/fills", 422},
		{"null wait on execute", `{"rules":[{"mask":{"baseAsset":"*","quoteAsset":"*"},"mode":"execute","priceVariant":"normal","wait":null}]}`, "validation", "type", "/rules/0/wait", 422},
		{"null seconds on infinite rest", `{"rules":[{"mask":{"baseAsset":"*","quoteAsset":"*"},"mode":"rest","wait":{"kind":"infinite","seconds":null}}]}`, "validation", "type", "/rules/0/wait/seconds", 422},
		{"null fills on split", strings.Replace(testPolicyExample, `"fills":3`, `"fills":null`, 1), "validation", "type", "/rules/0/fills", 422},
		{"null variant on split", strings.Replace(testPolicyExample, `"priceVariant":"normal"`, `"priceVariant":null`, 1), "validation", "type", "/rules/0/priceVariant", 422},
		{"null wait on finite rest", strings.Replace(testPolicyExample, `"wait":{"kind":"finite","seconds":10}`, `"wait":null`, 1), "validation", "type", "/rules/1/wait", 422},
		{"null seconds on finite rest", strings.Replace(testPolicyExample, `"seconds":10`, `"seconds":null`, 1), "validation", "type", "/rules/1/wait/seconds", 422},
		{"null fills on reject even", `{"rules":[{"mask":{"baseAsset":"*","quoteAsset":"*"},"mode":"reject_even","priceVariant":"normal","fills":null}]}`, "validation", "type", "/rules/0/fills", 422},
		{"null fills on rest", `{"rules":[{"mask":{"baseAsset":"*","quoteAsset":"*"},"mode":"rest","wait":{"kind":"infinite"},"fills":null}]}`, "validation", "type", "/rules/0/fills", 422},
		{"null wait on reject even", `{"rules":[{"mask":{"baseAsset":"*","quoteAsset":"*"},"mode":"reject_even","priceVariant":"normal","wait":null}]}`, "validation", "type", "/rules/0/wait", 422},
		{"null wait on split", strings.Replace(testPolicyExample, `"fills":3`, `"fills":3,"wait":null`, 1), "validation", "type", "/rules/0/wait", 422},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router, err := newRouter(newTestPolicyService(t))
			if err != nil {
				t.Fatal(err)
			}
			seed := callTestPolicy(t, router, "/api/v1", http.MethodPut, testPolicyPath, testPolicyExample)
			if seed.Code != 200 {
				t.Fatalf("seed=%s", seed.Body.String())
			}
			rec := callTestPolicy(t, router, "/api/v1", http.MethodPut, testPolicyPath, tc.body)
			if rec.Code != tc.status || rec.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("status=%d body=%s content-type=%s", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
			}
			var problem struct {
				Status int                                          `json:"status"`
				Errors []struct{ Code, Constraint, Pointer string } `json:"errors"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			if problem.Status != tc.status || len(problem.Errors) != 1 {
				t.Fatalf("problem=%+v", problem)
			}
			item := problem.Errors[0]
			if item.Code != tc.code || item.Constraint != tc.constraint || item.Pointer != tc.pointer {
				t.Fatalf("error=%+v want code=%s constraint=%s pointer=%s", item, tc.code, tc.constraint, tc.pointer)
			}
			after := callTestPolicy(t, router, "/api/v1", http.MethodGet, testPolicyPath, "")
			requireTestPolicyJSON(t, after, 200, seed.Body.String())
		})
	}
}
func TestTestPolicyRESTRefusals(t *testing.T) {
	router, err := newRouter(newTestPolicyService(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		body := ""
		if method == http.MethodPut {
			body = testPolicyExample
		}
		rec := callTestPolicy(t, router, "/api/v1", method, "/trading/connections/venue-connection/test-policy", body)
		requireTestPolicyJSON(t, rec, 409, `{"error":{"code":"execution_policy_unsupported","message":"execution policy is not supported by this trading provider"}}`)
		rec = callTestPolicy(t, router, "/api/v1", method, "/trading/connections/missing/test-policy", body)
		if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
			t.Fatalf("unknown connection=%d %s", rec.Code, rec.Body.String())
		}
	}
}
func TestTestPolicyRESTCommandIDs(t *testing.T) {
	routes := NewRouteRegistry(&fakeService{}, nil, noSealReport).Routes()
	want := map[string]string{"trading.test-policy.get": http.MethodGet, "trading.test-policy.put": http.MethodPut}
	for _, route := range routes {
		if method, ok := want[route.ID]; ok {
			if route.Method != method || route.Pattern != "/trading/connections/{id}/test-policy" || route.Public || route.DuringSetup {
				t.Fatalf("route=%+v", route)
			}
			delete(want, route.ID)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing commands=%v", want)
	}
}

func TestTestPolicyOpenAPI(t *testing.T) {
	spec := string(openapiSpec)
	for _, schema := range []struct {
		name  string
		value any
	}{
		{"InstrumentMask", dto.InstrumentMaskDTO{}},
		{"TestExecutionWait", dto.TestExecutionWaitDTO{}},
		{"TestExecutionRule", dto.TestExecutionRuleDTO{}},
		{"TestExecutionPolicy", dto.TestExecutionPolicyDTO{}},
		{"TestExecutionPolicyState", dto.TestExecutionPolicyStateDTO{}},
	} {
		wantRequired, wantProperties := jsonMembers(reflect.TypeOf(schema.value))
		gotRequired, gotProperties := openAPISchemaMembers(t, spec, schema.name)
		if !reflect.DeepEqual(gotRequired, wantRequired) || !reflect.DeepEqual(gotProperties, wantProperties) {
			t.Fatalf("schema %s required=%v properties=%v want required=%v properties=%v", schema.name, gotRequired, gotProperties, wantRequired, wantProperties)
		}
	}
}

type testPolicyAuthorizer struct{ commands []string }

func (a *testPolicyAuthorizer) Authorize(_ context.Context, _ domain.Caller, command string) error {
	a.commands = append(a.commands, command)
	return domain.ErrForbidden
}
func TestTestPolicyRESTAuthorizerPrecedesLookup(t *testing.T) {
	for _, prefix := range []string{"/api/v1", "/app/api/v1"} {
		authorizer := &testPolicyAuthorizer{}
		guards, err := testGuards()
		if err != nil {
			t.Fatal(err)
		}
		router, err := httpx.NewRouter(httpx.RouterConfig{
			Surfaces:   httpx.V1Surfaces(NewRouteRegistry(&fakeService{}, nil, noSealReport), nil),
			Authorizer: authorizer, CallerResolver: testCallerResolver, BodyLimit: BodyLimitPolicy(),
			Guards: guards, AccessMode: func() httpx.AccessMode { return httpx.AccessModeOpen },
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			rec := callTestPolicy(t, router, prefix, method, "/trading/connections/missing/test-policy", testPolicyExample)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("authorizer status=%d body=%s", rec.Code, rec.Body.String())
			}
		}
		if !reflect.DeepEqual(authorizer.commands, []string{"trading.test-policy.get", "trading.test-policy.put"}) {
			t.Fatalf("authorized commands=%v", authorizer.commands)
		}
	}
}
