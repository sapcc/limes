/*******************************************************************************
*
* Copyright 2017 SAP SE
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

package util

import (
	"fmt"
	"log"
	"os"
	"strings"
)

var isDebug = os.Getenv("LIMES_DEBUG") == "1"

func init() {
	log.SetOutput(os.Stdout)
}

//LogFatal logs a fatal error and terminates the program.
func LogFatal(msg string, args ...interface{}) {
	doLog("FATAL: "+msg, args)
	os.Exit(1)
}

//LogError logs a non-fatal error.
func LogError(msg string, args ...interface{}) {
	doLog("ERROR: "+msg, args)
}

//LogInfo logs an informational message.
func LogInfo(msg string, args ...interface{}) {
	doLog("INFO: "+msg, args)
}

//LogDebug logs a debug message if debug logging is enabled.
func LogDebug(msg string, args ...interface{}) {
	if isDebug {
		doLog("DEBUG: "+msg, args)
	}
}

func doLog(msg string, args []interface{}) {
	msg = strings.TrimPrefix(msg, "\n")
	msg = strings.Replace(msg, "\n", "\\n", -1) //avoid multiline log messages
	if len(args) > 0 {
		log.Printf(msg+"\n", args...)
	} else {
		log.Println(msg)
	}
}

//AuditTrail is a list of log lines with log level AUDIT. It has a separate
//interface from the rest of the logging to allow to withhold the logging until
//DB changes are committed.
type AuditTrail struct {
	lines []string
}

//Add adds a line to the audit trail.
func (t *AuditTrail) Add(msg string, args ...interface{}) {
	t.lines = append(t.lines, fmt.Sprintf(msg, args...))
}

//Commit sends the whole audit trail into the log. Call this after tx.Commit().
func (t *AuditTrail) Commit() {
	for _, line := range t.lines {
		doLog("AUDIT: "+line, nil)
	}
	t.lines = nil //do not log these lines again
}
