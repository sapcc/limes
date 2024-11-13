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

// Package liquidapi provides a runtime for servers that implement the LIQUID
// API and nothing else. The application will only have to provide a type that
// implements the Logic interface. Then it can call Run() on an instance of it
// to parse configuration, connect to OpenStack and run the HTTP server.
//
// Ref: <https://pkg.go.dev/github.com/sapcc/go-api-declarations/liquid>
//
// This package also provides a Gophercloud-based Client for talking to the
// LIQUID API. Realistically, only Limes and limesctl will need this though. :)
package liquidapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/go-bits/gophercloudext"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/httpext"
	"github.com/sapcc/go-bits/osext"
	"github.com/sapcc/go-bits/respondwith"
)

// Logic is the interface for types that implement the core logic of a liquid.
//
// Besides Init, all methods may be called in parallel, so the implementation
// must make sure to protect shared data with mutexes. Note that
// gophercloud.ServiceClient instances are generally safe to use concurrently.
type Logic interface {
	// Init is called once at the start of Run(). The logic can use this to
	// obtain its gophercloud.ServiceClient instances.
	Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error

	// BuildServiceInfo will be called once directly after Init(), and then
	// periodically (as configured in RunOpts). The previous ServiceInfo will be
	// served until this call returns, so it's not a big deal if this call takes
	// a long time.
	BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error)

	// These methods represent all the API endpoints of LIQUID that do actual work.
	//
	// The latest ServiceInfo is provided for reference. Only a shallow copy is
	// provided, so implementations must make sure to not edit the ServiceInfo in
	// order to uphold thread-safety.
	ScanCapacity(ctx context.Context, req liquid.ServiceCapacityRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceCapacityReport, error)
	ScanUsage(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceUsageReport, error)
	SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error
}

// RunOpts provides configuration to func Run().
type RunOpts struct {
	// If set, the file at $LIQUID_CONFIG_PATH will be json.Unmarshal()ed into
	// the Logic instance to supply configuration to it, before Init() is called.
	TakesConfiguration bool

	// If set, when the runtime loads its oslo.policy from $LIQUID_POLICY_PATH,
	// YAML will be supported in addition to JSON. This is an explicit dependency
	// injection slot to allow the caller to choose their YAML library.
	YAMLUnmarshal func(in []byte, out any) error

	// How often the runtime will call BuildServiceInfo() to refresh the
	// ServiceInfo of the liquid. The zero value can be used for liquids with
	// static ServiceInfo; no polling will be performed then.
	ServiceInfoRefreshInterval time.Duration

	// How many HTTP requests may be served concurrently. If set, the runtime
	// will ensure that no more than that many calls of ScanCapacity, ScanUsage
	// or SetQuota are ongoing at any time. The default (0) imposes no limit.
	MaxConcurrentRequests int

	// (Required.) Where the HTTP server will listen by default, e.g. ":8080".
	// Can be overridden at runtime by setting $LIQUID_LISTEN_ADDRESS.
	DefaultListenAddress string

	// If set, the server will be run with TLS support. Can be overridden at
	// runtime by setting $LIQUID_TLS_CERT_PATH and $LIQUID_TLS_KEY_PATH.
	DefaultTLSCertificatePath string
	DefaultTLSPrivateKeyPath  string
}

type runtime struct {
	Logic            Logic
	ServiceInfo      liquid.ServiceInfo
	ServiceInfoMutex sync.RWMutex
	TokenValidator   gopherpolicy.Validator
}

