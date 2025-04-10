package controller

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func cleanupPod(ctx context.Context, k8sClient client.Client, pod *corev1.Pod) {
	// Wait for the pod to be deleted
	Eventually(func() bool {
		// Delete the pod
		gracePeriodSeconds := int64(0) // Required to delete pod in envtest environment
		Expect(k8sClient.Delete(ctx, pod, &client.DeleteOptions{GracePeriodSeconds: &gracePeriodSeconds})).To(Succeed())

		// Make sure pod is deleted
		err := k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, pod)
		return errors.IsNotFound(err)
	}, "10s", "2s").Should(BeTrue(), "Pod was not deleted within timeout period")
}

var _ = Describe("NodeReconciler", func() {
	var (
		ctx        context.Context
		reconciler *NodeReconciler
		node       *corev1.Node
	)

	BeforeEach(func() {
		ctx = context.Background()

		// Create a test node with the target taint
		node = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-node",
			},
			Spec: corev1.NodeSpec{
				Taints: []corev1.Taint{
					{
						Key:    "test-taint",
						Value:  "true",
						Effect: corev1.TaintEffectNoSchedule,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())

		// Create the reconciler with test configuration
		reconciler = &NodeReconciler{
			Client:       k8sClient,
			Scheme:       scheme.Scheme,
			TargetTaint:  "test-taint",
			OwnedByNames: []string{"test-daemonset"},
		}
	})

	AfterEach(func() {
		// Clean up the test node
		Expect(k8sClient.Delete(ctx, node)).To(Succeed())

		// Wait for the node to be fully deleted
		Eventually(func() error {
			node := &corev1.Node{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-node"}, node)
			if err != nil {
				return nil // Node is gone, which is what we want
			}
			return fmt.Errorf("node still exists")
		}, "2m", "1s").Should(Succeed(), "Node was not deleted within timeout period")
	})

	Context("when reconciling a node", func() {
		It("should ignore nodes without the target taint", func() {
			// Create a node without the target taint
			cleanNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "clean-node",
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{
						{
							Key:    "other-taint",
							Value:  "true",
							Effect: corev1.TaintEffectNoSchedule,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cleanNode)).To(Succeed())
			defer func() {
				Expect(k8sClient.Delete(ctx, cleanNode)).To(Succeed())
			}()

			// Reconcile the node
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cleanNode.Name},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			// Verify node is unchanged
			updatedNode := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cleanNode.Name}, updatedNode)).To(Succeed())
			Expect(updatedNode.Spec.Taints).To(Equal(cleanNode.Spec.Taints))
		})

		It("should keep taint when no pods exist", func() {
			// Reconcile the node
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: node.Name},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			// Verify taint still exists
			updatedNode := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updatedNode)).To(Succeed())
			Expect(updatedNode.Spec.Taints).To(ContainElement(corev1.Taint{
				Key:    "test-taint",
				Value:  "true",
				Effect: corev1.TaintEffectNoSchedule,
			}))
		})

		It("should keep taint when pods are not ready", func() {
			// Create an unready pod
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-unready",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "DaemonSet",
							Name:       "test-daemonset",
							UID:        "test-uid",
						},
					},
				},
				Spec: corev1.PodSpec{
					NodeName: node.Name,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "busybox",
						},
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
				},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			defer cleanupPod(ctx, k8sClient, pod)

			// Reconcile the node
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: node.Name},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			// Verify taint still exists
			updatedNode := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updatedNode)).To(Succeed())
			Expect(updatedNode.Spec.Taints).To(ContainElement(corev1.Taint{
				Key:    "test-taint",
				Value:  "true",
				Effect: corev1.TaintEffectNoSchedule,
			}))
		})

		It("should remove taint when all required pods are ready", func() {
			reconciler.OwnedByNames = []string{"test-daemonset-1", "test-daemonset-2"}
			// Create first pod
			pod1 := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-ready-1",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "DaemonSet",
							Name:       "test-daemonset-1",
							UID:        "test-uid",
						},
					},
				},
				Spec: corev1.PodSpec{
					NodeName: node.Name,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "busybox",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pod1)).To(Succeed())
			defer cleanupPod(ctx, k8sClient, pod1)

			// Create second pod
			pod2 := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-ready-2",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "DaemonSet",
							Name:       "test-daemonset-2",
							UID:        "test-uid",
						},
					},
				},
				Spec: corev1.PodSpec{
					NodeName: node.Name,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "busybox",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pod2)).To(Succeed())
			defer cleanupPod(ctx, k8sClient, pod2)

			// Update first pod status to ready
			pod1Patch := pod1.DeepCopy()
			pod1Patch.Status = corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodReady,
						Status: corev1.ConditionTrue,
					},
				},
			}
			Expect(k8sClient.Status().Patch(ctx, pod1Patch, client.MergeFrom(pod1))).To(Succeed())

			// Reconcile the node - should still have taint since pod2 isn't ready
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: node.Name},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			// Update second pod status to ready
			pod2Patch := pod2.DeepCopy()
			pod2Patch.Status = corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodReady,
						Status: corev1.ConditionTrue,
					},
				},
			}
			Expect(k8sClient.Status().Patch(ctx, pod2Patch, client.MergeFrom(pod2))).To(Succeed())

			// Reconcile the node again - now both pods are ready
			result, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: node.Name},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			// Verify taint is removed
			updatedNode := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updatedNode)).To(Succeed())
			Expect(updatedNode.Spec.Taints).NotTo(ContainElement(corev1.Taint{
				Key:    "test-taint",
				Value:  "true",
				Effect: corev1.TaintEffectNoSchedule,
			}))
		})

		It("should ignore pods not owned by target workloads", func() {
			// Create a ready pod owned by a different workload
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-other",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "DaemonSet",
							Name:       "other-daemonset",
							UID:        "other-uid",
						},
					},
				},
				Spec: corev1.PodSpec{
					NodeName: node.Name,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "busybox",
						},
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			defer cleanupPod(ctx, k8sClient, pod)

			// Reconcile the node
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: node.Name},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			// Verify taint still exists
			updatedNode := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updatedNode)).To(Succeed())
			Expect(updatedNode.Spec.Taints).To(ContainElement(corev1.Taint{
				Key:    "test-taint",
				Value:  "true",
				Effect: corev1.TaintEffectNoSchedule,
			}))
		})

		It("should keep taint when one pod is ready and another is not for different daemonsets", func() {
			reconciler.OwnedByNames = []string{"test-daemonset-1", "test-daemonset-2"}

			// Create first pod (ready)
			pod1 := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-ready-1",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "DaemonSet",
							Name:       "test-daemonset-1",
							UID:        "test-uid-1",
						},
					},
				},
				Spec: corev1.PodSpec{
					NodeName: node.Name,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "busybox",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pod1)).To(Succeed())
			defer cleanupPod(ctx, k8sClient, pod1)

			// Update pod1 status to ready
			pod1.Status = corev1.PodStatus{
				Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodReady,
						Status: corev1.ConditionTrue,
					},
				},
			}
			Expect(k8sClient.Status().Update(ctx, pod1)).To(Succeed())

			// Create second pod (not ready)
			pod2 := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod-not-ready-2",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       "DaemonSet",
							Name:       "test-daemonset-2",
							UID:        "test-uid-2",
						},
					},
				},
				Spec: corev1.PodSpec{
					NodeName: node.Name,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "busybox",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pod2)).To(Succeed())
			defer cleanupPod(ctx, k8sClient, pod2)

			// Reconcile the node
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: node.Name},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			// Verify taint still exists
			updatedNode := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updatedNode)).To(Succeed())
			Expect(updatedNode.Spec.Taints).To(ContainElement(corev1.Taint{
				Key:    "test-taint",
				Value:  "true",
				Effect: corev1.TaintEffectNoSchedule,
			}))
		})
	})
})
