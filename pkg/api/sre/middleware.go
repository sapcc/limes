/******************************************************************************
*
*  Copyright 2019 SAP SE
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

//Package sre contains an instrumentation middleware similar to
//github.com/prometheus/client_golang/prometheus/promhttp, but with an
//additional "path" label identifying the github.com/gorilla/mux route taken by
//the request.
package sre

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
)

//Config contains the configuration options for this package.
type Config struct {
	AppName                  string
	FirstByteDurationBuckets []float64
	ResponseDurationBuckets  []float64
	RequestBodySizeBuckets   []float64
	ResponseBodySizeBuckets  []float64
}

var (
	appName                 string
	metricFirstByteDuration *prometheus.HistogramVec
	metricResponseDuration  *prometheus.HistogramVec
	metricRequestBodySize   *prometheus.HistogramVec
	metricResponseBodySize  *prometheus.HistogramVec
)

//Init sets up the metrics used by type Middleware. It must be called exactly once.
func Init(cfg Config) {
	appName = cfg.AppName
	labelNames := []string{"app", "method", "status", "path"}

	metricFirstByteDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "httpmux_first_byte_seconds",
		Help:    "Duration in seconds until the first byte was sent in response to HTTP requests received by the application.",
		Buckets: cfg.FirstByteDurationBuckets,
	}, labelNames)
	metricResponseDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "httpmux_response_duration_seconds",
		Help:    "Duration in seconds until the full response was sent in response to HTTP requests received by the application.",
		Buckets: cfg.ResponseDurationBuckets,
	}, labelNames)
	metricRequestBodySize = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "httpmux_request_size_bytes",
		Help:    "Size in bytes of HTTP request bodies received by the application.",
		Buckets: cfg.RequestBodySizeBuckets,
	}, labelNames)
	metricResponseBodySize = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "httpmux_response_size_bytes",
		Help:    "Size in bytes of response bodies sent in response to HTTP requests received by the application.",
		Buckets: cfg.ResponseBodySizeBuckets,
	}, labelNames)

	prometheus.MustRegister(metricFirstByteDuration)
	prometheus.MustRegister(metricResponseDuration)
	prometheus.MustRegister(metricRequestBodySize)
	prometheus.MustRegister(metricResponseBodySize)
}

//Instrument applies this middleware to the given http.Handler.
func Instrument(next http.Handler) http.Handler {
	return handler{next}
}

type handler struct {
	Next http.Handler
}

func (h handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	d := newDelegator(w, func(status int) {
		labels := getLabels(status, r)
		metricFirstByteDuration.With(labels).Observe(time.Since(startedAt).Seconds())
	})

	h.Next.ServeHTTP(d, r)

	labels := getLabels(d.Status(), r)
	metricResponseDuration.With(labels).Observe(time.Since(startedAt).Seconds())
	if r.ContentLength != -1 {
		metricRequestBodySize.With(labels).Observe(float64(r.ContentLength))
	}
	metricResponseBodySize.With(labels).Observe(float64(d.Written()))
}

func getLabels(status int, r *http.Request) prometheus.Labels {
	l := prometheus.Labels{
		"method": strings.ToUpper(r.Method),
		"path":   "unknown",
		"app":    appName,
	}

	if status == 0 {
		l["status"] = "200"
	} else {
		l["status"] = strconv.Itoa(status)
	}

	if route := mux.CurrentRoute(r); route != nil {
		if path, err := route.GetPathTemplate(); err != nil {
			l["path"] = path
		}
	}
	return l
}
