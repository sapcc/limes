# example for a custom policy: only projects called "network-infra" may request quota for network/rbac_policies
package limes

# each violation must contain a message and indicate which service's/resource's quota is in violation
violations[{"msg": msg, "service": srv.type, "resource": res.name }] {
  # `input.targetprojectreport` contains the same structure that you would see in a GET response for the project in question
  # (you can also look at `input.targetdomainreport` for context about the project's domain)
  input.targetprojectreport.name != "network-infra"
  # You can also look at `input.targetdomainreport` for context about the project's domain.
  # When domain quota is being validated, only `input.targetdomainreport` will be given.

  srv := input.targetprojectreport.services[_]
  srv.type == "network"

  res := srv.resources[_]
  res.name == "rbac_policies"
  res.quota != 0

  msg := "only projects called \"network-infra\" may request quota for network/rbac_policies"
}
