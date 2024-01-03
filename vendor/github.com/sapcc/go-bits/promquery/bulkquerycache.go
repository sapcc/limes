/******************************************************************************
*
*  Copyright 2023 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package promquery

import (
	"fmt"
	"time"

	"github.com/prometheus/common/model"
)

// BulkQueryCache queries Prometheus in bulk and caches the result.
//
// When certain simple Prometheus queries need to be executed repeatedly with
// different parameters, it's usually more efficient to request the entire data
// set in bulk instead of querying for each individual values. For example,
// querying 100 times for
//
//	sum(filesystem_capacity_bytes{hostname="%s"})
//	sum(filesystem_used_bytes{hostname="%s"})
//
// for different hostnames can be optimized by querying once for
//
//	sum by (hostname) (filesystem_capacity_bytes)
//	sum by (hostname) (filesystem_used_bytes)
//
// and using this cached result. BulkQueryCache provides the reusable
// infrastructure for this access pattern. It is parametrized on a cache key
// (K) which identifies a single record to be retrieved, and the cached value
// (V) containing such a single record. In this expanded example, K and V are
// instantiated as HostName and HostFilesystemMetrics, respectively:
type BulkQueryCache[K comparable, V any] struct {
	client          Client
	queries         []BulkQuery[K, V]
	refreshInterval time.Duration
	filledAt        *time.Time
	entries         map[K]*V
}

// BulkQuery is a query that can be executed by type BulkQueryCache
// (see there for details).
type BulkQuery[K comparable, V any] struct {
	// The PromQL query returning the bulk data.
	Query string
	// A user-readable description for this dataset that can be interpolated into log messages.
	Description string
	// Computes the cache key for each sample returned by the query.
	Keyer func(*model.Sample) K
	// Fills data from this sample into the cache entry.
	Filler func(*V, *model.Sample)
}

// NewBulkQueryCache initializes a BulkQueryCache that executes the given
// queries once per refresh interval.
func NewBulkQueryCache[K comparable, V any](queries []BulkQuery[K, V], refreshInterval time.Duration, client Client) *BulkQueryCache[K, V] {
	return &BulkQueryCache[K, V]{
		client:          client,
		queries:         queries,
		refreshInterval: refreshInterval,
	}
}

// Get returns the entry for this key, or a zero-initialized entry if this key
// does not exist in the dataset.
func (c *BulkQueryCache[K, V]) Get(key K) (entry V, err error) {
	err = c.fillCacheIfNecessary()
	if err != nil {
		return
	}
	entryPtr := c.entries[key]
	if entryPtr != nil {
		entry = *entryPtr
	}
	return
}

func (c *BulkQueryCache[K, V]) fillCacheIfNecessary() error {
	//query Prometheus only on first call or if cache is too old
	if c.filledAt != nil && c.filledAt.After(time.Now().Add(-c.refreshInterval)) {
		return nil
	}

	result := make(map[K]*V)
	for _, q := range c.queries {
		vector, err := c.client.GetVector(q.Query)
		if err != nil {
			return fmt.Errorf("cannot collect %s: %w", q.Description, err)
		}
		for _, sample := range vector {
			key := q.Keyer(sample)
			entry := result[key]
			if entry == nil {
				var empty V
				entry = &empty
				result[key] = entry
			}
			q.Filler(entry, sample)
		}
	}

	now := time.Now()
	c.filledAt = &now
	c.entries = result
	return nil
}
