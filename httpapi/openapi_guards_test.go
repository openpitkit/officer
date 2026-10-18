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
	"fmt"
	"strings"
	"testing"
)

const (
	unsupportedMediaTypeRef = "#/components/responses/UnsupportedMediaTypeError"
	foreignHostResponse     = "        \"403\":\n          $ref: \"#/components/responses/ForeignHostError\"\n"
	mediaTypeResponse       = "        \"415\":\n          $ref: \"" + unsupportedMediaTypeRef + "\"\n"
)

// TestOpenAPIBrowserGuardsContract holds the browser-guard refusals every
// operation documents: the Host guard answers every operation, the Origin and
// JSON-body guards answer the unsafe ones only.
func TestOpenAPIBrowserGuardsContract(t *testing.T) {
	safe, unsafe, problems := browserGuardProblems(string(openapiSpec))
	if len(problems) > 0 {
		t.Fatalf("OpenAPI browser guard declarations, %d safe and %d unsafe operations checked:\n%s",
			safe, unsafe, strings.Join(problems, "\n"))
	}
	t.Logf("checked %d safe and %d unsafe operations", safe, unsafe)
}

// TestOpenAPIBrowserGuardsContractBites feeds the same check specs with one
// declaration removed or added, and requires each to be refused.
func TestOpenAPIBrowserGuardsContractBites(t *testing.T) {
	spec := string(openapiSpec)
	const (
		getStatus  = "\n  /status:\n    get:\n"
		postExport = "\n  /backup/export:\n    post:\n"
	)
	for _, tc := range []struct {
		name string
		spec string
		want string
	}{
		{"empty spec", "", "no operations found"},
		{"GET without 403", editOperation(t, spec, getStatus, foreignHostResponse, ""),
			"GET /status: no 403 response"},
		{"POST without 415", editOperation(t, spec, postExport, mediaTypeResponse, ""),
			"POST /backup/export: no 415 response"},
		{"GET with 415", editOperation(t, spec, getStatus, foreignHostResponse, foreignHostResponse+mediaTypeResponse),
			"GET /status: documents 415"},
		{"403 to a missing component", editOperation(t, spec, getStatus, "ForeignHostError", "ForeignHostErrors"),
			"GET /status: 403 references missing components/responses/ForeignHostErrors"},
		{"no components/responses", strings.Replace(spec, "\n  responses:\n", "\n  missing:\n", 1),
			"no components/responses section"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, problems := browserGuardProblems(tc.spec)
			for _, problem := range problems {
				if strings.Contains(problem, tc.want) {
					return
				}
			}
			t.Fatalf("problems %q do not contain %q", problems, tc.want)
		})
	}
}

// editOperation replaces the first occurrence of old after header with
// replacement, requiring that occurrence to lie inside the operation's path.
func editOperation(t *testing.T, spec, header, old, replacement string) string {
	t.Helper()
	start := strings.Index(spec, header)
	if start < 0 {
		t.Fatalf("operation header %q is missing from the spec", header)
	}
	start += len(header)
	end := len(spec)
	if next := strings.Index(spec[start:], "\n  /"); next >= 0 {
		end = start + next
	}
	at := strings.Index(spec[start:end], old)
	if at < 0 {
		t.Fatalf("%q is missing from the operation after %q", old, header)
	}
	at += start
	return spec[:at] + replacement + spec[at+len(old):]
}

// browserGuardProblems checks the operations openAPIOperations reads and
// returns how many safe and unsafe operations it found with one message per
// missing or forbidden declaration. A safe method is one the Origin and
// JSON-body guards let through unchecked: GET, HEAD and OPTIONS; the Host guard
// checks every method. The operationId lines of the paths section are counted
// apart from the walker, so an operation it misses is reported.
func browserGuardProblems(spec string) (safe, unsafe int, problems []string) {
	lines := strings.Split(spec, "\n")
	components, found := openAPIResponseComponents(lines)
	if !found {
		problems = append(problems, "no components/responses section")
	}
	if !strings.Contains(components["UnsupportedMediaTypeError"], "unsupported_media_type") {
		problems = append(problems, "components/responses/UnsupportedMediaTypeError does not name unsupported_media_type")
	}
	for _, operation := range openAPIOperations(spec) {
		isSafe := operation.Method == "GET" || operation.Method == "HEAD" || operation.Method == "OPTIONS"
		if isSafe {
			safe++
		} else {
			unsafe++
		}
		problems = append(problems,
			operationGuardProblems(operation.Method+" "+operation.Path, isSafe, operation.Lines, components)...)
	}
	operationIDs := 0
	for _, line := range lines {
		if line == "components:" {
			break
		}
		if strings.HasPrefix(line, "      operationId:") {
			operationIDs++
		}
	}
	switch {
	case safe+unsafe == 0:
		problems = append(problems, "no operations found")
	case safe+unsafe != operationIDs:
		problems = append(problems, fmt.Sprintf("%d safe and %d unsafe operations found, but %d operationId lines",
			safe, unsafe, operationIDs))
	}
	return safe, unsafe, problems
}