// Run spawns an HTTP server that serves the LIQUID API, using the provided
// Logic to answer requests.
//
// It will connect to OpenStack using the standard OS_* environment variables.
// The provided credentials need to have enough access to execute all the API
// requests that the Logic type wants to do.
//
// Incoming requests will be authorized using oslo.policy config loaded from a
// policy file at $LIQUID_POLICY_PATH. The following rules must be defined:
//   - "liquid:get_info"
//   - "liquid:get_capacity"
//   - "liquid:get_usage" (object parameter "project_uuid")
//   - "liquid:set_quota" (object parameter "project_uuid")
//
// Please refer to the documentation on type RunOpts for various other
// behaviors that this function provides.
func Run(ctx context.Context, logic Logic, opts RunOpts) error {
	// validate RunOpts
	if opts.DefaultListenAddress == "" {
		return errors.New("missing required value: liquidapi.RunOpts.DefaultListenAddress")
	}

	// read configuration if requested
	if opts.TakesConfiguration {
		path, err := osext.NeedGetenv("LIQUID_CONFIG_PATH")
		if err != nil {
			return err
		}
		buf, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		err = json.Unmarshal(buf, logic)
		if err != nil {
			return fmt.Errorf("while parsing configuration from %s: %w", path, err)
		}
	}

	// connect to OpenStack
	provider, eo, err := gophercloudext.NewProviderClient(ctx, nil)
	if err != nil {
		return err
	}

	// initialize TokenValidator
	identityV3, err := openstack.NewIdentityV3(provider, eo)
	if err != nil {
		return fmt.Errorf("cannot initialize Keystone v3 client: %w", err)
	}
	tv := &gopherpolicy.TokenValidator{
		IdentityV3: identityV3,
		Cacher:     gopherpolicy.InMemoryCacher(),
	}
	policyPath, err := osext.NeedGetenv("LIQUID_POLICY_PATH")
	if err != nil {
		return err
	}
	err = tv.LoadPolicyFile(policyPath, opts.YAMLUnmarshal)
	if err != nil {
		return err
	}

	// initialize logic
	err = logic.Init(ctx, provider, eo)
	if err != nil {
		return fmt.Errorf("during Logic.Init(): %w", err)
	}
	serviceInfo, err := logic.BuildServiceInfo(ctx)
	if err != nil {
		return fmt.Errorf("during Logic.BuildServiceInfo(): %w", err)
	}
	rt := &runtime{
		Logic:          logic,
		ServiceInfo:    serviceInfo,
		TokenValidator: tv,
	}

	// if necessary, start a goroutine that polls for ServiceInfo updates
	// (this requires some concurrency infrastructure to translate errors from
	// BuildServiceInfo into a shutdown of the HTTP server)
	errChan := make(chan error, 1)
	if opts.ServiceInfoRefreshInterval != 0 {
		ctxWithCancel, cancel := context.WithCancel(ctx)
		ctx = ctxWithCancel // if the ServiceInfo update fails, it can cancel the HTTP server and cause a process shutdown
		go rt.pollServiceInfo(ctx, cancel, opts.ServiceInfoRefreshInterval, errChan)
	}

	// build HTTP handler
	muxer := http.NewServeMux()
	muxer.Handle("/", httpapi.Compose(
		rt,
		httpapi.HealthCheckAPI{SkipRequestLog: true},
		httpapi.WithGlobalMiddleware(limitRequestsMiddleware(opts.MaxConcurrentRequests)),
	))
	muxer.Handle("/metrics", promhttp.Handler())

	// run HTTP server
	listenAddr := osext.GetenvOrDefault("LIQUID_LISTEN_ADDRESS", opts.DefaultListenAddress)
	if opts.DefaultTLSCertificatePath != "" {
		certFile := osext.GetenvOrDefault("LIQUID_TLS_CERT_PATH", opts.DefaultTLSCertificatePath)
		keyFile := osext.GetenvOrDefault("LIQUID_TLS_KEY_PATH", opts.DefaultTLSPrivateKeyPath)
		err = httpext.ListenAndServeTLSContext(ctx, listenAddr, certFile, keyFile, muxer)
	} else {
		err = httpext.ListenAndServeContext(ctx, listenAddr, muxer)
	}
	if err == nil {
		// if we terminated because of an error in pollServiceInfo, fetch the error
		select {
		case err = <-errChan:
		default: // do not block if no error was sent
		}
	}
	return err
}

func (rt *runtime) pollServiceInfo(ctx context.Context, cancelHTTPServer func(), interval time.Duration, errChan chan<- error) {
	defer cancelHTTPServer()
	defer close(errChan)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			serviceInfo, err := rt.Logic.BuildServiceInfo(ctx)
			if err == nil {
				rt.setServiceInfo(serviceInfo)
			} else {
				cancelHTTPServer()
				errChan <- fmt.Errorf("during Logic.BuildServiceInfo(): %w", err)
				return
			}
		}
	}
}

