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
	"testing"

	"github.com/majewsky/schwift"
)

func TestContainerLifecycle(t *testing.T) {
	testWithAccount(t, func(a *schwift.Account) {
		containerName := getRandomName()
		c := a.Container(containerName)

		expectString(t, c.Name(), containerName)
		if c.Account() != a {
			t.Errorf("expected c.Account() = %#v, got %#v instead\n", a, c.Account())
		}

		exists, err := c.Exists()
		expectSuccess(t, err)
		expectBool(t, exists, false)

		_, err = c.Headers()
		expectError(t, err, "expected 204 response, got 404 instead")
		expectBool(t, schwift.Is(err, http.StatusNotFound), true)
		expectBool(t, schwift.Is(err, http.StatusNoContent), false)

		//DELETE should be idempotent and not return success on non-existence, but
		//OpenStack LOVES to be inconsistent with everything (including, notably, itself)
		err = c.Delete(nil)
		expectError(t, err, "expected 204 response, got 404 instead: <html><h1>Not Found</h1><p>The resource could not be found.</p></html>")

		err = c.Create(nil)
		expectSuccess(t, err)

		exists, err = c.Exists()
		expectSuccess(t, err)
		expectBool(t, exists, true)

		err = c.Delete(nil)
		expectSuccess(t, err)
	})
}

func TestContainerUpdate(t *testing.T) {
	testWithContainer(t, func(c *schwift.Container) {

		hdr, err := c.Headers()
		expectSuccess(t, err)
		expectBool(t, hdr.ObjectCount().Exists(), true)
		expectUint64(t, hdr.ObjectCount().Get(), 0)

		hdr = schwift.NewContainerHeaders()
		hdr.ObjectCountQuota().Set(23)
		hdr.BytesUsedQuota().Set(42)

		err = c.Update(hdr, nil)
		expectSuccess(t, err)

		hdr, err = c.Headers()
		expectSuccess(t, err)
		expectUint64(t, hdr.BytesUsedQuota().Get(), 42)
		expectUint64(t, hdr.ObjectCountQuota().Get(), 23)

	})
}

func expectContainerExistence(t *testing.T, c *schwift.Container, expectedExists bool) {
	t.Helper()
	c.Invalidate()
	actualExists, err := c.Exists()
	expectSuccess(t, err)
	expectBool(t, actualExists, expectedExists)
}
