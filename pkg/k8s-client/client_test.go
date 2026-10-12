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

package k8sclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// This verifies the real client HTTP payload, not API-server precondition validation.
func TestSetAnnotationsOnPodPreservesPreconditions(t *testing.T) {
	type request struct {
		method, path, contentType string
		body                      map[string]interface{}
		err                       error
	}
	requests := make(chan request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&body)
		requests <- request{r.Method, r.URL.Path, r.Header.Get("Content-Type"), body, err}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"pod","namespace":"ns","uid":"original"}}`))
	}))
	defer server.Close()
	clientset, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	c := &client{clientset: clientset}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns", UID: "original", ResourceVersion: "7", Annotations: map[string]string{"unrelated": "old"}}}
	if err := c.SetAnnotationsOnPod(pod, map[string]string{"managed": "new"}); err != nil {
		t.Fatal(err)
	}
	got := <-requests
	want := map[string]interface{}{"metadata": map[string]interface{}{"uid": "original", "resourceVersion": "7", "annotations": map[string]interface{}{"managed": "new"}}}
	if got.err != nil || got.method != http.MethodPatch || got.path != "/api/v1/namespaces/ns/pods/pod" || got.contentType != "application/merge-patch+json" || !reflect.DeepEqual(got.body, want) {
		t.Fatalf("unexpected annotation request: %+v; want body %+v", got, want)
	}
}

func TestSetAnnotationsOnPodRequiresPreconditions(t *testing.T) {
	c := &client{}
	for _, pod := range []*corev1.Pod{nil, {ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns", ResourceVersion: "7"}}, {ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns", UID: "original"}}} {
		if err := c.SetAnnotationsOnPod(pod, map[string]string{"managed": "new"}); err == nil {
			t.Fatal("missing Pod precondition accepted")
		}
	}
}
