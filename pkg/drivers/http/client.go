package http

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"

	"github.com/golang/glog"
)

type AuthMode string

type Client interface {
	Get(url string, expectedStatusCode int) ([]byte, error)
	Post(url string, expectedStatusCode int, body []byte) ([]byte, error)
	SetBasicAuth(auth *BasicAuth)
}

type BasicAuth struct {
	Username string
	Password string
}

type client struct {
	authMode   AuthMode
	basicAuth  *BasicAuth
	httpClient *http.Client
}

const AuthBasic AuthMode = "Basic"

func NewClient(isSecure bool, authMode AuthMode, cert string) Client {
	glog.V(3).Info("NewClient():")
	httpClient := &http.Client{Transport: http.DefaultTransport}
	if isSecure {
		if cert == "" {
			httpClient.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		} else {
			caCertPool := x509.NewCertPool()
			caCertPool.AppendCertsFromPEM([]byte(cert))
			httpClient.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: caCertPool}
		}
	}

	return &client{authMode: authMode, httpClient: httpClient}
}

func (c *client) Get(url string, expectedStatusCode int) ([]byte, error) {
	glog.V(3).Infof("Get(): url %s, expectedStatusCode %v", url, expectedStatusCode)
	return c.executeRequest(http.MethodGet, url, expectedStatusCode, nil)
}

func (c *client) Post(url string, expectedStatusCode int, body []byte) ([]byte, error) {
	glog.V(3).Infof("Post(): url %s, expectedStatusCode %v, body %s", url, expectedStatusCode, string(body))
	return c.executeRequest(http.MethodPost, url, expectedStatusCode, body)
}

func (c *client) SetBasicAuth(auth *BasicAuth) {
	c.basicAuth = auth
}

func (c *client) createRequest(method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request object %v", err)
	}

	// Set auth
	if c.authMode == AuthBasic {
		if c.basicAuth == nil {
			return nil, fmt.Errorf("basic Auth is required but not set")
		}
		req.SetBasicAuth(c.basicAuth.Username, c.basicAuth.Password)
	} else {
		err = fmt.Errorf("createRequest(): unknown authentication mode %v", c.authMode)
		glog.Error(err)
		return nil, err
	}

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
		return nil, fmt.Errorf("faied request %v", err)
	}
	defer resp.Body.Close()
	responseBody, _ := ioutil.ReadAll(resp.Body)
	if resp.StatusCode != expectedStatusCode {
		return responseBody, fmt.Errorf("failed request with status code %v, expected status code %v: %v",
			resp.StatusCode, expectedStatusCode, string(responseBody))
	}

	return responseBody, nil
}
