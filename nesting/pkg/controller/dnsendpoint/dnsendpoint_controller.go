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

package dnsendpoint

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	dns "github.com/kubernetes-incubator/external-dns/endpoint"
	"github.com/kubernetes-incubator/external-dns/nesting/pkg/kops"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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

/**
* USER ACTION REQUIRED: This is a scaffold file intended for the user to modify with their own Controller
* business logic.  Delete these comments after modifying this file.*
 */

// Add creates a new DNSEndpoint Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) *ReconcileDNSEndpoint {
	return &ReconcileDNSEndpoint{Client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r *ReconcileDNSEndpoint) error {
	// Create a new controller
	c, err := controller.New("dnsendpoint-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to DNSEndpoint
	err = c.Watch(&source.Kind{Type: &dns.DNSEndpoint{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	findDNSEndpointsForPod := func(pod *corev1.Pod, dest map[types.NamespacedName]bool) error {
		ctx := context.TODO()
		specExternal := pod.Annotations[kops.AnnotationNameDNSExternal]
		specInternal := pod.Annotations[kops.AnnotationNameDNSInternal]

		if specExternal == "" && specInternal == "" {
			return nil
		}

		// TODO: Filter by annotations?
		endpoints := &dns.DNSEndpointList{}
		err := r.List(ctx, &client.ListOptions{Namespace: pod.Namespace}, endpoints)
		if err != nil {
			return fmt.Errorf("error retrieving DNSEndpoints: %v", err)
		}

		if specExternal != "" {
			names := kops.SplitAnnotation(specExternal)

			for i := range endpoints.Items {
				endpoint := &endpoints.Items[i]
				v := endpoint.Annotations[kops.AnnotationNameDNSExternal]
				if v == "" {
					continue
				}

				for _, name := range names {
					if v == name {
						dest[types.NamespacedName{Namespace: endpoint.Namespace, Name: endpoint.Name}] = true
					}
				}
			}
		}

		if specInternal != "" {
			names := kops.SplitAnnotation(specInternal)

			for i := range endpoints.Items {
				endpoint := &endpoints.Items[i]
				v := endpoint.Annotations[kops.AnnotationNameDNSInternal]
				if v == "" {
					continue
				}

				for _, name := range names {
					if v == name {
						dest[types.NamespacedName{Namespace: endpoint.Namespace, Name: endpoint.Name}] = true
					}
				}
			}
		}

		return nil
	}

	// Watch for changes to Nodes (needed so we can see the external IP changes)
	mapNodeToDNSEndpoints := handler.ToRequestsFunc(
		func(a handler.MapObject) []reconcile.Request {
			ctx := context.TODO()
			log := log.Log

			nodeName := a.Meta.GetName()

			pods := &corev1.PodList{}
			// TODO: Add indexer to watch
			//err := r.List(context.TODO(), client.ForSelector("nodeName", nodeName), pods)
			err := r.List(ctx, &client.ListOptions{}, pods)
			if err != nil {
				log.Error(err, "error retrieving pods", "nodeName", nodeName)
				return nil
			}

			affected := make(map[types.NamespacedName]bool)
			for i := range pods.Items {
				pod := &pods.Items[i]
				// TODO: Move to node watcher
				if pod.Spec.NodeName != nodeName {
					continue
				}

				if err := findDNSEndpointsForPod(pod, affected); err != nil {
					log.Error(err, "error retrieving DNSEndpoints for pod", "pod", pod.Namespace+"/"+pod.Name)
				}
			}

			log.Info("node updated, refreshing endpoints", "node", nodeName, "endpointCount", len(affected))
			if len(affected) == 0 {
				return nil
			}

			var requests []reconcile.Request
			for k := range affected {
				requests = append(requests, reconcile.Request{NamespacedName: k})
			}

			return requests
		})

	// TODO: Filter node events to only register external/internal ip changes?
	err = c.Watch(&source.Kind{Type: &corev1.Node{}}, &handler.EnqueueRequestsFromMapFunc{
		ToRequests: mapNodeToDNSEndpoints,
	} /*, nodePredicate*/)
	if err != nil {
		return err
	}

	// Watch for changes to Pods
	// Note that this is run on both old & new objects
	mapPodToDNSEndpoints := handler.ToRequestsFunc(
		func(a handler.MapObject) []reconcile.Request {
			log := log.Log

			pod := a.Object.(*corev1.Pod)
			podName := types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}

			affected := make(map[types.NamespacedName]bool)

			if err := findDNSEndpointsForPod(pod, affected); err != nil {
				log.Error(err, "error retrieving DNSEndpoints for pod", "pod", podName)
			}

			log.Info("pod updated, refreshing endpoints", "pod", podName, "endpointCount", len(affected))
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
		ToRequests: mapPodToDNSEndpoints,
	} /*, nodePredicate*/)
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileDNSEndpoint{}

// ReconcileDNSEndpoint reconciles a DNSEndpoint object
type ReconcileDNSEndpoint struct {
	client.Client
	scheme *runtime.Scheme
}

// Reconcile reads that state of the cluster for a DNSEndpoint object and makes changes based on the state read
// and what is in the DNSEndpoint.Spec
// TODO(user): Modify this Reconcile function to implement your Controller logic.  The scaffolding writes
// a Deployment as an example
// +kubebuilder:rbac:groups=externaldns.k8s.io,resources=dnsendpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=externaldns.k8s.io,resources=dnsendpoints/status,verbs=get;update;patch
func (r *ReconcileDNSEndpoint) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	ctx := context.TODO()
	log := log.Log

	// Fetch the DNSEndpoint instance
	endpoint := &dns.DNSEndpoint{}
	err := r.Get(ctx, request.NamespacedName, endpoint)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("endpoint not found", "name", request.NamespacedName)
			// Not found; assume deleted and don't retry
			return reconcile.Result{}, nil
		}
		log.Error(err, "error fetching endpoint", "name", request.NamespacedName)
		return reconcile.Result{}, err
	}

	if err := r.updateDNSEndpoint(ctx, endpoint); err != nil {
		log.Error(err, "error updating DNSEndpoint", "nameendpoint", request.NamespacedName)
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func EnsureDotSuffix(s string) string {
	if !strings.HasSuffix(s, ".") {
		s = s + "."
	}
	return s
}

// updateDNSEndpoint will apply the records for the specified DNSEndpoint.
func (r *ReconcileDNSEndpoint) updateDNSEndpoint(ctx context.Context, dnsEndpoint *dns.DNSEndpoint) error {
	log := log.Log

	endpointName := types.NamespacedName{Namespace: dnsEndpoint.Namespace, Name: dnsEndpoint.Name}

	if dnsEndpoint.Annotations[kops.AnnotationNameDNSExternal] == "" && dnsEndpoint.Annotations[kops.AnnotationNameDNSInternal] == "" {
		return nil
	}

	pods := &corev1.PodList{}
	err := r.List(ctx, &client.ListOptions{Namespace: dnsEndpoint.Namespace}, pods)
	if err != nil {
		log.Error(err, "error retrieving pods", "namespace", dnsEndpoint.Namespace)
		return nil
	}

	// TODO: Should I set multiple Owner refs?

	var records []*dns.Endpoint

	for i := range pods.Items {
		pod := &pods.Items[i]

		externalName := dnsEndpoint.Annotations[kops.AnnotationNameDNSExternal]

		matchExternal := false
		if externalName != "" {
			for _, name := range kops.SplitAnnotation(pod.Annotations[kops.AnnotationNameDNSExternal]) {
				if name == externalName {
					matchExternal = true
				}
			}
		}
		if matchExternal {
			var nodeName string
			if pod.Spec.HostNetwork {
				nodeName = pod.Spec.NodeName
			} else {
				log.V(4).Info("Pod had "+kops.AnnotationNameDNSExternal+" annotation, but was not HostNetwork", "pod", pod.Name)
			}

			if nodeName != "" {
				node := &corev1.Node{}
				err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node)
				if err != nil {
					if errors.IsNotFound(err) {
						return fmt.Errorf("node %q was specified in pod but was not found", nodeName)
					}
					return fmt.Errorf("error retrieving node %q specified in pod: %v", nodeName, err)
				}

				var targets dns.Targets
				for _, a := range node.Status.Addresses {
					if a.Type != corev1.NodeExternalIP {
						continue
					}
					targets = append(targets, a.Address)
				}

				fqdn := EnsureDotSuffix(externalName)
				records = append(records, &dns.Endpoint{
					RecordType: dns.RecordTypeA,
					DNSName:    fqdn,
					Targets:    targets,
				})
			}
		}

		matchInternal := false
		internalName := dnsEndpoint.Annotations[kops.AnnotationNameDNSInternal]

		if internalName != "" {
			for _, name := range kops.SplitAnnotation(pod.Annotations[kops.AnnotationNameDNSInternal]) {
				if name == internalName {
					matchInternal = true
				}
			}
		}

		if matchInternal {
			var ips []string
			if pod.Spec.HostNetwork {
				if pod.Status.PodIP != "" {
					ips = append(ips, pod.Status.PodIP)
				}
			} else {
				log.V(4).Info("Pod had "+kops.AnnotationNameDNSInternal+" annotation, but was not HostNetwork", "pod", pod.Name)
			}

			fqdn := EnsureDotSuffix(internalName)
			records = append(records, &dns.Endpoint{
				RecordType: dns.RecordTypeA,
				DNSName:    fqdn,
				Targets:    ips,
			})
		}
	}

	expected := &dns.DNSEndpoint{}
	expected.Spec.Endpoints = records

	if !reflect.DeepEqual(dnsEndpoint.Spec, expected.Spec) {
		dnsEndpoint.Spec = expected.Spec
		log.Info("updating DNSEndpoint", "endpoint", endpointName)
		err = r.Update(ctx, dnsEndpoint)
		if err != nil {
			return fmt.Errorf("error updating endpoint: %v", err)
		}
	} else {
		log.Info("in-sync DNSEndpoint", "endpoint", endpointName)
	}

	return nil
}
