// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package ratesv2

// ClusterReport is the response type for GET /rates/v2/cluster.
// It contains all dynamic rate data for the whole cluster.
// This can contain aggregated data from lower object levels or data only available on cluster level.
type ClusterReport struct {
}
