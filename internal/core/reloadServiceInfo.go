// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/jobloop"

	"github.com/sapcc/limes/internal/db"
)

// ReloadServiceInfoJob is a jobloop.CronJob.
//
// It reloads the Cluster LiquidConnections periodically
func (c *Cluster) ReloadServiceInfoJob(registerer prometheus.Registerer) jobloop.Job {
	return (&jobloop.CronJob{
		Metadata: jobloop.JobMetadata{
			ReadableName: "reload service info",
			CounterOpts: prometheus.CounterOpts{
				Name: "limes_service_info_reloads",
				Help: "Counts reloads of the service info from the database.",
			},
			CounterLabels: []string{"service_type"},
		},
		Interval: 30 * time.Second,
		Task:     c.reloadServiceInfo,
	}).Setup(registerer)
}

func (c *Cluster) reloadServiceInfo(ctx context.Context, labels prometheus.Labels) error {
	serviceType := db.ServiceType(labels["service_type"])
	_, err := c.LiquidConnections[serviceType].updateServiceInfo(ctx, dbOnly)
	if err != nil {
		return fmt.Errorf("while reloading service info for ServiceType %s: %w", serviceType, err)
	}
	return nil
}
