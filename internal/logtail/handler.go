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

package logtail

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Handler is a slog.Handler that tees each record: it formats the record into a
// self-contained line, appends it to the ring buffer, and delegates to an inner
// handler so the configured output (level, format) is unchanged. The same
// buffer pointer is shared across the WithAttrs/WithGroup chain.
type Handler struct {
	inner  slog.Handler
	buf    *Buffer
	groups []string
	attrs  []slog.Attr
}

// NewHandler returns a Handler that appends a formatted copy of every record to
// buf and delegates to inner. inner owns the real output stream; buf is the
// in-memory tail read back over the HTTP surface.
func NewHandler(inner slog.Handler, buf *Buffer) slog.Handler {
	return &Handler{inner: inner, buf: buf}
}

// Enabled delegates to the inner handler so the tee honours the same level.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle formats the record into the buffer and then delegates to the inner
// handler. The buffer append always runs, independent of the inner handler's
// own filtering, so the tail reflects what the logger emitted.
func (h *Handler) Handle(ctx context.Context, record slog.Record) error {
	h.buf.Append(h.format(record))
	return h.inner.Handle(ctx, record)
}

// WithAttrs returns a tee wrapping inner.WithAttrs and sharing the same buffer.
// The accumulated attrs (qualified by the current group path) are carried so a
// formatted line includes the handler-bound state.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	clone := h.clone()
	clone.inner = h.inner.WithAttrs(attrs)
	prefix := strings.Join(h.groups, ".")
	for _, a := range attrs {
		clone.attrs = append(clone.attrs, qualifyAttr(prefix, a))
	}
	return clone
}

// WithGroup returns a tee wrapping inner.WithGroup and sharing the same buffer.
// The group name is pushed so subsequent attrs render dotted (group.key=value).
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := h.clone()
	clone.inner = h.inner.WithGroup(name)
	clone.groups = append(append([]string{}, h.groups...), name)
	return clone
}

// clone makes a shallow copy with independent slices so a derived handler never
// mutates the parent's accumulated state. The buffer pointer is shared.
func (h *Handler) clone() *Handler {
	return &Handler{
		inner:  h.inner,
		buf:    h.buf,
		groups: append([]string{}, h.groups...),
		attrs:  append([]slog.Attr{}, h.attrs...),
	}
}

// format renders a record as a self-contained line:
// "RFC3339-time  LEVEL  message  key=value ...". Handler-bound attrs (from
// WithAttrs, qualified by any WithGroup path) come first, then the record's own
// attrs (qualified by the current group path).
func (h *Handler) format(record slog.Record) string {
	var b strings.Builder
	b.WriteString(record.Time.Format(time.RFC3339))
	b.WriteString("  ")
	b.WriteString(record.Level.String())
	b.WriteString("  ")
	b.WriteString(record.Message)

	for _, a := range h.attrs {
		b.WriteString("  ")
		b.WriteString(formatAttr(a))
	}

	prefix := strings.Join(h.groups, ".")
	record.Attrs(func(a slog.Attr) bool {
		b.WriteString("  ")
		b.WriteString(formatAttr(qualifyAttr(prefix, a)))
		return true
	})

	return b.String()
}

// qualifyAttr prefixes an attr key with the dotted group path, if any.
func qualifyAttr(prefix string, a slog.Attr) slog.Attr {
	if prefix == "" {
		return a
	}
	return slog.Attr{Key: prefix + "." + a.Key, Value: a.Value}
}

// formatAttr renders one attr as key=value, resolving any LogValuer and using
// the attr value's own string form.
func formatAttr(a slog.Attr) string {
	return a.Key + "=" + fmt.Sprintf("%v", a.Value.Resolve().Any())
}
