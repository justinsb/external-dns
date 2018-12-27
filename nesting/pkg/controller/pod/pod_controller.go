/*
Copyright 2018 The Kubernetes authors.

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

package pod

import (
	"context"
	"fmt"

	dns "github.com/kubernetes-incubator/external-dns/endpoint"
	"github.com/kubernetes-incubator/external-dns/nesting/pkg/kops"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	log "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// Add creates a new Pod Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) *ReconcilePod {
	return &ReconcilePod{Client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r *ReconcilePod) error {
	log := log.Log

	// Create a new controller
	c, err := controller.New("pod-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// TODO: This is evil - we are using the Name as the annotation value

	// Note that this runs on both the old & new objects
	mapPodToAnnotations := handler.ToRequestsFunc(
		func(a handler.MapObject) []reconcile.Request {
			pod := a.Object.(*corev1.Pod)

			affected := make(map[types.NamespacedName]bool)
			for _, name := range kops.SplitAnnotation(pod.Annotations[kops.AnnotationNameDNSExternal]) {
				affected[types.NamespacedName{Namespace: pod.Namespace, Name: name}] = true
			}

			for _, name := range kops.SplitAnnotation(pod.Annotations[kops.AnnotationNameDNSInternal]) {
				affected[types.NamespacedName{Namespace: pod.Namespace, Name: name}] = true
			}

			log.Info("pod updated, refreshing endpoints", "pod", pod.Namespace+"/"+pod.Name, "endpointCount", len(affected))
			if len(affected) == 0 {
				return nil
			}

			var requests []reconcile.Request
			for k := range affected {
				requests = append(requests, reconcile.Request{NamespacedName: k})
			}
			return requests
		})

	// TODO: Filter pod events to only register annotation changes?
	err = c.Watch(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestsFromMapFunc{
		ToRequests: mapPodToAnnotations,
	} /*, nodePredicate*/)
	if err != nil {
		return err
	}

	/*
		// Watch the endpoints created by us
		err = c.Watch(&source.Kind{Type: &dns.DNSEndpoint{}}, &handler.EnqueueRequestForOwner{
			IsController: true,
			OwnerType:    &corev1.Pod{},
		})
		if err != nil {
			return err
		}
	*/

	return nil
}

var _ reconcile.Reconciler = &ReconcilePod{}

// ReconcilePod reconciles a Pod object
type ReconcilePod struct {
	client.Client
	scheme *runtime.Scheme
}

// Reconcile reads that state of the cluster for a Pod object and makes changes based on the state read
// and what is in the Pod.Spec
// TODO(user): Modify this Reconcile function to implement your Controller logic.  The scaffolding writes
// a Deployment as an example
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods/status,verbs=get;update;patch
func (r *ReconcilePod) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	log := log.Log
	if err := r.reconcile(request); err != nil {
		log.Error(err, "error reconciling", "request", request)
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (r *ReconcilePod) reconcile(request reconcile.Request) error {
	ctx := context.TODO()

	// Fetch the pods
	pods := &corev1.PodList{}
	if err := r.List(ctx, &client.ListOptions{Namespace: request.Namespace}, pods); err != nil {
		return fmt.Errorf("error retrieving pods: %v", err)
	}

	// Fetch the DNS endpoints
	dnsEndpoints := &dns.DNSEndpointList{}
	if err := r.List(ctx, &client.ListOptions{Namespace: request.Namespace}, dnsEndpoints); err != nil {
		return fmt.Errorf("error retrieving DNSEndpoints: %v", err)
	}

	// TODO: Set the owner - can we have multiple owners??

	internalNames := make(map[string]bool)
	externalNames := make(map[string]bool)

	for i := range pods.Items {
		pod := &pods.Items[i]
		for _, name := range kops.SplitAnnotation(pod.Annotations[kops.AnnotationNameDNSExternal]) {
			externalNames[name] = true
		}

		for _, name := range kops.SplitAnnotation(pod.Annotations[kops.AnnotationNameDNSInternal]) {
			internalNames[name] = true

		}
	}

	internalEndpoints := make(map[string]*dns.DNSEndpoint)
	externalEndpoints := make(map[string]*dns.DNSEndpoint)
	dnsEndpointNames := make(map[string]bool)

	for i := range dnsEndpoints.Items {
		dnsEndpoint := &dnsEndpoints.Items[i]

		dnsEndpointNames[dnsEndpoint.Name] = true
		if dnsEndpoint.Annotations[kops.AnnotationNameDNSExternal] != "" {
			externalEndpoints[dnsEndpoint.Annotations[kops.AnnotationNameDNSExternal]] = dnsEndpoint
		}
		if dnsEndpoint.Annotations[kops.AnnotationNameDNSInternal] != "" {
			internalEndpoints[dnsEndpoint.Annotations[kops.AnnotationNameDNSInternal]] = dnsEndpoint
		}
	}

	// Create missing DNSEndpoints for external names
	for k := range externalNames {
		if externalEndpoints[k] == nil {
			o := &dns.DNSEndpoint{}
			o.Namespace = request.Namespace
			o.Name = generateName(k, dnsEndpointNames)
			o.Annotations = make(map[string]string)
			o.Annotations[kops.AnnotationNameDNSExternal] = k

			if err := r.Create(ctx, o); err != nil {
				return fmt.Errorf("error creating DNSEndpoint: %v", err)
			}
		}
	}

	// Create missing DNSEndpoints for internal names
	for k := range internalNames {
		if internalEndpoints[k] == nil {
			o := &dns.DNSEndpoint{}
			o.Namespace = request.Namespace
			o.Name = generateName(k, dnsEndpointNames)
			o.Annotations = make(map[string]string)
			o.Annotations[kops.AnnotationNameDNSInternal] = k

			if err := r.Create(ctx, o); err != nil {
				return fmt.Errorf("error creating DNSEndpoint: %v", err)
			}
		}
	}

	// Remove DNSEndpoints that no longer are referenced
	for k, o := range externalEndpoints {
		if !externalNames[k] {
			if err := r.Delete(ctx, o); err != nil {
				return fmt.Errorf("error deleting DNSEndpoint: %v", err)
			}
		}
	}
	for k, o := range internalEndpoints {
		if !internalNames[k] {
			if err := r.Delete(ctx, o); err != nil {
				return fmt.Errorf("error deleting DNSEndpoint: %v", err)
			}
		}
	}

	return nil
}

func generateName(dnsName string, used map[string]bool) string {
	k := dnsName
	if !used[k] {
		return k
	}

	i := 1
	for {
		s := fmt.Sprintf("%s-%d", k, i)
		if !used[s] {
			return s
		}
		i++
	}
}
