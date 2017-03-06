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

import (
	"github.com/sapcc/limes/pkg/drivers"
	"github.com/sapcc/limes/pkg/limes"
)

//Domain represents a Keystone domain in Limes' database.
type Domain struct {
	ID                     int64
	drivers.KeystoneDomain //Name and UUID
	ClusterID              string
	exists                 bool
}

//DomainsTable enables table-level operations on domains.
var DomainsTable = &Table{
	Name:       "domains",
	AllFields:  []string{"id", "cluster_id", "uuid", "name"},
	makeRecord: func() Record { return &Domain{exists: true} },
}

//Table implements the Record interface.
func (d *Domain) Table() *Table {
	return DomainsTable
}

//ScanTargets implements the Record interface.
func (d *Domain) ScanTargets() []interface{} {
	return []interface{}{
		&d.ID, &d.ClusterID, &d.UUID, &d.Name,
	}
}

//Save implements the Record interface.
func (d *Domain) Save(db DBInterface) (err error) {
	if d.exists {
		//NOTE: only name may be updated
		_, err = db.Exec(`UPDATE domains SET name = $1 WHERE id = $2`, d.Name, d.ID)
	} else {
		err = limes.DB.QueryRow(
			`INSERT INTO domains (cluster_id, uuid, name) VALUES ($1, $2, $3) RETURNING id`,
			d.ClusterID, d.UUID, d.Name,
		).Scan(&d.ID)
		d.exists = err == nil
	}
	return
}

//Delete implements the Record interface.
func (d *Domain) Delete(db DBInterface) error {
	_, err := db.Exec(`DELETE FROM domains WHERE id = $1`, d.ID)
	return err
}
