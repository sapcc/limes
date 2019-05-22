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

package test

import "time"

var clockSeconds int64 = -1

//TimeNow replaces time.Now in unit tests. It provides a simulated clock that
//behaves the same in every test run: It returns the UNIX epoch the first time,
//and then advances by one second on every call.
func TimeNow() time.Time {
	clockSeconds++
	return time.Unix(clockSeconds, 0).UTC()
}

//ResetTime should be called at the start of unit tests that use TimeNow, to
//ensure a reproducible flow of time.
func ResetTime() {
	clockSeconds = -1
}
