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

package datamodel

// ConfirmProjectCommitments will try to confirm as many additional commitments
// as can be covered at the current capacity and usage values.
func ConfirmProjectCommitments(serviceType, resourceName string) error {
	//TODO implement (this stub allows UI development on the commitment API to proceed)
	//TODO note to self: generate audit events upon confirmation
	return nil
}
