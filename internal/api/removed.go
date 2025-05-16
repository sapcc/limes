// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import "net/http"

// ForbidClusterIDHeader is a global middleware that rejects the
// X-Limes-Cluster-Id header (which was removed from the API spec).
func ForbidClusterIDHeader(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clusterID := r.Header.Get("X-Limes-Cluster-Id")
		if clusterID != "" && clusterID != "current" {
			http.Error(w, "multi-cluster support is removed: the X-Limes-Cluster-Id header is not allowed anymore", http.StatusBadRequest)
		} else {
			inner.ServeHTTP(w, r)
		}
	})
}
