// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"math/rand"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
)

// Collector provides methods that implement the collection jobs performed by
// limes-collect. The struct contains a reference to the Cluster configuration,
// which holds references to the keystone DiscoveryPlugin and LiquidConnections
// (which define the service type to be targeted), and a few other things;
// basically everything that needs to be replaced by a mock implementation for
// the collector's unit tests.
type Collector struct {
	Cluster *core.Cluster
	DB      *gorp.DbMap
	// Usually logg.Error, but can be changed inside unit tests.
	LogError func(msg string, args ...any)
	// Usually time.Now, but can be changed inside unit tests.
	// MeasureTimeAtEnd behaves slightly differently in unit tests: It will advance
	// the mock.Clock before reading it to simulate time passing during the previous task.
	MeasureTime      func() time.Time
	MeasureTimeAtEnd func() time.Time
	// Usually addJitter, but can be changed inside unit tests.
	AddJitter func(time.Duration) time.Duration
}

// NewCollector creates a Collector instance.
func NewCollector(cluster *core.Cluster, dbm *gorp.DbMap) *Collector {
	return &Collector{
		Cluster:          cluster,
		DB:               dbm,
		LogError:         logg.Error,
		MeasureTime:      time.Now,
		MeasureTimeAtEnd: time.Now,
		AddJitter:        addJitter,
	}
}

// addJitter returns a random duration within +/- 10% of the requested value.
// This can be used to even out the load on a scheduled job over time, by
// spreading jobs that would normally be scheduled right next to each other out
// over time without corrupting the individual schedules too much.
func addJitter(duration time.Duration) time.Duration {
	//nolint:gosec // This is not crypto-relevant, so math/rand is okay.
	r := rand.Float64() //NOTE: 0 <= r < 1
	return time.Duration(float64(duration) * (0.9 + 0.2*r))
}

// TaskTiming appears in the task types of our ProducerConsumerJobs.
type TaskTiming struct {
	StartedAt  time.Time // filled during DiscoverTask
	FinishedAt time.Time // filled during ProcessTask
}

// Duration measures the duration of the main portion of a task.
func (t TaskTiming) Duration() time.Duration {
	return t.FinishedAt.Sub(t.StartedAt)
}
