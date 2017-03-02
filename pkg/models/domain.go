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
}

//DomainsTable enables table-level operations on domains.
var DomainsTable = &Table{
	Name:       "domains",
	AllFields:  []string{"id", "cluster_id", "uuid", "name"},
	makeRecord: func() Record { return &Domain{} },
}

//CreateDomain puts a new domain in the database.
func CreateDomain(kd drivers.KeystoneDomain, clusterID string) (*Domain, error) {
	d := &Domain{
		KeystoneDomain: kd,
		ClusterID:      clusterID,
	}
	return d, limes.DB.QueryRow(
		`INSERT INTO domains (cluster_id, uuid, name) VALUES ($1, $2, $3) RETURNING id`,
		d.ClusterID, d.UUID, d.Name,
	).Scan(&d.ID)
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

//Delete implements the Record interface.
func (d *Domain) Delete() error {
	_, err := limes.DB.Exec(`DELETE FROM domains WHERE id = $1`, d.ID)
	return err
}
