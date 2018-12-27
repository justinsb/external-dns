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
	"reflect"
	"strings"

	dns "github.com/kubernetes-incubator/external-dns/endpoint"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	log "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// AnnotationNameDNSExternal is used to set up a DNS name for accessing the resource from outside the cluster
// For a service of Type=LoadBalancer, it would map to the external LB hostname or IP
const AnnotationNameDNSExternal = "dns.alpha.kubernetes.io/external"

// AnnotationNameDNSInternal is used to set up a DNS name for accessing the resource from inside the cluster
// This is only supported on Pods currently, and maps to the Internal address
const AnnotationNameDNSInternal = "dns.alpha.kubernetes.io/internal"

func EnsureDotSuffix(s string) string {
	if !strings.HasSuffix(s, ".") {
		s = s + "."
	}
	return s
}

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

	// Watch for changes to Pod
	err = c.Watch(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Watch for changes to Nodes (needed so we can see the external IP changes)
	mapNodeToPods := handler.ToRequestsFunc(
		func(a handler.MapObject) []reconcile.Request {
			nodeName := a.Meta.GetName()

			pods := &corev1.PodList{}
			// TODO: Add indexer to watch
			//err := r.List(context.TODO(), client.ForSelector("nodeName", nodeName), pods)
			err := r.List(context.TODO(), &client.ListOptions{}, pods)
			if err != nil {
				log.Error(err, "error retrieving pods", "nodeName", nodeName)
				return nil
			}

			var affected []reconcile.Request
			for i := range pods.Items {
				// TODO: Move to node watcher
				if pods.Items[i].Spec.NodeName != nodeName {
					continue
				}

				// TODO: Only if pod has annotation?
				affected = append(affected, reconcile.Request{NamespacedName: types.NamespacedName{
					Name:      pods.Items[i].Name,
					Namespace: pods.Items[i].Namespace,
				}})
				log.Info("node updated, refreshing pod", "node", nodeName, "pod", pods.Items[i].Namespace+"/"+pods.Items[i].Name)
			}

			log.Info("node updated, refreshing pods", "node", nodeName, "podCount", len(affected))

			return affected
		})

	// TODO: Filter node events to only register external/internal ip changes?
	err = c.Watch(&source.Kind{Type: &corev1.Node{}}, &handler.EnqueueRequestsFromMapFunc{
		ToRequests: mapNodeToPods,
	} /*, nodePredicate*/)
	if err != nil {
		return err
	}

	// Watch the endpoints created by us
	err = c.Watch(&source.Kind{Type: &dns.DNSEndpoint{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &corev1.Pod{},
	})
	if err != nil {
		return err
	}

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

	ctx := context.TODO()

	// Fetch the Pod instance
	pod := &corev1.Pod{}
	err := r.Get(ctx, request.NamespacedName, pod)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			log.Info("pod not found", "pod", request.NamespacedName)
			return reconcile.Result{}, nil
		}
		log.Error(err, "error reading pod", "pod", request.NamespacedName)
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	if err := r.updatePodRecords(ctx, pod); err != nil {
		log.Error(err, "error updating pod", "pod", request.NamespacedName)
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

// updatePodRecords will apply the records for the specified pod.
func (r *ReconcilePod) updatePodRecords(ctx context.Context, pod *corev1.Pod) error {
	log := log.Log

	var records []*dns.Endpoint

	specExternal := pod.Annotations[AnnotationNameDNSExternal]
	if specExternal != "" {
		var nodeName string
		if pod.Spec.HostNetwork {
			nodeName = pod.Spec.NodeName
		} else {
			log.V(4).Info("Pod had "+AnnotationNameDNSExternal+" annotation, but was not HostNetwork", "pod", pod.Name)
		}

		if nodeName != "" {
			node := &corev1.Node{}
			err := r.Get(context.TODO(), types.NamespacedName{Name: nodeName}, node)
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

			tokens := strings.Split(specExternal, ",")
			for _, token := range tokens {
				token = strings.TrimSpace(token)

				fqdn := EnsureDotSuffix(token)
				records = append(records, &dns.Endpoint{
					RecordType: dns.RecordTypeA,
					DNSName:    fqdn,
					Targets:    targets,
				})
			}
		}
	}

	specInternal := pod.Annotations[AnnotationNameDNSInternal]
	if specInternal != "" {
		var ips []string
		if pod.Spec.HostNetwork {
			if pod.Status.PodIP != "" {
				ips = append(ips, pod.Status.PodIP)
			}
		} else {
			log.V(4).Info("Pod had "+AnnotationNameDNSInternal+" annotation, but was not HostNetwork", "pod", pod.Name)
		}

		tokens := strings.Split(specInternal, ",")
		for _, token := range tokens {
			token = strings.TrimSpace(token)

			fqdn := EnsureDotSuffix(token)
			records = append(records, &dns.Endpoint{
				RecordType: dns.RecordTypeA,
				DNSName:    fqdn,
				Targets:    ips,
			})
		}
	}

	endpoint := &dns.DNSEndpoint{}
	endpoint.Namespace = pod.Namespace
	endpoint.Name = pod.Name
	endpoint.Spec.Endpoints = records

	if err := controllerutil.SetControllerReference(pod, endpoint, r.scheme); err != nil {
		return err
	}

	// Compare to the existing object
	found := &dns.DNSEndpoint{}
	err := r.Get(ctx, types.NamespacedName{Name: endpoint.Name, Namespace: endpoint.Namespace}, found)
	if err != nil && errors.IsNotFound(err) {
		log.Info("creating DNSEndpoint", "namespace", endpoint.Namespace, "name", endpoint.Name)
		err = r.Create(ctx, endpoint)
		if err != nil {
			return fmt.Errorf("error creating DNSEndpoint: %v", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("error fetching existing DNSEndpoint: %v", err)
	}

	if !reflect.DeepEqual(endpoint.Spec, found.Spec) {
		found.Spec = endpoint.Spec
		log.Info("updating DNSEndpoint", "namespace", endpoint.Namespace, "name", endpoint.Name)
		err = r.Update(ctx, found)
		if err != nil {
			return fmt.Errorf("error updating endpoint: %v", err)
		}
	} else {
		log.Info("in-sync DNSEndpoint", "namespace", endpoint.Namespace, "name", endpoint.Name)
	}

	return nil
}
