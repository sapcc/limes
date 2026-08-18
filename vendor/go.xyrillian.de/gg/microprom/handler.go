// SPDX-FileCopyrightText: 2026 Stefan Majewsky <majewsky@gmx.net>
// SPDX-License-Identifier: Apache-2.0

package microprom

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"

	"go.xyrillian.de/gg/internal/accept"
)

// Handler is an [http.Handler] rendering metrics in Prometheus exposition formats.
//
// If SortOutput is false:
//   - Metric families will be printed in undefined order.
//   - Metrics within the same family will be printed in the order in which they were added.
//   - This behavior is the default because it is more efficient.
//
// If SortOutput is true:
//   - Metric families will be sorted by name.
//   - Metrics within the same family will be sorted by Labels.
//   - This behavior may be useful in tests because it produces deterministic output.
//
// When asserting on metrics in tests, it may be useful to set SortOutput equal to testing.Testing().
type Handler struct {
	// The set of metric families for which this handler can report metrics.
	Families map[MetricFamilyName]MetricFamilyInfo
	// This function will be called for each request to the handler.
	// The implementation shall provide metrics by calling [MetricSet.Add].
	Collect func(context.Context, *MetricSet) error

	// See documentation on type for details.
	SortOutput bool
}

var _ http.Handler = Handler{}

// ServeHTTP implements the [http.Handler] interface.
func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	acceptedFormat, ok := accept.ParseHeader(r.Header["Accept"]).Negotiate(
		// The way that Prometheus handles `Accept` is insane.
		// They put a billion parameters in there, with `escaping=` possibly depending on server configuration.
		// (I have not read enough of the Prometheus source code to be sure.)
		// The easiest way for us is to negotiate for all possible combinations.
		//
		// Note that it is fine for the client to request less specific formats, e.g. just "application/openmetrics-text",
		// in which case the first match will be used.
		// Each set of similar choices has `escaping=underscores` on top each time because that's the default escaping scheme in promhttp.
		"text/plain; version=0.0.4; charset=utf-8; escaping=underscores",
		"text/plain; version=0.0.4; charset=utf-8; escaping=allow-utf-8",
		"text/plain; version=0.0.4; charset=utf-8; escaping=dots",
		"text/plain; version=0.0.4; charset=utf-8; escaping=values",
		"application/openmetrics-text; version=1.0.0; charset=utf-8; escaping=underscores",
		"application/openmetrics-text; version=1.0.0; charset=utf-8; escaping=allow-utf-8",
		"application/openmetrics-text; version=1.0.0; charset=utf-8; escaping=dots",
		"application/openmetrics-text; version=1.0.0; charset=utf-8; escaping=values",
	).Unpack()
	if !ok {
		http.Error(w, "supported formats are text/plain and application/openmetrics-text", http.StatusNotAcceptable)
		return
	}

	w.Header().Set("Content-Type", acceptedFormat)
	syntax := SyntaxPrometheusLegacy
	if strings.HasPrefix(acceptedFormat, "application/openmetrics-text; version=1.0.0;") {
		syntax = SyntaxOpenMetricsV1
	}

	ms := NewMetricSet(syntax, h.Families)
	err := h.Collect(r.Context(), ms)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	bw := bufio.NewWriter(w)
	if h.SortOutput {
		for _, familyName := range slices.Sorted(maps.Keys(h.Families)) {
			h.printMetricFamily(bw, syntax, familyName, h.Families[familyName], ms.metrics[familyName])
		}
	} else {
		for familyName, familyInfo := range h.Families {
			h.printMetricFamily(bw, syntax, familyName, familyInfo, ms.metrics[familyName])
		}
	}

	if syntax != SyntaxPrometheusLegacy {
		fmt.Fprint(bw, "# EOF\n")
	}
	err = bw.Flush()
	if err != nil {
		// We do not have a way to log this because we do not know what log library the application uses,
		// and I also do not want to add a dependency injection slot to type Handler for this one extremely unlikely codepath.
		// So instead, we're just going to wreck the response body and hope that Prometheus
		// or whatever else receives this logs this as a syntax error or something.
		fmt.Fprintf(w, "flush error: %s\n", err.Error())
	}
}

func (h Handler) printMetricFamily(w io.Writer, syntax Syntax, familyName MetricFamilyName, info MetricFamilyInfo, metrics []metric) {
	if len(metrics) == 0 {
		return
	}

	var metricName string
	switch info.Type {
	case MetricTypeGauge:
		metricName = string(familyName)
	case MetricTypeCounter:
		metricName = string(familyName) + "_total"
	case MetricTypeInfo:
		metricName = string(familyName) + "_info"
	default:
		panic("unreachable") // NewMetricSet() should have rejected unknown MetricType values
	}

	if syntax == SyntaxPrometheusLegacy {
		// Prometheus Text Format does not distinguish between metric names and metric family names
		familyName = MetricFamilyName(metricName)
	}

	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", familyName, info.Help, familyName, metricTypeNames[info.Type])

	if h.SortOutput {
		slices.SortFunc(metrics, func(lhs, rhs metric) int {
			return strings.Compare(string(lhs.labels), string(rhs.labels))
		})
	}
	for _, m := range metrics {
		if m.labels == "" {
			fmt.Fprintf(w, "%s ", metricName)
		} else {
			fmt.Fprintf(w, "%s{%s} ", metricName, m.labels)
		}
		if syntax == SyntaxPrometheusLegacy {
			fmt.Fprintf(w, "%g\n", m.value)
		} else {
			// TODO: ugly
			fi := floatInspector{inner: w}
			fmt.Fprintf(&fi, "%g", m.value)
			if fi.clearlyFloat {
				fmt.Fprintf(w, "\n")
			} else {
				fmt.Fprintf(w, ".0\n")
			}
		}
	}
}

type floatInspector struct {
	inner        io.Writer
	clearlyFloat bool
}

func (fi *floatInspector) Write(buf []byte) (int, error) {
	if slices.Contains(buf, '.') {
		fi.clearlyFloat = true
	}
	if slices.Contains(buf, 'e') {
		fi.clearlyFloat = true
	}
	return fi.inner.Write(buf)
}
