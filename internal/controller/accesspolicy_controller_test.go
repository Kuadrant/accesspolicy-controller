/*
Copyright 2026.

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

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agenticv1alpha1 "sigs.k8s.io/kube-agentic-networking/api/v1alpha1"
)

var _ = Describe("AccessPolicy Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName      = "test-resource"
			resourceNamespace = "default"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: resourceNamespace,
		}
		accesspolicy := &agenticv1alpha1.AccessPolicy{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind AccessPolicy")
			err := k8sClient.Get(ctx, typeNamespacedName, accesspolicy)
			if err != nil && errors.IsNotFound(err) {
				resource := &agenticv1alpha1.AccessPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: resourceNamespace,
					},
					Spec: agenticv1alpha1.AccessPolicySpec{
						TargetRefs: []gatewayapiv1.LocalPolicyTargetReferenceWithSectionName{
							{
								LocalPolicyTargetReference: gatewayapiv1.LocalPolicyTargetReference{
									Group: "gateway.networking.k8s.io",
									Kind:  gatewayapiv1.Kind(gatewayKind),
									Name:  "test-gateway",
								},
							},
						},
						Action: "Allow",
						Rules: []agenticv1alpha1.AccessRule{
							{
								Name: "test-rule",
								Source: agenticv1alpha1.AccessRuleSource{
									Type: agenticv1alpha1.AuthorizationSourceTypeServiceAccount,
									ServiceAccount: &agenticv1alpha1.AuthorizationSourceServiceAccount{
										Name: "default",
									},
								},
								Authorization: &agenticv1alpha1.AuthorizationRule{
									Type: agenticv1alpha1.AuthorizationRuleTypeCEL,
									CEL: &agenticv1alpha1.AccessPolicyCELRule{
										Expression: "request.mcp.tool_name == 'search_web'",
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &agenticv1alpha1.AccessPolicy{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance AccessPolicy")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &AccessPolicyReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).To(HaveOccurred()) // Gateway CRDs are not installed in envtest
		})
	})
})
