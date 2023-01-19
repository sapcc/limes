package limes

violations[{"msg": msg, "service": "shared", "resource": "capacity"}] {
  some i, j, k, l

  # if trying to set shared/capacity to not 0
	input.targetdomainreport.services[i].area == "shared"
  input.targetdomainreport.services[i].resources[j].name == "capacity"
	input.targetdomainreport.services[i].resources[j].quota != 0

  # then shared/things cannot be 0
  input.targetdomainreport.services[k].area == "shared"
  input.targetdomainreport.services[k].resources[l].name == "things"
	input.targetdomainreport.services[k].resources[l].quota == 0

	msg := "must allocate shared/things quota before"
}

violations[{"msg": msg, "service": "shared", "resource": "capacity"}] {
  some i, j#, k, l

  # if trying to set shared/things to not 0
	input.targetdomainreport.services[i].area == "shared"
  input.targetdomainreport.services[i].resources[j].name == "capacity"
	input.targetdomainreport.services[i].resources[j].quota != 0

  # then unshared/does-not-exist cannot be 0
  input.targetdomainreport.services[k].area == "unshared"
  input.targetdomainreport.services[k].resources[l].name == "capacity"
	input.targetdomainreport.services[k].resources[l].quota != 0

	msg := "must not allocate shared/capacity and unshared/capacity at the same time"
}
