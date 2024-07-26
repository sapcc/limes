/*******************************************************************************
*
* Copyright 2024 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

package liquid

// MetricName is the name of a metric family.
// For more information, please refer to the "Metrics" section of the package documentation.
type MetricName string

// MetricType is an enum.
// For more information, please refer to the "Metrics" section of the package documentation.
type MetricType string

const (
	MetricTypeUnknown        MetricType = "unknown"
	MetricTypeGauge          MetricType = "gauge"
	MetricTypeCounter        MetricType = "counter"
	MetricTypeStateset       MetricType = "stateset"
	MetricTypeInfo           MetricType = "info"
	MetricTypeHistogram      MetricType = "histogram"
	MetricTypeGaugeHistogram MetricType = "gaugehistogram"
	MetricTypeSummary        MetricType = "summary"
)

// MetricFamilyInfo describes a metric family.
// This type appears in type ServiceInfo.
// For more information, please refer to the "Metrics" section of the package documentation.
type MetricFamilyInfo struct {
	// The metric type.
	// The most common values are MetricTypeGauge and MetricTypeCounter.
	Type MetricType `json:"type"`

	// A brief description of the metric family for human consumption.
	// Should be short enough to be used as a tooltip.
	Help string `json:"help"`

	// All labels that will be present on each metric in this family.
	LabelKeys []string `json:"labelKeys"`
}

// Metric is a metric.
// This type appears in type ServiceCapacityReport.
// For more information, please refer to the "Metrics" section of the package documentation.
//
// Because reports can include very large numbers of Metric instances, this type uses a compact serialization to improve efficiency.
type Metric struct {
	Value float64 `json:"v"`

	// This label set does not include keys to avoid redundant encoding.
	// The slice must be of the same length as the LabelKeys slice in the respective MetricFamilyInfo instance in type ServiceInfo.
	// Each label value is implied to belong to the label key with the same slice index.
	// For example, LabelKeys = ["name","location"] and LabelValues = ["author","work"] represents the label set {name="author",location="work"}.
	LabelValues []string `json:"l"`
}
