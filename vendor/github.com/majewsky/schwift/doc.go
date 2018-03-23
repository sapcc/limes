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

/*

Package schwift is a client library for OpenStack Swift
(https://github.com/openstack/swift, https://openstack.org).

TODO update doc for changed auth workflow

It uses Gophercloud (https://github.com/gophercloud/gophercloud) for
authentication, so you usually start by obtaining a gophercloud.ServiceClient
for Swift like so:

	authOptions, err := openstack.AuthOptionsFromEnv() // or build a gophercloud.AuthOptions instance yourself
	provider, err := openstack.AuthenticatedClient(authOptions)
	client, err := openstack.NewObjectStorageV1(provider, gophercloud.EndpointOpts{})

Or, if you use Swift's built-in authentication instead of Keystone:

	provider, err := openstack.NewClient("http://swift.example.com:8080")
	client, err := swauth.NewObjectStorageV1(provider, swauth.AuthOpts {
		User: "project:user",
		Key:  "password",
	})

Then, in both cases, you use Wrap() from the subpackage gopherschwift to obtain
a schwift.Account instance, from which point you have access to all of
schwift's API.

Caching

When a GET or HEAD request is sent by an Account, Container or Object instance,
the headers associated with that thing will be stored in that instance and not
retrieved again.

	obj := account.Container("foo").Object("bar")

	hdr, err := obj.Headers() //sends HTTP request "HEAD <storage-url>/foo/bar"
	...
	hdr, err = obj.Headers()  //returns cached values immediately

If this behavior is not desired, the Invalidate() method can be used to clear
caches on any Account, Container or Object instance.

*/
package schwift
