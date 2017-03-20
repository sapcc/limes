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

package output

//Scope contains data about a cluster, domain or project in the format returned by the API.
type Scope struct {
	ID       string     `json:"id"`
	Services []*Service `json:"services,keepempty"`
}

//FindService finds the service with that type within this scope, or appends a
//new service.
func (s *Scope) FindService(serviceType string) *Service {
	for _, srv := range s.Services {
		if srv.Type == serviceType {
			return srv
		}
	}
	srv := &Service{Type: serviceType}
	s.Services = append(s.Services, srv)
	return srv
}

//Scopes is a list of Scope.
type Scopes struct {
	Scopes []*Scope
}

//FindScope finds the scope with that ID within this list, or appends a new
//Scope instance.
func (s *Scopes) FindScope(id string) *Scope {
	for _, obj := range s.Scopes {
		if obj.ID == id {
			return obj
		}
	}
	scope := &Scope{ID: id}
	s.Scopes = append(s.Scopes, scope)
	return scope
}
