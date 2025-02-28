/******************************************************************************
*
*  Copyright 2025 SAP SE
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

// MailClient is an interface that provides the methods to communicate with a mail backend service.
type MailClient interface {
	// Builds the request to sent the mail content to a mail API.
	PostMail(ctx context.Context, req MailRequest) error
}

// mailClientImpl is an implmentation of MailClient.
// It builds the request to send a mail to the target mail server.
type mailClientImpl struct {
	gophercloud.ServiceClient
}

// NewMailClient returns a service client to communicate with a mail API.
func NewMailClient(provider *gophercloud.ProviderClient, endpoint string) (MailClient, error) {
	if endpoint == "" {
		return nil, errors.New("mail: service type for the endpoint needs to be set")
	}

	return mailClientImpl{
		ServiceClient: gophercloud.ServiceClient{
			ProviderClient: provider,
			Endpoint:       endpoint,
			Type:           "mailClient",
		},
	}, nil
}

// PostMail implements the method of MailClient to sent the mail content to the mail API.
func (c mailClientImpl) PostMail(ctx context.Context, req MailRequest) error {
	url := c.ServiceURL("v1", "send-email?from=limes")
	opts := gophercloud.RequestOpts{KeepResponseBody: true, OkCodes: []int{http.StatusOK}}
	resp, err := c.Post(ctx, url, req, nil, &opts)
	resp.Body.Close()
	if resp.StatusCode == http.StatusTeapot {
		return UndeliverableMailError{Inner: err}
	}
	return err
}

// UndeliverableMailError is a custom error type to define udeliverable mails.
// Used in the MailClient interface implementations.
type UndeliverableMailError struct {
	Inner error
}

// implements https://pkg.go.dev/builtin#error
func (e UndeliverableMailError) Error() string { return e.Inner.Error() }

// implements the interface implied by https://pkg.go.dev/errors
func (e UndeliverableMailError) Unwrap() error { return e.Inner }
