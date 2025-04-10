package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// NodeReconciler reconciles a Node object
type NodeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// TargetTaint is the taint we're looking for on nodes
	TargetTaint string
	// OwnedByNames is a list of workload names to check for readiness
	OwnedByNames []string
}

// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	node := &corev1.Node{}

	if err := r.Get(ctx, req.NamespacedName, node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if the node has our target taint
	hasTargetTaint := false
	for _, taint := range node.Spec.Taints {
		if taint.Key == r.TargetTaint {
			hasTargetTaint = true
			break
		}
	}

	if !hasTargetTaint {
		// Node doesn't have our target taint, no need to reconcile
		return ctrl.Result{}, nil
	}

	// Get all pods on this node
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.MatchingFields{"spec.nodeName": node.Name}); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list pods: %w", err)
	}

	// Check if all required pods are ready
	allPodsReady := true
	hasTargetPods := false
	for _, pod := range pods.Items {
		// Skip pods that aren't owned by our target workloads
		isTargetPod := false
		for _, owner := range pod.OwnerReferences {
			for _, targetName := range r.OwnedByNames {
				if owner.Name == targetName {
					isTargetPod = true
					hasTargetPods = true
					break
				}
			}
			if isTargetPod {
				break
			}
		}

		if !isTargetPod {
			continue
		}

		// Check if pod is ready
		podReady := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				podReady = true
				break
			}
		}

		if !podReady {
			log.Info("Pod is not ready, requeueing", "pod", pod.Name, "podStatus", pod.Status, "finalizers", pod.Finalizers)
			allPodsReady = false
			break
		}
	}

	if allPodsReady && hasTargetPods {
		// Remove the target taint
		newTaints := make([]corev1.Taint, 0)
		for _, taint := range node.Spec.Taints {
			if taint.Key != r.TargetTaint {
				newTaints = append(newTaints, taint)
			}
		}
		node.Spec.Taints = newTaints

		if err := r.Update(ctx, node); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update node: %w", err)
		}

		log.Info("Removed target taint from node", "node", node.Name)
		return ctrl.Result{}, nil
	}

	// Not all pods are ready yet, requeue
	log.Info("Not all required pods are ready, requeueing", "node", node.Name)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create an index for pods by node name
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&corev1.Pod{},
		"spec.nodeName",
		func(obj client.Object) []string {
			pod := obj.(*corev1.Pod)
			if pod.Spec.NodeName == "" {
				return nil
			}
			return []string{pod.Spec.NodeName}
		},
	); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		WithEventFilter(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return true
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return false
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				return false
			},
			GenericFunc: func(e event.GenericEvent) bool {
				return false
			},
		}).
		Complete(r)
}
