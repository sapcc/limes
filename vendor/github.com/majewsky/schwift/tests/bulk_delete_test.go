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
	"strings"
	"testing"

	"github.com/majewsky/schwift"
)

func TestBulkDeleteSuccess(t *testing.T) {
	testWithAccount(t, func(a *schwift.Account) {
		c, err := a.Container("schwift-test-bulkdelete").EnsureExists()
		expectSuccess(t, err)
		objs, err := createTestObjects(c)
		expectSuccess(t, err)

		numDeleted, numNotFound, err := c.Account().BulkDelete(objs, nil, nil)
		expectSuccess(t, err)
		expectInt(t, numDeleted, len(objs))
		expectInt(t, numNotFound, 0)
		expectContainerExistence(t, c, true)

		numDeleted, numNotFound, err = c.Account().BulkDelete(objs, nil, nil)
		expectSuccess(t, err)
		expectInt(t, numDeleted, 0)
		expectInt(t, numNotFound, len(objs))
		expectContainerExistence(t, c, true)

		objs, err = createTestObjects(c)
		expectSuccess(t, err)
		cs := []*schwift.Container{c}

		numDeleted, numNotFound, err = c.Account().BulkDelete(objs, cs, nil)
		expectSuccess(t, err)
		expectInt(t, numDeleted, len(objs)+1)
		expectInt(t, numNotFound, 0)
		expectContainerExistence(t, c, false)
	})
}

func TestBulkDeleteError(t *testing.T) {
	testWithContainer(t, func(c *schwift.Container) {
		objs, err := createTestObjects(c)
		expectSuccess(t, err)
		objs = objs[1:]
		cs := []*schwift.Container{c}

		//not deleting all objects should lead to 409 Conflict when deleting the Container
		numDeleted, numNotFound, err := c.Account().BulkDelete(objs, cs, nil)
		expectInt(t, numDeleted, len(objs))
		expectInt(t, numNotFound, 0)
		expectError(t, err, "400 Bad Request (+1 object errors)")
		expectContainerExistence(t, c, true)
	})
}

func createTestObjects(c *schwift.Container) ([]*schwift.Object, error) {
	var objs []*schwift.Object
	for idx := 1; idx <= 5; idx++ {
		obj := c.Object(fmt.Sprintf("object%d", idx))
		err := obj.Upload(strings.NewReader("example"), nil)
		if err != nil {
			return nil, err
		}
		objs = append(objs, obj)
	}
	return objs, nil
}
