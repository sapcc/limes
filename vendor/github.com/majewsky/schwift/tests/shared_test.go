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
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"math"
	"os"
	"testing"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/objectstorage/v1/swauth"
	"github.com/majewsky/schwift"
	"github.com/majewsky/schwift/gopherschwift"
)

func testWithAccount(t *testing.T, testCode func(a *schwift.Account)) {
	stAuth := os.Getenv("ST_AUTH")
	stUser := os.Getenv("ST_USER")
	stKey := os.Getenv("ST_KEY")
	var client *gophercloud.ServiceClient

	if stAuth == "" && stUser == "" && stKey == "" {
		//option 1: Keystone authentication
		authOptions, err := openstack.AuthOptionsFromEnv()
		if err != nil {
			t.Error("missing Swift credentials (need either ST_AUTH, ST_USER, ST_KEY or OS_* variables)")
			t.Error("openstack.AuthOptionsFromEnv returned: " + err.Error())
			return
		}
		provider, err := openstack.AuthenticatedClient(authOptions)
		if err != nil {
			t.Errorf("openstack.AuthenticatedClient returned: " + err.Error())
			return
		}
		client, err = openstack.NewObjectStorageV1(provider, gophercloud.EndpointOpts{})
		if err != nil {
			t.Errorf("openstack.NewObjectStorageV1 returned: " + err.Error())
			return
		}
	} else {
		//option 2: Swift authentication v1
		provider, err := openstack.NewClient(stAuth)
		if err != nil {
			t.Errorf("openstack.NewClient returned: " + err.Error())
			return
		}
		client, err = swauth.NewObjectStorageV1(provider, swauth.AuthOpts{User: stUser, Key: stKey})
		if err != nil {
			t.Errorf("swauth.NewObjectStorageV1 returned: " + err.Error())
			return
		}
	}

	account, err := gopherschwift.Wrap(client)
	if err != nil {
		t.Error(err.Error())
		return
	}
	account, err = schwift.InitializeAccount(
		&RequestCountingBackend{Inner: account.Backend()},
	)
	if err != nil {
		t.Error(err.Error())
		return
	}
	testCode(account)
}

func testWithContainer(t *testing.T, testCode func(c *schwift.Container)) {
	testWithAccount(t, func(a *schwift.Account) {
		containerName := getRandomName()
		container, err := a.Container(containerName).EnsureExists()
		expectSuccess(t, err)

		testCode(container)

		//cleanup
		exists, err := container.Exists()
		expectSuccess(t, err)
		if exists {
			expectSuccess(t, container.Objects().Foreach(func(o *schwift.Object) error {
				return o.Delete(nil)
			}))
			err = container.Delete(nil)
			expectSuccess(t, err)
		}
	})
}

////////////////////////////////////////////////////////////////////////////////

func etagOf(buf []byte) string {
	hash := md5.Sum(buf)
	return hex.EncodeToString(hash[:])
}

func getRandomName() string {
	var buf [16]byte
	_, err := rand.Read(buf[:])
	if err != nil {
		panic(err.Error())
	}
	return hex.EncodeToString(buf[:])
}

////////////////////////////////////////////////////////////////////////////////

func expectBool(t *testing.T, actual, expected bool) {
	t.Helper()
	if actual != expected {
		t.Errorf("expected value %#v, got %#v instead\n", expected, actual)
	}
}

func expectFloat64(t *testing.T, actual, expected float64) {
	t.Helper()
	if math.Abs((actual-expected)/expected) > 1e-8 {
		t.Errorf("expected value %g, got %g instead\n", expected, actual)
	}
}

func expectInt(t *testing.T, actual, expected int) {
	t.Helper()
	if actual != expected {
		t.Errorf("expected value %d, got %d instead\n", expected, actual)
	}
}

func expectInt64(t *testing.T, actual, expected int64) {
	t.Helper()
	if actual != expected {
		t.Errorf("expected value %d, got %d instead\n", expected, actual)
	}
}

func expectUint64(t *testing.T, actual, expected uint64) {
	t.Helper()
	if actual != expected {
		t.Errorf("expected value %d, got %d instead\n", expected, actual)
	}
}

func expectString(t *testing.T, actual, expected string) {
	t.Helper()
	if actual != expected {
		t.Errorf("expected value %q, got %q instead\n", expected, actual)
	}
}

func expectError(t *testing.T, actual error, expected string) (ok bool) {
	t.Helper()
	if actual == nil {
		t.Errorf("expected error %q, got no error\n", expected)
		return false
	}
	if expected != actual.Error() {
		t.Errorf("expected error %q, got %q instead\n", expected, actual.Error())
		return false
	}
	return true
}

func expectSuccess(t *testing.T, actual error) (ok bool) {
	t.Helper()
	if actual != nil {
		t.Errorf("expected success, got error %q instead\n", actual.Error())
		return false
	}
	return true
}

func expectHeaders(t *testing.T, actual map[string]string, expected map[string]string) {
	t.Helper()
	reported := make(map[string]bool)

	for k, av := range actual {
		ev, exists := expected[k]
		if !exists {
			ev = "<not set>"
		}
		if av != ev {
			t.Errorf(`expected "%s: %s", got "%s: %s" instead`, k, ev, k, av)
			reported[k] = true
		}
	}

	for k, ev := range expected {
		av, exists := actual[k]
		if !exists {
			av = "<not set>"
		}
		if av != ev && !reported[k] {
			t.Errorf(`expected "%s: %s", got "%s: %s" instead`, k, ev, k, av)
		}
	}
}
