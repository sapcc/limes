// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"errors"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
)

// MailClient provides methods for communicating with a mail backend service.
type MailClient interface {
	// Sends a mail using the mail API.
	PostMail(ctx context.Context, req MailRequest) error
}

// mailClientImpl is an implementation of MailClient.
//
// It sends mails using the OpenStack service type "mailClient"
// as specified in the Limes documentation.
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
