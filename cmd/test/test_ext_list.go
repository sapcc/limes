package main

import (
	"fmt"
	"os"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/common/extensions"
)

type neutronResourceMetadata struct {
	LimesName   string
	NeutronName string
	Extension   string
}

var neutronResourceMeta = []neutronResourceMetadata{
	{
		LimesName:   "networks",
		NeutronName: "network",
		Extension:   "",
	},
	{
		LimesName:   "subnets",
		NeutronName: "subnet",
		Extension:   "",
	},
	{
		LimesName:   "subnet_pools",
		NeutronName: "subnetpool",
		Extension:   "",
	},
	{
		LimesName:   "floating_ips",
		NeutronName: "floatingip",
		Extension:   "",
	},
	{
		LimesName:   "routers",
		NeutronName: "router",
		Extension:   "",
	},
	{
		LimesName:   "ports",
		NeutronName: "port",
		Extension:   "",
	},
	{
		LimesName:   "security_groups",
		NeutronName: "security_group",
		Extension:   "",
	},
	{
		LimesName:   "security_group_rules",
		NeutronName: "security_group_rule",
		Extension:   "",
	},
	{
		LimesName:   "rbac_policies",
		NeutronName: "rbac_policy",
		Extension:   "lbaasv2",
	},
	{
		LimesName:   "bgpvpns",
		NeutronName: "bgpvpn",
		Extension:   "bgpvpn",
	},
}

func main() {

	ao, err := openstack.AuthOptionsFromEnv()
	if err != nil {
		fmt.Printf("ERROR getting auth options: %+v\n", err)
		os.Exit(1)
	}

	provider, err := openstack.AuthenticatedClient(ao)
	if err != nil {
		fmt.Printf("ERROR authenticating client: %+v\n", err)
		os.Exit(1)
	}

	networkClient, err := openstack.NewNetworkV2(provider, gophercloud.EndpointOpts{
		Region: os.Getenv("OS_REGION_NAME"),
	})
	if err != nil {
		fmt.Printf("ERROR getting neutron client: %+v\n", err)
		os.Exit(1)
	}

	ext := map[string]bool{}
	for _, resource := range neutronResourceMeta {
		if resource.Extension == "" {
			continue
		}
		r := extensions.Get(networkClient, resource.Extension)
		switch r.Result.Err.(type) {
		case gophercloud.ErrDefault404:
			ext[resource.Extension] = false
		case nil:
			ext[resource.Extension] = true
		default:
			fmt.Printf("cannot check for %q support in Neutron: %s", resource.Extension, r.Result.Err.Error())
		}
	}

	fmt.Printf("RES: %+v", ext)

	fmt.Printf("CHACKE bgpvpn: %v", isExtensionEnabled("bgpvpn", ext))
	fmt.Printf("CHACKE kokoko: %v", isExtensionEnabled("kokoko", ext))
	fmt.Printf("CHACKE lbaasv2: %v", isExtensionEnabled("lbaasv2", ext))
}

func isExtensionEnabled(extensionAlias string, list map[string]bool) bool {
	enabled, ok := list[extensionAlias]
	return ok && enabled
}
