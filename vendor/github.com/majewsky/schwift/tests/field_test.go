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
	"net/http"
	"strconv"
	"testing"

	"github.com/majewsky/schwift"
)

func TestFieldString(t *testing.T) {
	hdr := schwift.NewAccountHeaders()
	expectBool(t, hdr.TempURLKey().Exists(), false)
	expectString(t, hdr.TempURLKey().Get(), "")
	expectSuccess(t, hdr.Validate())

	hdr.Headers["X-Account-Meta-Temp-Url-Key"] = ""
	expectBool(t, hdr.TempURLKey().Exists(), false)
	expectString(t, hdr.TempURLKey().Get(), "")
	expectSuccess(t, hdr.Validate())

	hdr.Headers["X-Account-Meta-Temp-Url-Key"] = "foo"
	expectBool(t, hdr.TempURLKey().Exists(), true)
	expectString(t, hdr.TempURLKey().Get(), "foo")
	expectSuccess(t, hdr.Validate())

	hdr.TempURLKey().Set("bar")
	expectHeaders(t, hdr.Headers, map[string]string{
		"X-Account-Meta-Temp-Url-Key": "bar",
	})
	hdr.TempURLKey().Clear()
	expectHeaders(t, hdr.Headers, map[string]string{
		"X-Account-Meta-Temp-Url-Key": "",
	})
	hdr.TempURLKey().Del()
	expectHeaders(t, hdr.Headers, nil)
	hdr.TempURLKey().Clear()
	expectHeaders(t, hdr.Headers, map[string]string{
		"X-Account-Meta-Temp-Url-Key": "",
	})
}

////////////////////////////////////////////////////////////////////////////////

func TestFieldTimestamp(t *testing.T) {
	testWithAccount(t, func(a *schwift.Account) {
		hdr, err := a.Headers()
		if !expectSuccess(t, err) {
			return
		}

		expectBool(t, hdr.CreatedAt().Exists(), true)

		actual := float64(hdr.CreatedAt().Get().UnixNano()) / 1e9
		expected, _ := strconv.ParseFloat(hdr.Headers["X-Timestamp"], 64)
		expectFloat64(t, actual, expected)
	})

	hdr := schwift.NewAccountHeaders()
	expectBool(t, hdr.CreatedAt().Exists(), false)
	expectBool(t, hdr.CreatedAt().Get().IsZero(), true)
	expectSuccess(t, hdr.Validate())

	hdr.Headers["X-Timestamp"] = "wtf"
	expectBool(t, hdr.CreatedAt().Exists(), true)
	expectBool(t, hdr.CreatedAt().Get().IsZero(), true)
	expectError(t, hdr.Validate(), `Bad header X-Timestamp: strconv.ParseFloat: parsing "wtf": invalid syntax`)
}

func TestFieldHTTPTimestamp(t *testing.T) {
	testWithContainer(t, func(c *schwift.Container) {
		obj := c.Object("test")
		err := obj.Upload(nil, nil)
		if !expectSuccess(t, err) {
			return
		}

		hdr, err := obj.Headers()
		if !expectSuccess(t, err) {
			return
		}
		expectBool(t, hdr.UpdatedAt().Exists(), true)

		actual := hdr.UpdatedAt().Get()
		expected, _ := http.ParseTime(hdr.Get("Last-Modified"))
		expectInt64(t, actual.Unix(), expected.Unix())
	})

	hdr := schwift.NewObjectHeaders()
	expectBool(t, hdr.UpdatedAt().Exists(), false)
	expectBool(t, hdr.UpdatedAt().Get().IsZero(), true)
	expectSuccess(t, hdr.Validate())

	hdr.Headers["Last-Modified"] = "wtf"
	expectBool(t, hdr.UpdatedAt().Exists(), true)
	expectBool(t, hdr.UpdatedAt().Get().IsZero(), true)
	expectError(t, hdr.Validate(), `Bad header Last-Modified: parsing time "wtf" as "Mon Jan _2 15:04:05 2006": cannot parse "wtf" as "Mon"`)
}

////////////////////////////////////////////////////////////////////////////////

func TestFieldUint64(t *testing.T) {
	hdr := schwift.NewAccountHeaders()
	expectBool(t, hdr.BytesUsedQuota().Exists(), false)
	expectUint64(t, hdr.BytesUsedQuota().Get(), 0)
	expectSuccess(t, hdr.Validate())

	hdr.Headers["X-Account-Meta-Quota-Bytes"] = "23"
	expectBool(t, hdr.BytesUsedQuota().Exists(), true)
	expectUint64(t, hdr.BytesUsedQuota().Get(), 23)
	expectSuccess(t, hdr.Validate())

	hdr.Headers["X-Account-Meta-Quota-Bytes"] = "-23"
	expectBool(t, hdr.BytesUsedQuota().Exists(), true)
	expectUint64(t, hdr.BytesUsedQuota().Get(), 0)
	expectError(t, hdr.Validate(), `Bad header X-Account-Meta-Quota-Bytes: strconv.ParseUint: parsing "-23": invalid syntax`)

	hdr.BytesUsedQuota().Set(9001)
	expectHeaders(t, hdr.Headers, map[string]string{
		"X-Account-Meta-Quota-Bytes": "9001",
	})
	hdr.BytesUsedQuota().Clear()
	expectHeaders(t, hdr.Headers, map[string]string{
		"X-Account-Meta-Quota-Bytes": "",
	})
	hdr.BytesUsedQuota().Del()
	expectHeaders(t, hdr.Headers, nil)
	hdr.BytesUsedQuota().Clear()
	expectHeaders(t, hdr.Headers, map[string]string{
		"X-Account-Meta-Quota-Bytes": "",
	})
}

func TestFieldUint64Readonly(t *testing.T) {
	hdr := schwift.NewAccountHeaders()
	expectBool(t, hdr.BytesUsed().Exists(), false)
	expectUint64(t, hdr.BytesUsed().Get(), 0)
	expectSuccess(t, hdr.Validate())

	hdr.Headers["X-Account-Bytes-Used"] = "23"
	expectBool(t, hdr.BytesUsed().Exists(), true)
	expectUint64(t, hdr.BytesUsed().Get(), 23)
	expectSuccess(t, hdr.Validate())

	hdr.Headers["X-Account-Bytes-Used"] = "-23"
	expectBool(t, hdr.BytesUsed().Exists(), true)
	expectUint64(t, hdr.BytesUsed().Get(), 0)
	expectError(t, hdr.Validate(), `Bad header X-Account-Bytes-Used: strconv.ParseUint: parsing "-23": invalid syntax`)
}