func (rt *runtime) setServiceInfo(serviceInfo liquid.ServiceInfo) {
	rt.ServiceInfoMutex.Lock()
	defer rt.ServiceInfoMutex.Unlock()
	rt.ServiceInfo = serviceInfo
}

func (rt *runtime) getServiceInfo() liquid.ServiceInfo {
	rt.ServiceInfoMutex.RLock()
	defer rt.ServiceInfoMutex.RUnlock()
	return rt.ServiceInfo
}

// The motivation for limiting the number of concurrent requests is that I want
// to run liquids with severely restricted memory limits to keep resource usage
// under control. Resource usage mostly scales with the amount of concurrency,
// so this should allow for keeping resource usage graphs nice and flat.
func limitRequestsMiddleware(maxRequests int) func(http.Handler) http.Handler {
	return func(inner http.Handler) http.Handler {
		if maxRequests == 0 {
			// no limit
			return inner
		}

		// Source for this semaphore pattern: <https://eli.thegreenplace.net/2019/on-concurrency-in-go-http-servers/>
		semaphore := make(chan struct{}, maxRequests)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			inner.ServeHTTP(w, r)
		})
	}
}

// AddTo implements the httpapi.API interface.
func (rt *runtime) AddTo(r *mux.Router) {
	r.Methods("GET").Path("/v1/info").HandlerFunc(rt.handleGetInfo)
	r.Methods("POST").Path("/v1/report-capacity").HandlerFunc(rt.handleReportCapacity)
	r.Methods("POST").Path("/v1/projects/{project_id}/report-usage").HandlerFunc(rt.handleReportUsage)
	r.Methods("PUT").Path("/v1/projects/{project_id}/quota").HandlerFunc(rt.handleSetQuota)
}

func (rt *runtime) handleGetInfo(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/info")
	if !rt.requireToken(w, r, "liquid:get_info") {
		return
	}

	respondwith.JSON(w, http.StatusOK, rt.getServiceInfo())
}

func (rt *runtime) handleReportCapacity(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/report-capacity")
	if !rt.requireToken(w, r, "liquid:get_capacity") {
		return
	}

	var req liquid.ServiceCapacityRequest
	if !requireJSON(w, r, &req) {
		return
	}

	resp, err := rt.Logic.ScanCapacity(r.Context(), req, rt.getServiceInfo())
	if respondwith.ErrorText(w, err) {
		return
	}
	respondwith.JSON(w, http.StatusOK, resp)
}

func (rt *runtime) handleReportUsage(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/projects/:id/report-usage")
	if !rt.requireToken(w, r, "liquid:get_usage") {
		return
	}
	vars := mux.Vars(r)

	var req liquid.ServiceUsageRequest
	if !requireJSON(w, r, &req) {
		return
	}

	resp, err := rt.Logic.ScanUsage(r.Context(), vars["project_id"], req, rt.getServiceInfo())
	if respondwith.ErrorText(w, err) {
		return
	}
	respondwith.JSON(w, http.StatusOK, resp)
}

func (rt *runtime) handleSetQuota(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/projects/:id/quota")
	if !rt.requireToken(w, r, "liquid:set_quota") {
		return
	}
	vars := mux.Vars(r)

	var req liquid.ServiceQuotaRequest
	if !requireJSON(w, r, &req) {
		return
	}

	err := rt.Logic.SetQuota(r.Context(), vars["project_id"], req, rt.getServiceInfo())
	if respondwith.ErrorText(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (rt *runtime) requireToken(w http.ResponseWriter, r *http.Request, policyRule string) bool {
	t := rt.TokenValidator.CheckToken(r)
	t.Context.Request = mux.Vars(r)
	return t.Require(w, policyRule)
}

func requireJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(&target)
	if err == nil {
		return true
	} else {
		msg := fmt.Sprintf("request body is not a valid JSON representation of %T: %s", target, err.Error())
		http.Error(w, msg, http.StatusBadRequest)
		return false
	}
}
