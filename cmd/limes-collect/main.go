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

	"github.com/sapcc/limes/pkg/collectors"
	"github.com/sapcc/limes/pkg/drivers"
	"github.com/sapcc/limes/pkg/limes"
)

func main() {
	//expect two argument (config file name and cluster ID)
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <config-file> <cluster-id>\n", os.Args[0])
		os.Exit(1)
	}
	config := limes.NewConfiguration(os.Args[1])

	err := limes.InitDatabase(config)
	if err != nil {
		limes.Log(limes.LogFatal, err.Error())
	}

	cluster, err := limes.NewCluster(config, os.Args[2])
	if err != nil {
		limes.Log(limes.LogFatal, err.Error())
	}
	driver := drivers.NewDriver(cluster)

	//TODO: replace by actual implementation
	result, err := collectors.ScanDomains(driver, cluster.ID, collectors.ScanDomainsOpts{ScanAllProjects: true})
	fmt.Printf("RESULT: %#v\n", result)
	if err != nil {
		limes.Log(limes.LogFatal, err.Error())
	}
}
