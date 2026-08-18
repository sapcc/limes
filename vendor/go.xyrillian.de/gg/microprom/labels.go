// SPDX-FileCopyrightText: 2026 Stefan Majewsky <majewsky@gmx.net>
// SPDX-License-Identifier: Apache-2.0

package microprom

import (
	"fmt"
	"strings"
)

// Labels holds a label set, formatted according to the text protocol of [OpenMetrics 1.0].
// Instances are constructed through [LabelNames.Format].
//
// [OpenMetrics 1.0]: https://prometheus.io/docs/specs/om/open_metrics_spec/
type Labels string

// LabelNames holds a set of label names.
type LabelNames struct {
	// NOTE: This is an opaque struct because, when adding support for OpenMetrics 2.0,
	// it will be useful to precompute escaped forms for these names where necessary.
	//
	// Another interesting addition might be alphabetical sorting of labels, where we
	// would need to remember the sort order because we need to apply it to the values
	// slice during Format().
	names []string
}

// NewLabelNames constructs a LabelNames instance.
//
// Per the [OpenMetrics 1.0] spec, label names must match the following regular expression:
//
//	[a-zA-Z_][a-zA-Z0-9_]*
//
// [OpenMetrics 1.0]: https://prometheus.io/docs/specs/om/open_metrics_spec/
func NewLabelNames(names ...string) LabelNames {
	for _, name := range names {
		if !labelNameRx.MatchString(name) {
			panic(fmt.Sprintf("invalid label name: %q", name))
		}
	}
	return LabelNames{names}
}

// FormatLabels serializes a Prometheus labelset into the string format used in Prometheus text expositions.
// For example:
//
//	// once, e.g. during func init()
//	var names = microprom.NewLabelNames("foo", "hello")
//
//	// during microprom.Handler.Collect()
//	labels := ms.FormatLabels(names, "bar", "world")
//	assert.Equal(t, labels, `foo="bar",hello="world"`)
func (ms *MetricSet) FormatLabels(n LabelNames, values ...string) Labels {
	// NOTE on API structure: This is not part of ms.Add() to allow reusing label sets for multiple metrics.

	if len(n.names) != len(values) {
		panic(fmt.Sprintf("expected %d label values, but got %d", len(n.names), len(values)))
	}
	if len(n.names) == 0 {
		return ""
	}

	// estimate the perfect number of bytes for the result string to avoid reallocations
	capacity := len(n.names) - 1 // number of "," between pairs
	needsEscaping := make([]bool, len(n.names))
	for idx, value := range values {
		// base length for an encoding in the form `label="value"`
		capacity += len(n.names[idx]) + len(value) + 3
		// some characters within `value` need escaping (TODO: this could be optimized to only iterate through `value` once)
		toEscape := strings.Count(value, "\n") + strings.Count(value, "\"") + strings.Count(value, "\\")
		needsEscaping[idx] = toEscape > 0
		capacity += toEscape
	}

	var b strings.Builder
	b.Grow(capacity)
	for idx, value := range values {
		if idx > 0 {
			_ = b.WriteByte(',')
		}
		_, _ = b.WriteString(n.names[idx])
		_ = b.WriteByte('=')
		_ = b.WriteByte('"')
		if needsEscaping[idx] {
			// TODO: this could be optimized, but since this branch is unlikely in practice, I did not bother yet
			value = strings.ReplaceAll(value, "\\", "\\\\")
			value = strings.ReplaceAll(value, "\"", "\\\"")
			value = strings.ReplaceAll(value, "\n", "\\n")
		}
		_, _ = b.WriteString(value)
		_ = b.WriteByte('"')
	}
	return Labels(b.String())
}
