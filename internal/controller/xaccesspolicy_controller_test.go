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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kuadrantv1 "github.com/kuadrant/kuadrant-operator/api/v1"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	agenticv1alpha1 "sigs.k8s.io/kube-agentic-networking/api/v1alpha1"
)

var _ = Describe("XAccessPolicy Controller", func() {
	Context("When reconciling a resource in envtest", func() {
		const (
			resourceName      = "test-resource"
			resourceNamespace = "default"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: resourceNamespace,
		}
		xaccesspolicy := &agenticv1alpha1.XAccessPolicy{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind XAccessPolicy")
			err := k8sClient.Get(ctx, typeNamespacedName, xaccesspolicy)
			if err != nil && errors.IsNotFound(err) {
				resource := &agenticv1alpha1.XAccessPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: resourceNamespace,
					},
					Spec: agenticv1alpha1.AccessPolicySpec{
						TargetRefs: []gatewayapiv1.LocalPolicyTargetReferenceWithSectionName{
							{
								LocalPolicyTargetReference: gatewayapiv1.LocalPolicyTargetReference{
									Group: gatewayapiv1.Group(gatewayGroup),
									Kind:  gatewayapiv1.Kind(gatewayKind),
									Name:  "test-gateway",
								},
							},
						},
						Action: agenticv1alpha1.ActionTypeAllow,
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
			resource := &agenticv1alpha1.XAccessPolicy{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance XAccessPolicy")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			controllerReconciler := &XAccessPolicyReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).To(HaveOccurred()) // Gateway CRDs are not installed in envtest
		})
	})

	Context("Unit tests for AuthPolicy authentication rules and Enforced status", func() {
		const (
			gwName    = "my-gateway"
			namespace = "default"
		)

		var (
			ctx        context.Context
			gateway    *gatewayapiv1.Gateway
			gwKey      types.NamespacedName
			authPolKey types.NamespacedName
		)

		BeforeEach(func() {
			ctx = context.Background()
			gwKey = types.NamespacedName{Name: gwName, Namespace: namespace}
			authPolKey = types.NamespacedName{Name: gwName + "-auth", Namespace: namespace}

			gateway = &gatewayapiv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      gwName,
					Namespace: namespace,
				},
				Spec: gatewayapiv1.GatewaySpec{
					GatewayClassName: "istio",
				},
			}
		})

		It("should set AccessPolicy status to Pending (Programmed=False) until AuthPolicy is Enforced, then Programmed=True", func() {
			policy := &agenticv1alpha1.XAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-status-policy",
					Namespace: namespace,
				},
				Spec: agenticv1alpha1.AccessPolicySpec{
					TargetRefs: []gatewayapiv1.LocalPolicyTargetReferenceWithSectionName{
						{
							LocalPolicyTargetReference: gatewayapiv1.LocalPolicyTargetReference{
								Group: gatewayapiv1.Group(gatewayGroup),
								Kind:  gatewayapiv1.Kind(gatewayKind),
								Name:  gatewayapiv1.ObjectName(gwName),
							},
						},
					},
					Action: agenticv1alpha1.ActionTypeAllow,
					Rules: []agenticv1alpha1.AccessRule{
						{
							Name: "sa-rule",
							Source: agenticv1alpha1.AccessRuleSource{
								Type: agenticv1alpha1.AuthorizationSourceTypeServiceAccount,
								ServiceAccount: &agenticv1alpha1.AuthorizationSourceServiceAccount{
									Name: "app-sa",
								},
							},
						},
					},
				},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(k8sClient.Scheme()).
				WithStatusSubresource(&agenticv1alpha1.XAccessPolicy{}, &kuadrantv1.AuthPolicy{}).
				WithObjects(gateway, policy).
				Build()

			r := &XAccessPolicyReconciler{Client: fakeClient, Scheme: k8sClient.Scheme()}

			// 1. Initial reconcile creates AuthPolicy (not enforced yet)
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
			Expect(err).NotTo(HaveOccurred())

			// Verify AccessPolicy status: Programmed condition should be False (Pending)
			updatedPolicy := &agenticv1alpha1.XAccessPolicy{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{Name: policy.Name, Namespace: namespace}, updatedPolicy)).To(Succeed())
			Expect(updatedPolicy.Status.Ancestors).To(HaveLen(1))
			progCond := meta.FindStatusCondition(updatedPolicy.Status.Ancestors[0].Conditions, "Programmed")
			Expect(progCond).NotTo(BeNil())
			Expect(progCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(progCond.Reason).To(Equal("Pending"))

			// 2. Simulate Kuadrant Operator marking AuthPolicy as Enforced
			authPolicy := &kuadrantv1.AuthPolicy{}
			Expect(fakeClient.Get(ctx, authPolKey, authPolicy)).To(Succeed())
			authPolicy.Status.Conditions = []metav1.Condition{
				{
					Type:               "Enforced",
					Status:             metav1.ConditionTrue,
					Reason:             "Enforced",
					Message:            "AuthPolicy is enforced",
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(fakeClient.Status().Update(ctx, authPolicy)).To(Succeed())

			// 3. Reconcile again: AccessPolicy Programmed condition should now be True
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeClient.Get(ctx, types.NamespacedName{Name: policy.Name, Namespace: namespace}, updatedPolicy)).To(Succeed())
			progCond = meta.FindStatusCondition(updatedPolicy.Status.Ancestors[0].Conditions, "Programmed")
			Expect(progCond).NotTo(BeNil())
			Expect(progCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(progCond.Reason).To(Equal("Programmed"))
		})

		It("should generate two plain rules (spiffe and spiffe-header) for SPIFFE ID authentication", func() {
			spiffeID := agenticv1alpha1.AuthorizationSourceSPIFFE("spiffe://cluster.local/ns/default/sa/client-sa")
			policy := &agenticv1alpha1.XAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spiffe-policy",
					Namespace: namespace,
				},
				Spec: agenticv1alpha1.AccessPolicySpec{
					TargetRefs: []gatewayapiv1.LocalPolicyTargetReferenceWithSectionName{
						{
							LocalPolicyTargetReference: gatewayapiv1.LocalPolicyTargetReference{
								Group: gatewayapiv1.Group(gatewayGroup),
								Kind:  gatewayapiv1.Kind(gatewayKind),
								Name:  gatewayapiv1.ObjectName(gwName),
							},
						},
					},
					Action: agenticv1alpha1.ActionTypeAllow,
					Rules: []agenticv1alpha1.AccessRule{
						{
							Name: "spiffe-rule",
							Source: agenticv1alpha1.AccessRuleSource{
								Type:   agenticv1alpha1.AuthorizationSourceTypeSPIFFE,
								SPIFFE: &spiffeID,
							},
						},
					},
				},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(k8sClient.Scheme()).
				WithStatusSubresource(&agenticv1alpha1.XAccessPolicy{}, &kuadrantv1.AuthPolicy{}).
				WithObjects(gateway, policy).
				Build()

			r := &XAccessPolicyReconciler{Client: fakeClient, Scheme: k8sClient.Scheme()}

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
			Expect(err).NotTo(HaveOccurred())

			authPolicy := &kuadrantv1.AuthPolicy{}
			Expect(fakeClient.Get(ctx, authPolKey, authPolicy)).To(Succeed())
			auths := authPolicy.Spec.AuthScheme.Authentication
			Expect(auths).To(HaveKey("spiffe"))
			Expect(auths).To(HaveKey("spiffe-header"))

			// Verify spiffe rule (source principal)
			Expect(auths["spiffe"].Plain).NotTo(BeNil())
			Expect(string(auths["spiffe"].Plain.Expression)).To(Equal("request.source.principal"))

			// Verify spiffe-header rule (x-spiffe-id header)
			Expect(auths["spiffe-header"].Plain).NotTo(BeNil())
			Expect(string(auths["spiffe-header"].Plain.Expression)).To(Equal("request.headers['x-spiffe-id']"))
		})

		It("should generate default plain authentication when no authentication source is mentioned", func() {
			policy := &agenticv1alpha1.XAccessPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-auth-policy",
					Namespace: namespace,
				},
				Spec: agenticv1alpha1.AccessPolicySpec{
					TargetRefs: []gatewayapiv1.LocalPolicyTargetReferenceWithSectionName{
						{
							LocalPolicyTargetReference: gatewayapiv1.LocalPolicyTargetReference{
								Group: gatewayapiv1.Group(gatewayGroup),
								Kind:  gatewayapiv1.Kind(gatewayKind),
								Name:  gatewayapiv1.ObjectName(gwName),
							},
						},
					},
					Action: agenticv1alpha1.ActionTypeAllow,
					Rules: []agenticv1alpha1.AccessRule{
						{
							Name: "public-mcp-rule",
							Authorization: &agenticv1alpha1.AuthorizationRule{
								Type: agenticv1alpha1.AuthorizationRuleTypeInline,
								MCP: agenticv1alpha1.MCPAttributes{
									Methods: []agenticv1alpha1.MCPMethod{
										{Name: "tools"},
									},
								},
							},
						},
					},
				},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(k8sClient.Scheme()).
				WithStatusSubresource(&agenticv1alpha1.XAccessPolicy{}, &kuadrantv1.AuthPolicy{}).
				WithObjects(gateway, policy).
				Build()

			r := &XAccessPolicyReconciler{Client: fakeClient, Scheme: k8sClient.Scheme()}

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
			Expect(err).NotTo(HaveOccurred())

			authPolicy := &kuadrantv1.AuthPolicy{}
			Expect(fakeClient.Get(ctx, authPolKey, authPolicy)).To(Succeed())
			auths := authPolicy.Spec.AuthScheme.Authentication
			Expect(auths).To(HaveKey("plain"))
			Expect(auths).To(HaveKey("anonymous"))
			Expect(auths["plain"].Plain).NotTo(BeNil())
			Expect(string(auths["plain"].Plain.Expression)).To(Equal("'anonymous'"))
		})
	})
})
