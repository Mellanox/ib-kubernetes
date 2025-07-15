package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"

	"github.com/rs/zerolog/log"
)

type Client interface {
	Get(url string, expectedStatusCode int) ([]byte, error)
	Post(url string, expectedStatusCode int, body []byte) ([]byte, error)
}

type BasicAuth struct {
	Username string
	Password string
}

type client struct {
	basicAuth  *BasicAuth
	httpClient *http.Client
}

func NewClient(isSecure bool, basicAuth *BasicAuth, cert string) (Client, error) {
	log.Debug().Msgf("creating http client, isSecure %v, basicAuth %+v, cert %s", isSecure, basicAuth, cert)
	if basicAuth == nil {
		return nil, fmt.Errorf("invalid basicAuth value %v", basicAuth)
	}
	httpClient := &http.Client{Transport: http.DefaultTransport}
	if isSecure {
		if cert == "" {
			//nolint:gosec
			httpClient.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		} else {
			caCertPool := x509.NewCertPool()
			caCertPool.AppendCertsFromPEM([]byte(cert))
			//nolint:gosec
			httpClient.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: caCertPool}
		}
	}

	return &client{basicAuth: basicAuth, httpClient: httpClient}, nil
}

func (c *client) Get(url string, expectedStatusCode int) ([]byte, error) {
	log.Debug().Msgf("Http client GET: url %s, expectedStatusCode %v", url, expectedStatusCode)
	return c.executeRequest(http.MethodGet, url, expectedStatusCode, nil)
}

func (c *client) Post(url string, expectedStatusCode int, body []byte) ([]byte, error) {
	log.Debug().Msgf("Http client POST: url %s, expectedStatusCode %v, body %s", url, expectedStatusCode, string(body))
	return c.executeRequest(http.MethodPost, url, expectedStatusCode, body)
}

func (c *client) createRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request object %v", err)
	}

	req.SetBasicAuth(c.basicAuth.Username, c.basicAuth.Password)

	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	return req, nil
}

func (c *client) executeRequest(method, url string, expectedStatusCode int, body []byte) ([]byte, error) {
	req, err := c.createRequest(context.TODO(), method, url, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed request %v", err)
	}
	//nolint:errcheck
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != expectedStatusCode {
		return responseBody, fmt.Errorf("failed request with status code %v, expected status code %v: %v",
			resp.StatusCode, expectedStatusCode, string(responseBody))
	}

	return responseBody, nil
}
