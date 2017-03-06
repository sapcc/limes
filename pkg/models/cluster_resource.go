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

package models

//ClusterResource represents a single resource within an OpenStack cluster.
type ClusterResource struct {
	ServiceID uint64 //index into `cluster_services` table
	Name      string
	Capacity  uint64
	exists    bool
}

//ClusterResourcesTable enables table-level operations on cluster resources.
var ClusterResourcesTable = &Table{
	Name:       "cluster_resources",
	AllFields:  []string{"service_id", "name", "capacity"},
	makeRecord: func() Record { return &ClusterResource{exists: true} },
}

//Table implements the Record interface.
func (cr *ClusterResource) Table() *Table {
	return ClusterResourcesTable
}

//ScanTargets implements the Record interface.
func (cr *ClusterResource) ScanTargets() []interface{} {
	return []interface{}{
		&cr.ServiceID, &cr.Name, &cr.Capacity,
	}
}

//Save implements the record interface.
func (cr *ClusterResource) Save(db DBInterface) (err error) {
	if cr.exists {
		_, err = db.Exec(
			`UPDATE cluster_resources SET capacity = $1 WHERE service_id = $2 AND name = $3`,
			cr.Capacity, cr.ServiceID, cr.Name)
	} else {
		_, err = db.Exec(
			`INSERT INTO cluster_resources (service_id, name, capacity) VALUES ($1, $2, $3) RETURNING id`,
			cr.ServiceID, cr.Name, cr.Capacity)
		cr.exists = err == nil
	}
	return
}

//Delete implements the Record interface.
func (cr *ClusterResource) Delete(db DBInterface) error {
	_, err := db.Exec(
		`DELETE FROM cluster_resources WHERE service_id = $1 AND name = $2`,
		cr.ServiceID, cr.Name)
	return err
}
