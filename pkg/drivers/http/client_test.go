// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestHTTP(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "HTTP Driver Suite")
}

var _ = Describe("Backend request outcomes", func() {
	It("reports a truncated success response as an uncertain request failure", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "10")
			_, _ = w.Write([]byte("short"))
		}))
		defer server.Close()
		api, err := NewClient(false, &BasicAuth{Username: "test", Password: "test"}, "")
		Expect(err).NotTo(HaveOccurred())
		_, err = api.Post(server.URL, http.StatusOK, []byte(`{}`))
		Expect(err).To(MatchError(ContainSubstring("failed to read response")))
	})
	It("bounds stalled backend requests", func() {
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
			<-req.Context().Done()
		}))
		defer server.Close()
		api, err := NewClient(false, &BasicAuth{Username: "test", Password: "test"}, "")
		Expect(err).NotTo(HaveOccurred())
		// Exercise the production timeout mechanism without a 30-second test delay.
		api.(*client).httpClient.Timeout = 10 * time.Millisecond
		_, err = api.Get(server.URL, http.StatusOK)
		Expect(err).To(HaveOccurred())
	})
})
