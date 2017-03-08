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

package db

//Domain contains a record from the `domains` table.
type Domain struct {
	ID        int64  `db:"id"`
	ClusterID string `db:"cluster_id"`
	Name      string `db:"name"`
	UUID      string `db:"uuid"`
}

//Project contains a record from the `projects` table.
type Project struct {
	ID       int64  `db:"id"`
	DomainID int64  `db:"domain_id"`
	Name     string `db:"name"`
	UUID     string `db:"uuid"`
}

//InitGorp is used by Init() to setup the ORM part of the database connection.
//It's available as an exported function because the unit tests need to call
//this while bypassing the normal Init() logic.
func InitGorp() {
	DB.AddTableWithName(Domain{}, "domains").SetKeys(true, "id")
	DB.AddTableWithName(Project{}, "projects").SetKeys(true, "id")
}
