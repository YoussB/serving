// +build e2e

/*
Copyright 2021 The Knative Authors

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

package autotls

import (
	"context"
	"testing"

	"github.com/kelseyhightower/envconfig"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"knative.dev/networking/test/conformance/ingress"
	duckv1 "knative.dev/pkg/apis/duck/v1"
	"knative.dev/pkg/reconciler"
	"knative.dev/serving/pkg/apis/serving/v1alpha1"
	"knative.dev/serving/test"
	"knative.dev/serving/test/e2e"
	v1test "knative.dev/serving/test/v1"
)

func TestDomainMappingAutoTLS(t *testing.T) {
	var env config
	if err := envconfig.Process("", &env); err != nil {
		t.Fatalf("Failed to process environment variable: %v.", err)
	}

	t.Parallel()
	ctx := context.Background()

	clients := e2e.SetupWithNamespace(t, test.TLSNamespace)

	names := test.ResourceNames{
		Service: test.ObjectNameForTest(t),
		Image:   "runtime",
	}

	if len(env.TLSServiceName) != 0 {
		names.Service = env.TLSServiceName + "dm-tls"
	}

	// Clean up on test failure or interrupt.
	test.EnsureTearDown(t, clients, &names)

	// Set up initial Service.
	svc, err := v1test.CreateServiceReady(t, clients, &names)
	if err != nil {
		t.Fatalf("Failed to create initial Service %v: %v", names.Service, err)
	}

	// Using fixed hostnames can lead to conflicts when multiple tests run at
	// once, so include the svc name to avoid collisions.
	host := svc.Service.Name + ".example.com"

	if test.ServingFlags.CustomDomain != "" {
		host = svc.Service.Name + "." + test.ServingFlags.CustomDomain
	}

	// Point DomainMapping at our service.
	var dm *v1alpha1.DomainMapping
	if err := reconciler.RetryTestErrors(func(int) error {
		dm, err = clients.ServingAlphaClient.DomainMappings.Create(ctx, &v1alpha1.DomainMapping{
			ObjectMeta: metav1.ObjectMeta{
				Name:      host,
				Namespace: svc.Service.Namespace,
			},
			Spec: v1alpha1.DomainMappingSpec{
				Ref: duckv1.KReference{
					Namespace:  svc.Service.Namespace,
					Name:       svc.Service.Name,
					APIVersion: "serving.knative.dev/v1",
					Kind:       "Service",
				},
			},
		}, metav1.CreateOptions{})
		return err
	}); err != nil {
		t.Fatalf("Create(DomainMapping) = %v, expected no error", err)
	}

	t.Cleanup(func() {
		clients.ServingAlphaClient.DomainMappings.Delete(ctx, dm.Name, metav1.DeleteOptions{})
	})

	// Wait for DomainMapping to go Ready.
	if waitErr := wait.PollImmediate(test.PollInterval, test.PollTimeout, func() (bool, error) {
		state, err := clients.ServingAlphaClient.DomainMappings.Get(context.Background(), dm.Name, metav1.GetOptions{})
		if err != nil {
			return true, err
		}

		return state.IsReady(), nil
	}); waitErr != nil {
		t.Fatalf("The DomainMapping %s was not marked as Ready: %v", dm.Name, waitErr)
	}

	// The TLS info is added to the ingress after the service is created, that's
	// why we need to wait again
	err = v1test.WaitForServiceState(clients.ServingClient, names.Service, httpsReady, "HTTPSIsReady")
	if err != nil {
		t.Fatalf("Service %s did not become ready or have HTTPS URL: %v", names.Service, err)
	}

	certName := getCertificateName(t, clients, svc)
	rootCAs := createRootCAs(t, clients, svc.Route.Namespace, certName)
	httpsClient := createHTTPSClient(t, clients, svc, rootCAs)
	ingress.RuntimeRequest(context.Background(), t, httpsClient, "https://"+host)
}
