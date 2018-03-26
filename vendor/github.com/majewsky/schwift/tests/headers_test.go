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

package tests

import (
	"testing"

	"github.com/majewsky/schwift"
)

func TestParseAccountHeadersSuccess(t *testing.T) {
	headers := schwift.AccountHeaders{
		Headers: schwift.Headers{
			"X-Account-Bytes-Used":       "1234",
			"X-Account-Object-Count":     "42",
			"X-Account-Container-Count":  "23",
			"X-Account-Meta-Quota-Bytes": "1048576",
			"X-Account-Meta-Foo":         "bar",
		},
	}

	expectSuccess(t, headers.Validate())
	expectUint64(t, headers.BytesUsed().Get(), 1234)
	expectUint64(t, headers.ContainerCount().Get(), 23)
	expectUint64(t, headers.ObjectCount().Get(), 42)
	expectUint64(t, headers.BytesUsedQuota().Get(), 1048576)

	expectString(t, headers.Metadata().Get("foo"), "bar")
	expectString(t, headers.Metadata().Get("Foo"), "bar")
	expectString(t, headers.Metadata().Get("FOO"), "bar")
}

//TODO TestParseAccountHeadersError
