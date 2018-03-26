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
	"fmt"
	"testing"

	"github.com/majewsky/schwift"
)

func TestContainerIterator(t *testing.T) {
	testWithAccount(t, func(a *schwift.Account) {
		cname := func(idx int) string {
			return fmt.Sprintf("schwift-test-listing%d", idx)
		}

		//create test containers that can be listed
		for idx := 1; idx <= 4; idx++ {
			_, err := a.Container(cname(idx)).EnsureExists()
			expectSuccess(t, err)
		}

		//test iteration with empty last page
		iter := a.Containers()
		iter.Prefix = "schwift-test-listing"
		cs, err := iter.NextPage(2)
		expectSuccess(t, err)
		expectContainerNames(t, cs, cname(1), cname(2))
		cs, err = iter.NextPage(2)
		expectSuccess(t, err)
		expectContainerNames(t, cs, cname(3), cname(4))
		cs, err = iter.NextPage(2)
		expectSuccess(t, err)
		expectContainerNames(t, cs)
		cs, err = iter.NextPage(2)
		expectSuccess(t, err)
		expectContainerNames(t, cs)

		//test iteration with partial last page
		iter = a.Containers()
		iter.Prefix = "schwift-test-listing"
		cs, err = iter.NextPage(3)
		expectSuccess(t, err)
		expectContainerNames(t, cs, cname(1), cname(2), cname(3))
		cs, err = iter.NextPage(3)
		expectSuccess(t, err)
		expectContainerNames(t, cs, cname(4))
		cs, err = iter.NextPage(4)
		expectSuccess(t, err)
		expectContainerNames(t, cs)

		//test detailed iteration
		iter = a.Containers()
		iter.Prefix = "schwift-test-listing"
		cis, err := iter.NextPageDetailed(2)
		expectSuccess(t, err)
		expectContainerInfos(t, cis, cname(1), cname(2))
		cis, err = iter.NextPageDetailed(3)
		expectSuccess(t, err)
		expectContainerInfos(t, cis, cname(3), cname(4))
		cis, err = iter.NextPageDetailed(3)
		expectSuccess(t, err)
		expectContainerInfos(t, cis)
		cis, err = iter.NextPageDetailed(3)
		expectSuccess(t, err)
		expectContainerInfos(t, cis)

		//test Foreach
		a.Invalidate()
		iter = a.Containers()
		iter.Prefix = "schwift-test-listing"
		idx := 0
		expectSuccess(t, iter.Foreach(func(c *schwift.Container) error {
			idx++
			expectString(t, c.Name(), cname(idx))
			return nil
		}))
		expectInt(t, idx, 4)
		expectAccountHeadersCached(t, a)

		//test ForeachDetailed
		a.Invalidate()
		iter = a.Containers()
		iter.Prefix = "schwift-test-listing"
		idx = 0
		expectSuccess(t, iter.ForeachDetailed(func(info schwift.ContainerInfo) error {
			idx++
			expectString(t, info.Container.Name(), cname(idx))
			return nil
		}))
		expectInt(t, idx, 4)
		expectAccountHeadersCached(t, a)

		//test Collect
		iter = a.Containers()
		iter.Prefix = "schwift-test-listing"
		cs, err = iter.Collect()
		expectSuccess(t, err)
		expectContainerNames(t, cs, cname(1), cname(2), cname(3), cname(4))

		//test CollectDetailed
		iter = a.Containers()
		iter.Prefix = "schwift-test-listing"
		cis, err = iter.CollectDetailed()
		expectSuccess(t, err)
		expectContainerInfos(t, cis, cname(1), cname(2), cname(3), cname(4))

		//cleanup
		iter = a.Containers()
		iter.Prefix = "schwift-test-listing"
		expectSuccess(t, iter.Foreach(func(c *schwift.Container) error {
			return c.Delete(nil)
		}))
	})
}

func expectAccountHeadersCached(t *testing.T, a *schwift.Account) {
	requestCountBefore := a.Backend().(*RequestCountingBackend).Count
	_, err := a.Headers()
	expectSuccess(t, err)
	requestCountAfter := a.Backend().(*RequestCountingBackend).Count

	t.Helper()
	if requestCountBefore != requestCountAfter {
		t.Error("Account.Headers was expected to use cache, but issued HEAD request")
	}
}

func expectContainerNames(t *testing.T, actualContainers []*schwift.Container, expectedNames ...string) {
	t.Helper()
	if len(actualContainers) != len(expectedNames) {
		t.Errorf("expected %d containers, got %d containers",
			len(expectedNames), len(actualContainers))
		return
	}
	for idx, c := range actualContainers {
		if c.Name() != expectedNames[idx] {
			t.Errorf("expected containers[%d].Name() == %q, got %q",
				idx, expectedNames[idx], c.Name())
		}
	}
}

func expectContainerInfos(t *testing.T, actualInfos []schwift.ContainerInfo, expectedNames ...string) {
	t.Helper()
	if len(actualInfos) != len(expectedNames) {
		t.Errorf("expected %d containers, got %d containers",
			len(expectedNames), len(actualInfos))
		return
	}
	for idx, info := range actualInfos {
		if info.Container.Name() != expectedNames[idx] {
			t.Errorf("expected containers[%d].Name() == %q, got %q",
				idx, expectedNames[idx], info.Container.Name())
		}
		//TODO: upload test object of defined size to the listed containers to
		//check if this zero is not just the default value
		if info.BytesUsed != 0 {
			t.Errorf("expected containers[%d] bytesUsed == 0, got %d",
				idx, info.BytesUsed)
		}
		if info.ObjectCount != 0 {
			t.Errorf("expected containers[%d] objectCount == 0, got %d",
				idx, info.ObjectCount)
		}
		if info.LastModified.IsZero() {
			t.Errorf("containers[%d].LastModified is zero", idx)
		}
	}
}
