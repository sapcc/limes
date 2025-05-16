// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"testing"
	"time"

	"github.com/sapcc/go-bits/easypg"

	"github.com/sapcc/limes/internal/test"
)

func TestMain(m *testing.M) {
	easypg.WithTestDB(m, func() int { return m.Run() })
}

func getCollector(t *testing.T, s test.Setup) Collector {
	return Collector{
		Cluster:     s.Cluster,
		DB:          s.DB,
		LogError:    t.Errorf,
		MeasureTime: s.Clock.Now,
		MeasureTimeAtEnd: func() time.Time {
			s.Clock.StepBy(5 * time.Second)
			return s.Clock.Now()
		},
		AddJitter: test.NoJitter,
	}
}
