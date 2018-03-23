///usr/bin/env go run "$0" "$@"; exit $!

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

package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"text/template"
)

func main() {
	input, err := ioutil.ReadAll(os.Stdin)
	failIfError(err)

	sections := strings.SplitN(string(input), "\n---\n", 2)
	if len(sections) != 2 {
		fail("missing separator between data and template")
	}
	dataStr, templateStr := sections[0], sections[1]

	data := make(map[string]interface{})
	failIfError(json.Unmarshal([]byte(dataStr), &data))

	tmpl, err := template.New("tmpl").Parse(templateStr)
	failIfError(err)

	failIfError(tmpl.Execute(os.Stdout, data))
}

func failIfError(err error) {
	if err != nil {
		fail(err.Error())
	}
}

func fail(msg string, args ...interface{}) {
	if len(args) > 0 {
		msg = fmt.Sprintf(msg, args...)
	}
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
