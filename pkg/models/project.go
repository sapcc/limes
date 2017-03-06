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
)

//Project represents a Keystone project in Limes' database.
type Project struct {
	ID                      int64
	drivers.KeystoneProject //Name and UUID
	DomainID                int64
	exists                  bool
}

//ProjectsTable enables table-level operations on projects.
var ProjectsTable = &Table{
	Name:       "projects",
	AllFields:  []string{"id", "domain_id", "uuid", "name"},
	makeRecord: func() Record { return &Project{exists: true} },
}

//Table implements the Record interface.
func (p *Project) Table() *Table {
	return ProjectsTable
}

//ScanTargets implements the Record interface.
func (p *Project) ScanTargets() []interface{} {
	return []interface{}{
		&p.ID, &p.DomainID, &p.UUID, &p.Name,
	}
}

//Save implements the Record interface.
func (p *Project) Save(db DBInterface) (err error) {
	if p.exists {
		//NOTE: only name may be updated
		_, err = db.Exec(`UPDATE projects SET name = $1 WHERE id = $2`, p.Name, p.ID)
	} else {
		err = db.QueryRow(
			`INSERT INTO projects (domain_id, uuid, name) VALUES ($1, $2, $3) RETURNING id`,
			p.DomainID, p.UUID, p.Name,
		).Scan(&p.ID)
		p.exists = err == nil
	}
	return
}

//Delete implements the Record interface.
func (p *Project) Delete(db DBInterface) error {
	_, err := db.Exec(`DELETE FROM projects WHERE id = $1`, p.ID)
	return err
}
