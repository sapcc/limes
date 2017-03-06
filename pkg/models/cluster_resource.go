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
}

//ClusterResourcesTable enables table-level operations on cluster resources.
var ClusterResourcesTable = &Table{
	Name:       "cluster_resources",
	AllFields:  []string{"service_id", "name", "capacity"},
	makeRecord: func() Record { return &ClusterResource{} },
}

//Insert writes this record into the database as a new row.
func (cr *ClusterResource) Insert(db DBInterface) error {
	_, err := db.Exec(
		`INSERT INTO cluster_resources (service_id, name, capacity) VALUES ($1, $2, $3)`,
		cr.ServiceID, cr.Name, cr.Capacity)
	return err
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

//Delete implements the Record interface.
func (cr *ClusterResource) Delete(db DBInterface) error {
	_, err := db.Exec(
		`DELETE FROM cluster_resources WHERE service_id = $1 AND name = $2`,
		cr.ServiceID, cr.Name)
	return err
}

//Update writes the values from this resource back into the DB, assuming that
//the record already exists in there.
func (cr *ClusterResource) Update(db DBInterface) error {
	_, err := db.Exec(
		`UPDATE cluster_resources SET capacity = $1 WHERE service_id = $2 AND name = $3`,
		cr.Capacity, cr.ServiceID, cr.Name)
	return err
}
