/******************************************************************************
*
*  Copyright 2018 Stefan Majewsky <majewsky@gmx.net>
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

package schwift

import (
	"net/http"
)

//Backend is the interface between Schwift and the libraries providing
//authentication for it.
//
//TODO list implementations
type Backend interface {
	//EndpointURL returns the endpoint URL from the Keystone catalog for the
	//Swift account that this backend operates on. It should look like
	//`http://domain.tld/v1/AUTH_projectid/`.
	EndpointURL() string
	//Clone returns a deep clone of this backend with the endpoint URL changed to
	//the given URL.
	Clone(newEndpointURL string) Backend
	//Do executes the given HTTP request after adding to it the X-Auth-Token
	//header containing the backend's current Keystone (or Swift auth) token. It
	//may also set other headers, such as User-Agent. If the status code returned
	//is 401, it shall attempt to acquire a new auth token and restart the
	//request with the new token.
	Do(req *http.Request) (*http.Response, error)
}
