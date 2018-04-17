/*******************************************************************************
*
* Copyright 2017 SAP SE
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

package api

import (
	"errors"
	"net/http"

	policy "github.com/databus23/goslo.policy"
	"github.com/gophercloud/gophercloud"
	"github.com/gorilla/mux"
	"github.com/sapcc/limes/pkg/util"
)

//Token represents a user's token, as passed through the X-Auth-Token header of
//a request.
type Token struct {
	enforcer *policy.Enforcer
	context  policy.Context
	err      error
	//some attributes from the policy.Context that are useful for logging
	UserUUID string
	UserName string
}

//CheckToken checks the validity of the request's X-Auth-Token in Keystone, and
//returns a Token instance for checking authorization. Any errors that occur
//during this function are deferred until Require() is called.
func (p *v1Provider) CheckToken(r *http.Request) *Token {
	str := r.Header.Get("X-Auth-Token")
	if str == "" {
		return &Token{err: errors.New("X-Auth-Token header missing")}
	}

	t := &Token{enforcer: p.Config.API.PolicyEnforcer}
	t.context, t.err = p.Cluster.Config.Auth.ValidateToken(str)
	t.context.Request = mux.Vars(r)

	//provide the cluster ID to the policy (this can be used e.g. to restrict
	//cloud-admin operations to a certain cluster)
	if t.context.Auth != nil {
		t.context.Auth["cluster_id"] = p.Cluster.ID
	}

	t.UserUUID = t.context.Auth["user_id"]
	t.UserName = t.context.Auth["user_name"]
	return t
}

//Require checks if the given token has the given permission according to the
//policy.json that is in effect. If not, an error response is written and false
//is returned.
func (t *Token) Require(w http.ResponseWriter, rule string) bool {
	if t.err != nil {
		util.LogError("authentication failed: " + extractErrorMessage(t.err))
		http.Error(w, "Unauthorized", 401)
		return false
	}

	if !t.enforcer.Enforce(rule, t.context) {
		http.Error(w, "Forbidden", 403)
		return false
	}
	return true
}

//Check is like Require, but does not write error responses.
func (t *Token) Check(rule string) bool {
	return t.err == nil && t.enforcer.Enforce(rule, t.context)
}

func extractErrorMessage(err error) string {
	switch e := err.(type) {
	case gophercloud.ErrUnexpectedResponseCode:
		return e.Error()
	case gophercloud.ErrDefault401:
		return e.ErrUnexpectedResponseCode.Error()
	case gophercloud.ErrDefault403:
		return e.ErrUnexpectedResponseCode.Error()
	case gophercloud.ErrDefault404:
		return e.ErrUnexpectedResponseCode.Error()
	case gophercloud.ErrDefault405:
		return e.ErrUnexpectedResponseCode.Error()
	case gophercloud.ErrDefault408:
		return e.ErrUnexpectedResponseCode.Error()
	case gophercloud.ErrDefault429:
		return e.ErrUnexpectedResponseCode.Error()
	case gophercloud.ErrDefault500:
		return e.ErrUnexpectedResponseCode.Error()
	case gophercloud.ErrDefault503:
		return e.ErrUnexpectedResponseCode.Error()
	default:
		return err.Error()
	}
}
