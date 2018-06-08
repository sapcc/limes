/*******************************************************************************
*
* Copyright 2018 SAP SE
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

package datamodel

import (
	"github.com/sapcc/limes/pkg/limes"
	gorp "gopkg.in/gorp.v2"
)

//Scope contains context that all functions in this module require.
type Scope struct {
	Cluster *limes.Cluster
	Tx      *gorp.Transaction
	//If not nil, this function will be called to log each record that has been
	//created by functions in this module.
	LogAutomaticActions func(msg string, args ...interface{})
}

func (s Scope) logAutomaticAction(msg string, args ...interface{}) {
	if s.LogAutomaticActions != nil {
		s.LogAutomaticActions(msg, args...)
	}
}
