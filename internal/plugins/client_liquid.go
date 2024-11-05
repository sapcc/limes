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

package plugins

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/liquid"
)

// Plugin-internal serialization format for metrics (see Scrape() and CollectMetrics()).
//
// When storing metrics from LIQUID in the Limes DB, we must also store parts of the
// MetricFamilyInfo alongside it, since the ServiceInfo might be updated between the serialization
// in Scrape() and the respective deserialization in CollectMetrics().
//
// This logic lives here because it is shared between liquidCapacityPlugin and liquidQuotaPlugin.
type liquidSerializedMetricFamily struct {
	LabelKeys []string        `json:"lk"`
	Metrics   []liquid.Metric `json:"m"`
}

func liquidSerializeMetrics(families map[liquid.MetricName]liquid.MetricFamilyInfo, metrics map[liquid.MetricName][]liquid.Metric) ([]byte, error) {
	serializableMetrics := make(map[liquid.MetricName]liquidSerializedMetricFamily, len(families))
	for metricName, metricFamilyInfo := range families {
		for _, metric := range metrics[metricName] {
			if len(metric.LabelValues) != len(metricFamilyInfo.LabelKeys) {
				return nil, fmt.Errorf("found unexpected number of label values on a %s metric: got %d values for %d keys",
					metricName, len(metric.LabelValues), len(metricFamilyInfo.LabelKeys))
			}
		}

		serializableMetrics[metricName] = liquidSerializedMetricFamily{
			LabelKeys: metricFamilyInfo.LabelKeys,
			Metrics:   metrics[metricName],
		}
	}
	return json.Marshal(serializableMetrics)
}

func liquidDescribeMetrics(ch chan<- *prometheus.Desc, families map[liquid.MetricName]liquid.MetricFamilyInfo, extraLabelKeys []string) {
	for metricName, info := range families {
		ch <- prometheus.NewDesc(string(metricName), info.Help, append(extraLabelKeys, info.LabelKeys...), nil)
	}
}

func liquidCollectMetrics(ch chan<- prometheus.Metric, serializedMetrics []byte, families map[liquid.MetricName]liquid.MetricFamilyInfo, extraLabelKeys, extraLabelValues []string) error {
	var metricFamilies map[liquid.MetricName]liquidSerializedMetricFamily
	err := json.Unmarshal(serializedMetrics, &metricFamilies)
	if err != nil {
		return err
	}

	for metricName, info := range families {
		metricFamily := metricFamilies[metricName]

		desc := prometheus.NewDesc(string(metricName), info.Help, append(slices.Clone(extraLabelKeys), info.LabelKeys...), nil)
		valueType := prometheus.GaugeValue
		if info.Type == liquid.MetricTypeCounter {
			valueType = prometheus.CounterValue
		}

		// build a mapping from the label placements in the serialization to the currently valid one
		labelMapping := make([]int, len(info.LabelKeys))
		for idx, key := range info.LabelKeys {
			labelMapping[idx] = slices.Index(metricFamily.LabelKeys, key)
		}

		// some labels are reused for all metrics
		reorderedLabels := make([]string, len(extraLabelValues)+len(info.LabelKeys))
		copy(reorderedLabels[0:len(extraLabelValues)], extraLabelValues)
		for _, metric := range metricFamily.Metrics {
			// fill the remaining slots with the metrics' specific label values
			offset := len(extraLabelValues)
			for targetIdx, sourceIdx := range labelMapping {
				if sourceIdx == -1 {
					reorderedLabels[offset+targetIdx] = ""
				} else {
					reorderedLabels[offset+targetIdx] = metric.LabelValues[sourceIdx]
				}
			}
			m, err := prometheus.NewConstMetric(desc, valueType, metric.Value, reorderedLabels...)
			if err != nil {
				return err
			}
			ch <- m
		}
	}

	return nil
}
