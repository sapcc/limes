/*******************************************************************************
*
* Copyright 2022 SAP SE
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

package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/respondwith"
)

// JSONListStream writes a JSON response containing a single array in the form
//
//	{ "things": [ thing1, thing2, ..., thingN ] }
//
// without needing to hold the entire list of things in memory at once. This is
// especially necessary for large project reports because the JSON can grow to
// several 100 MiB for large domains and high detail settings, which would lead
// to OOM on the API process in a cgroup-controlled deployment if we try to
// hold it all in memory.
//
// We delay the opening `{"things":[` until we receive the first item, so that
// errors can be logged as a 5xx response if necessary. Upon getting the first
// report, we commit to the response being 200 and print reports as they come
// in. If we get to the end, we just need to write the trailing `]}` to
// complete the response.
type JSONListStream[T any] struct {
	OuterFieldName string
	Request        *http.Request
	ResponseWriter http.ResponseWriter

	// `w != nil` indicates that we have started writing a response
	w *bufio.Writer
}

func NewJSONListStream[T any](w http.ResponseWriter, r *http.Request, outerFieldName string) *JSONListStream[T] {
	return &JSONListStream[T]{
		OuterFieldName: outerFieldName,
		Request:        r,
		ResponseWriter: w,
	}
}

// WriteItem can be called as many times as necessary to append items to the
// JSON document.
func (s *JSONListStream[T]) WriteItem(item T) error {
	if s.w == nil {
		// output has not started yet -> write opener before first item
		s.ResponseWriter.Header().Set("Content-Type", "application/json")
		s.ResponseWriter.WriteHeader(http.StatusOK)
		s.w = bufio.NewWriter(s.ResponseWriter)
		opener := fmt.Sprintf(`{"%s":[`, s.OuterFieldName)
		_, err := s.w.Write([]byte(opener))
		if err != nil {
			return err
		}
	} else {
		// output has already started -> write commas between items
		_, err := s.w.Write([]byte(`,`))
		if err != nil {
			return err
		}
	}

	// write the item
	return json.NewEncoder(s.w).Encode(item)
}

// FinalizeDocument must be called once after all items have been written, to
// properly finalize the JSON document.
func (s *JSONListStream[T]) FinalizeDocument(err error) {
	if err == nil {
		if s.w != nil {
			// write closer after last report
			_, err = s.w.Write([]byte(`]}`))
			if err == nil {
				err = s.w.Flush()
			}
		} else {
			// this branch is reached when there are no items in the list and therefore
			// Write() was never called, so we need to write the entire document now
			respondwith.JSON(s.ResponseWriter, http.StatusOK, map[string]any{s.OuterFieldName: []any{}})
			return
		}
	}
	if err != nil {
		if s.w != nil {
			// deliberately destroy the ongoing JSON document to make it clear to the
			// client that an error occurred
			fmt.Fprintf(s.ResponseWriter, "\naborting because of error: %s\n", err.Error())
			// usually we don't need to log errors in handlers because the
			// logg.Middleware does it for us, but since this is a 200 response, we
			// need to do it ourselves
			logg.Error("late error during GET %s: %s", s.Request.URL.String(), err.Error())
		} else {
			// the callback was never called, so we can properly report the error to the client
			respondwith.ErrorText(s.ResponseWriter, err)
		}
	}
}
