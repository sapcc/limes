// SPDX-FileCopyrightText: 2026 Stefan Majewsky <majewsky@gmx.net>
// SPDX-License-Identifier: Apache-2.0

// Package microprom is a minimal alternative implementation of [promhttp],
// intended for very specific situations where the design choices of [prometheus/client_golang]
// cause scaling problems:
//   - metric families with very high cardinality,
//   - that may have lots of label dimensions,
//   - and which do not need to be held in memory, but can instead easily be generated at scrape time (e.g. from a database query).
//
// In this specific circumstance, the internal structure of [prometheus/client_golang]
// leads to abnormally high memory fragmentation and a spiky memory usage pattern overall.
// Implementing the same metrics endpoint with microprom will lead to a
// more stable memory consumption with less intense spikes during scrapes,
// at the cost of slightly more CPU time cost and GC pressure.
//
// A microprom handler produces output in the [Prometheus exposition format], matching the output of promhttp exactly;
// thus it can be scraped by Prometheus or any other OpenTelemetry-compatible metrics collector.
// However, because of the highly specialized focus on high-cardinality database metrics,
// significant parts of the OTLP Stream Model (e.g. summaries, histograms, exemplars) are not implemented.
// The only supported metric types are gauges, counters and info metrics.
//
// # How to use
//
// To get started with microprom, look at how to build a [Handler] instance,
// and follow the documentation from there.
//
// [promhttp]: https://pkg.go.dev/github.com/prometheus/client_golang/prometheus/promhttp
// [prometheus/client_golang]: https://pkg.go.dev/github.com/prometheus/client_golang
// [Prometheus exposition format]: https://prometheus.io/docs/instrumenting/exposition_formats/
package microprom

import (
	"fmt"
	"regexp"
)

// MetricFamilyInfo appears in type [Handler].
type MetricFamilyInfo struct {
	Type MetricType
	Help string
}

var (
	labelNameRx        = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	metricFamilyNameRx = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
)

func (i MetricFamilyInfo) validate(name MetricFamilyName) error {
	if !metricFamilyNameRx.MatchString(string(name)) {
		return fmt.Errorf("in family %q: invalid family name (does not match /%s/)", name, metricFamilyNameRx.String())
	}
	if i.Type >= MetricType(len(metricTypeSuffixes)) {
		return fmt.Errorf("in family %q: invalid value for microprom.MetricType: %d", name, i.Type)
	}
	return nil
}

// MetricFamilyName is the name of a metric family.
//
// Per the [OpenMetrics 1.0] spec, metric family names must match the following regular expression:
//
//	^[a-zA-Z_:][a-zA-Z0-9_:]*$
//
// Package microprom does not implement escaping at the moment;
// metric family names not matching this pattern are invalid and will cause a panic.
// This restriction may be lifted in a future version.
//
// [OpenMetrics 1.0]: https://prometheus.io/docs/specs/om/open_metrics_spec/
type MetricFamilyName string

// MetricType is a enum. It appears in type [MetricFamilyInfo].
//
// As documented on the individual values below,
// the choice of metric type determines how [MetricSet.Add] derives the metric name.
type MetricType uint

const (
	// MetricTypeGauge is used for metrics that are current measurements,
	// where the absolute value is of interest to a user.
	//
	// For this metric type, the metric name is the same as the metric family name.
	MetricTypeGauge MetricType = iota

	// MetricTypeCounter is used for counting discrete events,
	// where the rate of increase over time is of interest to a user.
	//
	// For this metric type, the metric name is formed by appending "_total" to the metric family name.
	MetricTypeCounter

	// MetricTypeInfo is used for info metrics,
	// which only expose textual information in their labels.
	//
	// For this metric type, the metric name is formed by appending "_info" to the metric family name.
	MetricTypeInfo
)

var (
	metricTypeNames    = []string{"gauge", "counter", "info"}
	metricTypeSuffixes = []string{"", "_total", "_info"}
)

// MetricSet holds a set of metrics.
type MetricSet struct {
	syntax  Syntax
	metrics map[MetricFamilyName][]metric
}

type metric struct {
	labels Labels
	value  float64
}

// NewMetricSet constructs an initially empty [MetricSet] that accepts metrics for the given metric families.
func NewMetricSet(syntax Syntax, families map[MetricFamilyName]MetricFamilyInfo) *MetricSet {
	if syntax > SyntaxOpenMetricsV1 {
		panic(fmt.Sprintf("unknown value for Syntax: %d", syntax))
	}
	m := make(map[MetricFamilyName][]metric, len(families))
	for name, family := range families {
		err := family.validate(name)
		if err != nil {
			// this is fine to panic because it will only blow up in case of gross API misuse
			panic(err.Error())
		}
		m[name] = nil
	}
	return &MetricSet{syntax, m}
}

// Add adds a metric to the MetricSet.
//
// The name must be of a metric family that was declared during [NewMetricSet], otherwise Add will panic.
// The metric name will be derived according to the rules documented on the respective [MetricType].
func (ms *MetricSet) Add(name MetricFamilyName, labels Labels, value float64) {
	_, ok := ms.metrics[name]
	if !ok {
		panic("no such family: " + string(name))
	}
	ms.metrics[name] = append(ms.metrics[name], metric{labels, value})
}

// Syntax is an enum, defining which exposition format will be used by [MetricSet].
//
//   - SyntaxPrometheusLegacy corresponds to the [Prometheus Text Format].
//   - SyntaxOpenMetricsV1 corresponds to the [OpenMetrics 1.0] text format
//   - Additional formats may be added in the future (e.g. OpenMetrics 2.0, once it is stabilized).
//
// [Prometheus Text Format]: https://prometheus.io/docs/instrumenting/exposition_formats/
// [OpenMetrics 1.0]: https://prometheus.io/docs/specs/om/open_metrics_spec/
type Syntax uint

const (
	// SyntaxPrometheusLegacy corresponds to the Prometheus text format (currently version 0.0.4).
	SyntaxPrometheusLegacy Syntax = iota
	// SyntaxOpenMetricsV1 corresponds to the OpenMetrics 1.0 text format.
	SyntaxOpenMetricsV1
)
