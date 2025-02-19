/******************************************************************************
*
*  Copyright 2024 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package collector

import (
	"context"
	"errors"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
)

type MailDelivery interface {
	PostMail(ctx context.Context, req MailRequest) error
}

type MailClient struct {
	gophercloud.ServiceClient
}

type MailOpts struct {
	ServiceType string
}

func NewMailClient(client *gophercloud.ProviderClient, endpointOpts gophercloud.EndpointOpts, opts MailOpts) (*MailClient, error) {
	if opts.ServiceType == "" {
		return nil, errors.New("mail: service type for the endpoint needs to be set")
	}
	endpoint := opts.ServiceType

	return &MailClient{
		ServiceClient: gophercloud.ServiceClient{
			ProviderClient: client,
			Endpoint:       endpoint,
			Type:           opts.ServiceType,
		},
	}, nil
}

func (c MailClient) PostMail(ctx context.Context, req MailRequest) error {
	url := c.ServiceURL("v1", "send-email?from=limes")
	opts := gophercloud.RequestOpts{KeepResponseBody: true, OkCodes: []int{http.StatusOK}}
	resp, err := c.Post(ctx, url, req, nil, &opts)
	resp.Body.Close()
	return err
}
