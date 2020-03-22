package http

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/golang/glog"
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

func NewClient(isSecure bool, basicAuth *BasicAuth, cert string, verifyHostName bool) (Client, error) {
	glog.V(3).Info("NewClient():")
	if basicAuth == nil {
		return nil, fmt.Errorf("Invalid basicAuth value %v", basicAuth)
	}
	httpClient := &http.Client{Transport: http.DefaultTransport}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM([]byte(cert))
	if isSecure {
		/*
		   #nosec
		   Ignore gosec lint check so it won't complain on using InsecureSkipVerify
		   We don't  skip verivy, we check the cert in VerifyPeerCertificate without
		   the hostname
		*/
		if !verifyHostName {
			httpClient.Transport.(*http.Transport).TLSClientConfig = &tls.Config{
				RootCAs: caCertPool,
				// Not actually skipping, we check the cert in VerifyPeerCertificate
				InsecureSkipVerify: true,
				VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
					// Code copy/pasted and adapted from
					// https://github.com/golang/go/blob/81555cb4f3521b53f9de4ce15f64b77cc9df61b9/src/crypto/tls/handshake_client.go#L327-L344, but adapted to skip the hostname verification.
					// See https://github.com/golang/go/issues/21971#issuecomment-412836078.

					// If this is the first handshake on a connection, process and
					// (optionally) verify the server's certificates.
					certs := make([]*x509.Certificate, len(rawCerts))
					for i, asn1Data := range rawCerts {
						cert, err := x509.ParseCertificate(asn1Data)
						if err != nil {
							return err
						}
						certs[i] = cert
					}

					opts := x509.VerifyOptions{
						Roots:         caCertPool,
						CurrentTime:   time.Now(),
						DNSName:       "", // <- skip hostname verification
						Intermediates: x509.NewCertPool(),
					}

					for i, cert := range certs {
						if i == 0 {
							continue
						}
						opts.Intermediates.AddCert(cert)
					}
					_, err := certs[0].Verify(opts)
					return err
				},
			}
		} else {
			httpClient.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: caCertPool}
		}
	}

	return &client{basicAuth: basicAuth, httpClient: httpClient}, nil
}

func (c *client) Get(url string, expectedStatusCode int) ([]byte, error) {
	glog.V(3).Infof("Get(): url %s, expectedStatusCode %v", url, expectedStatusCode)
	return c.executeRequest(http.MethodGet, url, expectedStatusCode, nil)
}

func (c *client) Post(url string, expectedStatusCode int, body []byte) ([]byte, error) {
	glog.V(3).Infof("Post(): url %s, expectedStatusCode %v, body %s", url, expectedStatusCode, string(body))
	return c.executeRequest(http.MethodPost, url, expectedStatusCode, body)
}

func (c *client) createRequest(method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request object %v", err)
	}

	req.SetBasicAuth(c.basicAuth.Username, c.basicAuth.Password)

	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	return req, nil
}

func (c *client) executeRequest(method, url string, expectedStatusCode int, body []byte) ([]byte, error) {
	req, err := c.createRequest(method, url, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed request %v", err)
	}
	defer resp.Body.Close()
	responseBody, _ := ioutil.ReadAll(resp.Body)
	if resp.StatusCode != expectedStatusCode {
		return responseBody, fmt.Errorf("failed request with status code %v, expected status code %v: %v",
			resp.StatusCode, expectedStatusCode, string(responseBody))
	}

	return responseBody, nil
}