// operationGuardProblems checks the 403 and 415 responses of one operation,
// given the lines nested under its method key.
func operationGuardProblems(name string, safe bool, operation []string, components map[string]string) []string {
	var responses map[string]string
	for i, line := range operation {
		if line == "      responses:" {
			responses = childBlocks(nestedLines(operation, i), 8)
		}
	}
	var problems []string
	forbidden, hasForbidden := responses["403"]
	forbidden, missing := resolveResponse(forbidden, components)
	if !hasForbidden {
		problems = append(problems, name+": no 403 response")
	} else if missing != "" {
		return append(problems, name+": 403 references missing components/responses/"+missing)
	} else if !strings.Contains(forbidden, "host_not_allowed") {
		problems = append(problems, name+": 403 does not name host_not_allowed")
	}
	unsupported, hasUnsupported := responses["415"]
	if safe {
		if hasUnsupported {
			problems = append(problems, name+": documents 415, which only an unsafe operation answers")
		}
		return problems
	}
	if hasForbidden && !strings.Contains(forbidden, "origin_not_allowed") {
		problems = append(problems, name+": 403 does not name origin_not_allowed")
	}
	if !hasUnsupported {
		problems = append(problems, name+": no 415 response")
	} else if !strings.Contains(unsupported, unsupportedMediaTypeRef) {
		problems = append(problems, name+": 415 does not reference UnsupportedMediaTypeError")
	}
	return problems
}

// resolveResponse returns the text of the components/responses entry the block
// references, or the block itself when it is an inline response. When the
// referenced entry does not exist it returns its name as missing.
func resolveResponse(block string, components map[string]string) (text, missing string) {
	for _, line := range strings.Split(block, "\n") {
		if ref, ok := strings.CutPrefix(strings.TrimSpace(line), `$ref: "#/components/responses/`); ok {
			name := strings.TrimSuffix(ref, `"`)
			text, found := components[name]
			if !found {
				return "", name
			}
			return text, ""
		}
	}
	return block, ""
}

// openAPIResponseComponents maps each components/responses name to its text,
// and reports whether the section was found.
func openAPIResponseComponents(lines []string) (map[string]string, bool) {
	inComponents := false
	for i, line := range lines {
		switch {
		case line == "components:":
			inComponents = true
		case inComponents && line == "  responses:":
			return childBlocks(nestedLines(lines, i), 4), true
		}
	}
	return nil, false
}

// openAPIOperation is one method key under a path of the spec's paths section.
type openAPIOperation struct {
	Method string   // upper-cased
	Path   string   // the path key, without its colon
	Lines  []string // the lines nested under the method key
}

// openAPIOperations reads the operations under paths line by line, over the
// spec's fixed indentation: the section ends at components:, a path is a line
// starting with two spaces and /, and an operation is an HTTP method key at
// four spaces.
func openAPIOperations(spec string) []openAPIOperation {
	lines := strings.Split(spec, "\n")
	var operations []openAPIOperation
	path := ""
	for i, line := range lines {
		if line == "components:" {
			break
		}
		key := strings.TrimSuffix(strings.TrimSpace(line), ":")
		switch {
		case strings.HasPrefix(line, "  /"):
			path = key
		case indentOf(line) == 4 && isHTTPMethod(key):
			operations = append(operations, openAPIOperation{
				Method: strings.ToUpper(key), Path: path, Lines: nestedLines(lines, i),
			})
		}
	}
	return operations
}

func isHTTPMethod(key string) bool {
	switch key {
	case "get", "put", "post", "delete", "options", "head", "patch", "trace":
		return true
	}
	return false
}

func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// nestedLines returns the lines nested under lines[i]: the following lines up
// to the first non-blank one that is not indented deeper than lines[i].
func nestedLines(lines []string, i int) []string {
	end := i + 1
	for end < len(lines) && (strings.TrimSpace(lines[end]) == "" || indentOf(lines[end]) > indentOf(lines[i])) {
		end++
	}
	return lines[i+1 : end]
}

// childBlocks maps the key of each line at exactly the given indent to the
// text of that line and the deeper lines under it.
func childBlocks(lines []string, indent int) map[string]string {
	blocks := map[string]string{}
	key := ""
	for _, line := range lines {
		switch {
		case strings.TrimSpace(line) == "":
		case indentOf(line) == indent:
			key = strings.Trim(strings.TrimSuffix(strings.TrimSpace(line), ":"), `"`)
			blocks[key] = line
		case key != "":
			blocks[key] += "\n" + line
		}
	}
	return blocks
}
