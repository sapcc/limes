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

package main

import (
	"fmt"
	"os"
	"time"

	"github.com/sapcc/limes/pkg/collectors"
	"github.com/sapcc/limes/pkg/drivers"
	"github.com/sapcc/limes/pkg/limes"
)

//SharedState contains all the stuff that the main thread shares with the workers.
type SharedState struct {
	Cluster *limes.Cluster
	Driver  drivers.Driver
}

var discoverInterval = 3 * time.Minute

func main() {
	//expect two arguments (config file name and cluster ID)
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <config-file> <cluster-id>\n", os.Args[0])
		os.Exit(1)
	}
	config := limes.NewConfiguration(os.Args[1])

	err := limes.InitDatabase(config)
	if err != nil {
		limes.Log(limes.LogFatal, err.Error())
	}

	//initialize shared state
	var state SharedState
	state.Cluster, err = limes.NewCluster(config, os.Args[2])
	if err != nil {
		limes.Log(limes.LogFatal, err.Error())
	}
	state.Driver = drivers.NewDriver(state.Cluster)

	//start threads (NOTE: Many people use a pair of sync.WaitGroup and stop
	//channel to shutdown threads in a controlled manner. I decided against that
	//for now, and instead construct worker threads in such a way that they can
	//be terminated at any time without leaving the system in an inconsistent
	//state, mostly through usage of DB transactions.)

	//TODO

	//since we don't have to manage thread lifetime in the main thread, I use it to check Keystone regularly
	for {
		_, err := collectors.ScanDomains(state.Driver, state.Cluster.ID, collectors.ScanDomainsOpts{ScanAllProjects: true})
		if err != nil {
			limes.Log(limes.LogError, err.Error())
		}
		time.Sleep(discoverInterval)
	}
}
