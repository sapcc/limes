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

import (
	"encoding/json"
	"testing"
)

func TestCFMBytesDeserialization(t *testing.T) {
	testcases := map[string]CFMBytes{
		"5 MB":      5e6,
		"1.75 TB":   1.75e12,
		"454.53 GB": 454.53e9,
		"0 bytes":   0,
	}

	for input, expected := range testcases {
		jsonBytes, _ := json.Marshal(input)

		var result CFMBytes
		err := json.Unmarshal(jsonBytes, &result)
		if err != nil {
			t.Errorf("unexpected error while demarshaling %q: %s", input, err.Error())
		}

		if result != expected {
			t.Errorf("expected %q to demarshal to %d, but got %d", input, expected, result)
		}
	}
}
