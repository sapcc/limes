// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"testing"
	"time"

	"github.com/sapcc/go-bits/easypg"

	"github.com/sapcc/limes/internal/db"
)

func Test_Consistency(t *testing.T) {
	s, cluster := keystoneTestCluster(t)
	_ = cluster
	c := getCollector(t, s)
	consistencyJob := c.CheckConsistencyJob(s.Registry)

	// run ScanDomains once to establish a baseline
	_, err := c.ScanDomains(s.Ctx, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains failed: %v", err)
	}
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualToFile("fixtures/checkconsistency0.sql")

	// check that CheckConsistency() is satisfied with the
	// {domain,project}_services created by ScanDomains(), but adds
	// services entries
	s.Clock.StepBy(time.Hour)
	err = consistencyJob.ProcessOne(s.Ctx)
	if err != nil {
		t.Error(err)
	}
	tr.DBChanges().AssertEmpty()

	// remove some project_services entries
	_, err = s.DB.Exec(`DELETE FROM project_services WHERE id = $1`, 2)
	if err != nil {
		t.Error(err)
	}
	// add some useless *_services entries
	err = s.DB.Insert(&db.Service{Type: "whatever", NextScrapeAt: s.Clock.Now()})
	if err != nil {
		t.Error(err)
	}
	err = s.DB.Insert(&db.ProjectService{
		ProjectID:    1,
		ServiceID:    3,
		NextScrapeAt: time.Unix(0, 0).UTC(),
	})
	if err != nil {
		t.Error(err)
	}
	tr.DBChanges().Ignore()

	// check that CheckConsistency() brings everything back into a nice state
	s.Clock.StepBy(time.Hour)
	err = consistencyJob.ProcessOne(s.Ctx)
	if err != nil {
		t.Error(err)
	}
	tr.DBChanges().AssertEqualf(`
		DELETE FROM project_services WHERE id = 7 AND project_id = 1 AND service_id = 3;
		INSERT INTO project_services (id, project_id, service_id, stale, next_scrape_at) VALUES (8, 1, 2, TRUE, %d);
		DELETE FROM services WHERE id = 3 AND type = 'whatever' AND liquid_version = 0;
	`,
		s.Clock.Now().Unix(),
	)
}
