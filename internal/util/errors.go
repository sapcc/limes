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

package util

import "github.com/gophercloud/gophercloud"

// UnpackError is usually a no-op, but for some Gophercloud errors, it removes
// the outer layer that obscures the better error message hidden within.
func UnpackError(err error) error {
	switch err := err.(type) {
	case gophercloud.ErrDefault400:
		return err.ErrUnexpectedResponseCode
	case gophercloud.ErrDefault401:
		return err.ErrUnexpectedResponseCode
	case gophercloud.ErrDefault403:
		return err.ErrUnexpectedResponseCode
	case gophercloud.ErrDefault404:
		return err.ErrUnexpectedResponseCode
	case gophercloud.ErrDefault405:
		return err.ErrUnexpectedResponseCode
	case gophercloud.ErrDefault408:
		return err.ErrUnexpectedResponseCode
	case gophercloud.ErrDefault429:
		return err.ErrUnexpectedResponseCode
	case gophercloud.ErrDefault500:
		return err.ErrUnexpectedResponseCode
	case gophercloud.ErrDefault503:
		return err.ErrUnexpectedResponseCode
	default:
		return err
	}
}
