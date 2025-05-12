// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"testing"
	"time"

	"github.com/sapcc/go-bits/easypg"

	"github.com/sapcc/limes/internal/core"
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
	easypg.AssertDBContent(t, s.DB.Db, "fixtures/checkconsistency-pre.sql")

	// check that CheckConsistency() is satisfied with the
	// {domain,project}_services created by ScanDomains(), but adds
	// cluster_services entries
	s.Clock.StepBy(time.Hour)
	err = consistencyJob.ProcessOne(s.Ctx)
	if err != nil {
		t.Error(err)
	}
	easypg.AssertDBContent(t, s.DB.Db, "fixtures/checkconsistency0.sql")

	// remove some *_services entries
	_, err = s.DB.Exec(`DELETE FROM cluster_services WHERE type = $1`, "shared")
	if err != nil {
		t.Error(err)
	}
	_, err = s.DB.Exec(`DELETE FROM project_services WHERE type = $1`, "shared")
	if err != nil {
		t.Error(err)
	}
	// add some useless *_services entries
	err = s.DB.Insert(&db.ClusterService{Type: "whatever", NextScrapeAt: s.Clock.Now()})
	if err != nil {
		t.Error(err)
	}
	err = s.DB.Insert(&db.ProjectService{
		ProjectID:         1,
		Type:              "whatever",
		NextScrapeAt:      time.Unix(0, 0).UTC(),
		RatesNextScrapeAt: time.Unix(0, 0).UTC(),
	})
	if err != nil {
		t.Error(err)
	}
	easypg.AssertDBContent(t, s.DB.Db, "fixtures/checkconsistency1.sql")

	// check that CheckConsistency() brings everything back into a nice state
	// the "whatever" service will remain but is ignored by the consistency service,
	// as that one is using the c.Cluster configuration.
	s.Clock.StepBy(time.Hour)
	err = consistencyJob.ProcessOne(s.Ctx)
	if err != nil {
		t.Error(err)
	}
	easypg.AssertDBContent(t, s.DB.Db, "fixtures/checkconsistency2.sql")

	// now we add the "whatever" service to the configuration, which will change the state of
	// the DB after running the job again
	s.Cluster.LiquidConnections["whatever"] = &core.LiquidConnection{}
	s.Clock.StepBy(time.Hour)
	err = consistencyJob.ProcessOne(s.Ctx)
	if err != nil {
		t.Error(err)
	}
	easypg.AssertDBContent(t, s.DB.Db, "fixtures/checkconsistency3.sql")
}
