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
	"log"
	"os"
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
	if len(args) > 0 {
		log.Printf(msg+"\n", args...)
	} else {
		log.Println(msg)
	}
}
