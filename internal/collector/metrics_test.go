// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector_test

import (
	"fmt"
	"testing"

	"github.com/sapcc/limes/internal/collector"
)

func BenchmarkLabelRendering(b *testing.B) {
	b.Run("method=fmt.Sprintf", func(b *testing.B) {
		for b.Loop() {
			_ = fmt.Sprintf(`availability_zone=%q,resource=%q,service=%q,service_name=%q`,
				"xx-yy-1a", "cores", "compute", "nova",
			)
		}
	})
	b.Run("method=BuildLabels", func(b *testing.B) {
		for b.Loop() {
			_ = collector.BuildLabels(
				[]string{"availability_zone", "resource", "service", "service_name"},
				"xx-yy-1a", "cores", "compute", "nova",
			)
		}
	})
}
