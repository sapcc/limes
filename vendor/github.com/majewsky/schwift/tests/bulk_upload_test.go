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
	"archive/tar"
	"bytes"
	"strings"
	"testing"

	"github.com/majewsky/schwift"
)

func TestBulkUploadSuccess(t *testing.T) {
	testWithContainer(t, func(c *schwift.Container) {
		obj1 := c.Object("file1")
		obj2 := c.Object("file2")

		archive := buildTarArchive(map[string][]byte{
			obj1.FullName(): []byte("hello"),
			obj2.FullName(): []byte("world"),
		})
		n, err := c.Account().BulkUpload(
			"", //upload path
			schwift.BulkUploadTar,
			bytes.NewReader(archive),
			nil,
		)
		expectInt(t, n, 2)
		expectSuccess(t, err)

		expectObjectExistence(t, obj1, true)
		expectObjectExistence(t, obj2, true)
		expectObjectContent(t, obj1, []byte("hello"))
		expectObjectContent(t, obj2, []byte("world"))
	})
}

func TestBulkUploadArchiveError(t *testing.T) {
	testWithContainer(t, func(c *schwift.Container) {
		n, err := c.Account().BulkUpload(
			c.Name(), //upload path
			schwift.BulkUploadTar,
			strings.NewReader("This is not the TAR archive you're looking for."),
			nil,
		)
		expectInt(t, n, 0)
		expectError(t, err, "400 Bad Request: Invalid Tar File: truncated header")
		bulkErr := err.(schwift.BulkError)
		expectInt(t, bulkErr.StatusCode, 400)
		expectString(t, bulkErr.OverallError, "Invalid Tar File: truncated header")
		expectInt(t, len(bulkErr.ObjectErrors), 0)
	})
}

func TestBulkUploadObjectError(t *testing.T) {
	testWithContainer(t, func(c *schwift.Container) {
		obj1 := c.Object(buildInvalidObjectName())
		obj2 := c.Object("file2")
		expectObjectExistence(t, obj2, false)

		archive := buildTarArchive(map[string][]byte{
			obj1.Name(): []byte("hello"),
			obj2.Name(): []byte("world"),
		})
		n, err := c.Account().BulkUpload(
			c.Name(), //upload path
			schwift.BulkUploadTar,
			bytes.NewReader(archive),
			nil,
		)
		expectInt(t, n, 1)
		expectError(t, err, "400 Bad Request (+1 object errors)")
		bulkErr := err.(schwift.BulkError)
		expectInt(t, len(bulkErr.ObjectErrors), 1)
		expectString(t, bulkErr.ObjectErrors[0].ContainerName, c.Name())
		expectInt(t, bulkErr.ObjectErrors[0].StatusCode, 400)
		//^ We cannot match the ObjectName (or use expectError, for that matter)
		//here because Swift truncates the object name to its max length.

		//even if some files cannot be processed, the other files shall be stored correctly
		expectObjectExistence(t, obj2, true)
		expectObjectContent(t, obj2, []byte("world"))
	})
}

func buildTarArchive(files map[string][]byte) []byte {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for fileName, contents := range files {
		err := w.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     fileName,
			Size:     int64(len(contents)),
			Mode:     0100644,
		})
		if err != nil {
			panic(err.Error())
		}
		_, err = w.Write(contents)
		if err != nil {
			panic(err.Error())
		}
	}
	err := w.Close()
	if err != nil {
		panic(err.Error())
	}
	return buf.Bytes()
}

func buildInvalidObjectName() string {
	//5000 is more than the usual max_object_name_length of 1024
	buf := make([]byte, 5000)
	for idx := range buf {
		buf[idx] = 'a'
	}
	return string(buf)
}
