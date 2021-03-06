/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package source

import (
	"testing"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/pkg/api/v1"

	"github.com/kubernetes-incubator/external-dns/endpoint"
)

// TestEndpoints tests that various services generate the correct endpoints.
func TestEndpoints(t *testing.T) {
	for _, tc := range []struct {
		title       string
		namespace   string
		name        string
		annotations map[string]string
		lbs         []string
		expected    []endpoint.Endpoint
	}{
		{
			"no annotated services return no endpoints",
			"testing",
			"foo",
			map[string]string{},
			[]string{"1.2.3.4"},
			[]endpoint.Endpoint{},
		},
		{
			"annotated services return an endpoint",
			"testing",
			"foo",
			map[string]string{
				"external-dns.kubernetes.io/hostname": "foo.example.org",
			},
			[]string{"1.2.3.4"},
			[]endpoint.Endpoint{
				{DNSName: "foo.example.org", Target: "1.2.3.4"},
			},
		},
		{
			"our controller type is dns-controller",
			"testing",
			"foo",
			map[string]string{
				"external-dns.kubernetes.io/controller": "dns-controller",
				"external-dns.kubernetes.io/hostname":   "foo.example.org",
			},
			[]string{"1.2.3.4"},
			[]endpoint.Endpoint{
				{DNSName: "foo.example.org", Target: "1.2.3.4"},
			},
		},
		{
			"different controller types are ignored",
			"testing",
			"foo",
			map[string]string{
				"external-dns.kubernetes.io/controller": "some-other-tool",
				"external-dns.kubernetes.io/hostname":   "foo.example.org",
			},
			[]string{"1.2.3.4"},
			[]endpoint.Endpoint{},
		},
		{
			"services are found in all namespaces",
			"other-testing",
			"foo",
			map[string]string{
				"external-dns.kubernetes.io/hostname": "foo.example.org",
			},
			[]string{"1.2.3.4"},
			[]endpoint.Endpoint{
				{DNSName: "foo.example.org", Target: "1.2.3.4"},
			},
		},
		{
			"no external entrypoints return no endpoints",
			"testing",
			"foo",
			map[string]string{
				"external-dns.kubernetes.io/hostname": "foo.example.org",
			},
			[]string{},
			[]endpoint.Endpoint{},
		},
		{
			"multiple external entrypoints return multiple endpoints",
			"testing",
			"foo",
			map[string]string{
				"external-dns.kubernetes.io/hostname": "foo.example.org",
			},
			[]string{"1.2.3.4", "8.8.8.8"},
			[]endpoint.Endpoint{
				{DNSName: "foo.example.org", Target: "1.2.3.4"},
				{DNSName: "foo.example.org", Target: "8.8.8.8"},
			},
		},
	} {
		t.Run(tc.title, func(t *testing.T) {
			// Create a Kubernetes testing client
			kubernetes := fake.NewSimpleClientset()

			// Create a service to test against
			ingresses := []v1.LoadBalancerIngress{}
			for _, lb := range tc.lbs {
				ingresses = append(ingresses, v1.LoadBalancerIngress{IP: lb})
			}

			service := &v1.Service{
				ObjectMeta: v1.ObjectMeta{
					Namespace:   tc.namespace,
					Name:        tc.name,
					Annotations: tc.annotations,
				},
				Status: v1.ServiceStatus{
					LoadBalancer: v1.LoadBalancerStatus{
						Ingress: ingresses,
					},
				},
			}

			_, err := kubernetes.Core().Services(service.Namespace).Create(service)
			if err != nil {
				t.Fatal(err)
			}

			// Create our object under test and get the endpoints.
			client := &ServiceSource{
				Client: kubernetes,
			}

			endpoints, err := client.Endpoints()
			if err != nil {
				t.Fatal(err)
			}

			// Validate returned endpoints against desired endpoints.
			validateEndpoints(t, endpoints, tc.expected)
		})
	}
}

func BenchmarkEndpoints(b *testing.B) {
	kubernetes := fake.NewSimpleClientset()

	service := &v1.Service{
		ObjectMeta: v1.ObjectMeta{
			Namespace: "testing",
			Name:      "foo",
			Annotations: map[string]string{
				"external-dns.kubernetes.io/hostname": "foo.example.org",
			},
		},
		Status: v1.ServiceStatus{
			LoadBalancer: v1.LoadBalancerStatus{
				Ingress: []v1.LoadBalancerIngress{
					{IP: "1.2.3.4"},
					{IP: "8.8.8.8"},
				},
			},
		},
	}

	_, err := kubernetes.Core().Services(service.Namespace).Create(service)
	if err != nil {
		b.Fatal(err)
	}

	client := &ServiceSource{
		Client: kubernetes,
	}

	for i := 0; i < b.N; i++ {
		_, err := client.Endpoints()
		if err != nil {
			b.Fatal(err)
		}
	}
}

// test helper functions

func validateEndpoints(t *testing.T, endpoints, expected []endpoint.Endpoint) {
	if len(endpoints) != len(expected) {
		t.Fatalf("expected %d endpoints, got %d", len(expected), len(endpoints))
	}

	for i := range endpoints {
		validateEndpoint(t, endpoints[i], expected[i])
	}
}

func validateEndpoint(t *testing.T, endpoint, expected endpoint.Endpoint) {
	if endpoint.DNSName != expected.DNSName {
		t.Errorf("expected %s, got %s", expected.DNSName, endpoint.DNSName)
	}

	if endpoint.Target != expected.Target {
		t.Errorf("expected %s, got %s", expected.Target, endpoint.Target)
	}
}
