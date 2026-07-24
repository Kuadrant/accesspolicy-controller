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
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	agenticv1alpha1 "sigs.k8s.io/kube-agentic-networking/api/v1alpha1"

	authorinov1beta3 "github.com/kuadrant/authorino/api/v1beta3"
	kuadrantv1 "github.com/kuadrant/kuadrant-operator/api/v1"

	"github.com/Kuadrant/accesspolicy-controller/internal/translator"
)

const gatewayKind = "Gateway"

// AccessPolicyReconciler reconciles a AccessPolicy object
type AccessPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=agentic.networking.x-k8s.io,resources=accesspolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentic.networking.x-k8s.io,resources=accesspolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agentic.networking.x-k8s.io,resources=accesspolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=kuadrant.io,resources=authpolicies,verbs=get;list;watch;create;update;patch;delete

func (r *AccessPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the Gateway
	var gateway gatewayapiv1.Gateway
	if err := r.Get(ctx, req.NamespacedName, &gateway); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Fetch all AccessPolicies in the namespace
	var policyList agenticv1alpha1.AccessPolicyList
	if err := r.List(ctx, &policyList, client.InNamespace(gateway.Namespace)); err != nil {
		return ctrl.Result{}, err
	}

	// Filter policies that target this Gateway
	var targetedPolicies []agenticv1alpha1.AccessPolicy
	for _, p := range policyList.Items {
		for _, targetRef := range p.Spec.TargetRefs {
			if string(targetRef.Kind) == gatewayKind && string(targetRef.Name) == gateway.Name {
				targetedPolicies = append(targetedPolicies, p)
				break
			}
		}
	}

	authPolicyName := fmt.Sprintf("%s-auth", gateway.Name)
	authPolicy := &kuadrantv1.AuthPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      authPolicyName,
			Namespace: gateway.Namespace,
		},
	}

	// If no policies target this Gateway, ensure AuthPolicy is deleted
	if len(targetedPolicies) == 0 {
		err := r.Delete(ctx, authPolicy)
		if err != nil && !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Sort policies by CreationTimestamp
	sort.Slice(targetedPolicies, func(i, j int) bool {
		return targetedPolicies[i].CreationTimestamp.Time.Before(targetedPolicies[j].CreationTimestamp.Time)
	})

	authentications := make(map[string]kuadrantv1.MergeableAuthenticationSpec)
	authorizations := make(map[string]kuadrantv1.MergeableAuthorizationSpec)

	priority := 0
	hasServiceAccount := false
	hasSPIFFE := false

	// Track policies that need status updates
	validPolicies := make([]*agenticv1alpha1.AccessPolicy, 0)

	for i := range targetedPolicies {
		p := &targetedPolicies[i]
		
		var currentTargetRef gatewayapiv1.LocalPolicyTargetReferenceWithSectionName
		for _, targetRef := range p.Spec.TargetRefs {
			if string(targetRef.Kind) == gatewayKind && string(targetRef.Name) == gateway.Name {
				currentTargetRef = targetRef
				break
			}
		}

		if p.Spec.Action == agenticv1alpha1.ActionTypeExternalAuth {
			r.updateStatus(p, currentTargetRef, agenticv1alpha1.PolicyConditionAccepted, metav1.ConditionFalse, gatewayapiv1.PolicyReasonInvalid, "ExternalAuth action is out of scope and not supported")
			_ = r.Status().Update(ctx, p)
			continue
		}

		allValid := true
		for _, rule := range p.Spec.Rules {
			var principal string
			if rule.Source.Type == agenticv1alpha1.AuthorizationSourceTypeServiceAccount && rule.Source.ServiceAccount != nil {
				hasServiceAccount = true
				ns := rule.Source.ServiceAccount.Namespace
				if ns == "" {
					ns = p.Namespace
				}
				principal = fmt.Sprintf("system:serviceaccount:%s:%s", ns, rule.Source.ServiceAccount.Name)
			} else if rule.Source.Type == agenticv1alpha1.AuthorizationSourceTypeSPIFFE && rule.Source.SPIFFE != nil {
				hasSPIFFE = true
				principal = string(*rule.Source.SPIFFE)
			}

			var authExprs []string
			if rule.Authorization != nil {
				if string(rule.Authorization.Type) == "CEL" && rule.Authorization.CEL != nil {
					authExpr := rule.Authorization.CEL.Expression
					authExpr = translator.TranslateCEL(authExpr)
					if err := translator.ValidateCEL(authExpr); err != nil {
						r.updateStatus(p, currentTargetRef, agenticv1alpha1.PolicyConditionAccepted, metav1.ConditionFalse, gatewayapiv1.PolicyReasonInvalid, "Invalid CEL: "+err.Error())
						_ = r.Status().Update(ctx, p)
						allValid = false
						break
					}
					authExprs = append(authExprs, authExpr)
				} else if string(rule.Authorization.Type) == "Inline" {
					if rule.Authorization.MCP.MCPBaseProtocolMethodsOption == agenticv1alpha1.MCPBaseProtocolMethodsOptionMatch {
						authExprs = append(authExprs, "request.headers['x-mcp-method'] in ['initialize', 'tools/list', 'completion', 'logging', 'notifications', 'ping'] || request.method in ['GET', 'DELETE']")
					}
					var methodExprs []string
					if len(rule.Authorization.MCP.Methods) > 0 {
						for _, m := range rule.Authorization.MCP.Methods {
							if len(m.Params) > 0 {
								for _, param := range m.Params {
									methodExprs = append(methodExprs, fmt.Sprintf("(request.headers['x-mcp-method'] == '%s' && request.headers['x-mcp-toolname'] == '%s')", m.Name, param))
								}
							} else {
								methodExprs = append(methodExprs, fmt.Sprintf("request.headers['x-mcp-method'] == '%s'", m.Name))
							}
						}
					}
					if len(methodExprs) > 0 {
						authExprs = append(authExprs, "("+strings.Join(methodExprs, " || ")+")")
					}
				}
			}

			if !allValid {
				continue
			}

			whenPredicates := []authorinov1beta3.PatternExpressionOrRef{
				{CelPredicate: authorinov1beta3.CelPredicate{Predicate: "size(auth.authorization) == 0"}},
			}
			if principal != "" {
				whenPredicates = append(whenPredicates, authorinov1beta3.PatternExpressionOrRef{
					CelPredicate: authorinov1beta3.CelPredicate{Predicate: fmt.Sprintf("auth.identity.principal == '%s'", principal)},
				})
			}
			if len(authExprs) > 0 {
				combinedAuthExpr := strings.Join(authExprs, " || ")
				whenPredicates = append(whenPredicates, authorinov1beta3.PatternExpressionOrRef{
					CelPredicate: authorinov1beta3.CelPredicate{Predicate: combinedAuthExpr},
				})
			}

			regoRule := "allow = true"
			if p.Spec.Action == agenticv1alpha1.ActionTypeAllow || p.Spec.Action == "" {
				regoRule = "allow = true"
			} else if string(p.Spec.Action) == "Deny" {
				regoRule = "allow = false"
			}

			ruleKey := fmt.Sprintf("%s-%s", p.Name, rule.Name)
			authorizations[ruleKey] = kuadrantv1.MergeableAuthorizationSpec{
				AuthorizationSpec: authorinov1beta3.AuthorizationSpec{
					Priority: priority,
					When:     whenPredicates,
					AuthorizationMethodSpec: authorinov1beta3.AuthorizationMethodSpec{
						Opa: &authorinov1beta3.OpaAuthorizationSpec{
							Rego: regoRule,
						},
					},
				},
			}
			priority++
		}

		if allValid {
			validPolicies = append(validPolicies, p)
		}
	}

	// Fail-close rule
	authorizations["fail-close"] = kuadrantv1.MergeableAuthorizationSpec{
		AuthorizationSpec: authorinov1beta3.AuthorizationSpec{
			Priority: priority,
			When: []authorinov1beta3.PatternExpressionOrRef{
				{CelPredicate: authorinov1beta3.CelPredicate{Predicate: "size(auth.authorization) == 0"}},
			},
			AuthorizationMethodSpec: authorinov1beta3.AuthorizationMethodSpec{
				Opa: &authorinov1beta3.OpaAuthorizationSpec{
					Rego: "allow = false",
				},
			},
		},
	}

	if hasServiceAccount {
		authentications["service-account"] = kuadrantv1.MergeableAuthenticationSpec{
			AuthenticationSpec: authorinov1beta3.AuthenticationSpec{
				AuthenticationMethodSpec: authorinov1beta3.AuthenticationMethodSpec{
					KubernetesTokenReview: &authorinov1beta3.KubernetesTokenReviewSpec{
						Audiences: []string{"https://kubernetes.default.svc.cluster.local"},
					},
				},
				When: []authorinov1beta3.PatternExpressionOrRef{
					{CelPredicate: authorinov1beta3.CelPredicate{Predicate: "'authorization' in request.headers && request.headers['authorization'].startsWith('Bearer ')"}},
				},
				Overrides: authorinov1beta3.ExtendedProperties{
					"principal": authorinov1beta3.ValueOrSelector{Expression: "auth.identity.user.username"},
				},
			},
		}
	}

	if hasSPIFFE {
		authentications["spiffe"] = kuadrantv1.MergeableAuthenticationSpec{
			AuthenticationSpec: authorinov1beta3.AuthenticationSpec{
				AuthenticationMethodSpec: authorinov1beta3.AuthenticationMethodSpec{
					Plain: &authorinov1beta3.PlainIdentitySpec{
						Expression: "source.principal",
					},
				},
				When: []authorinov1beta3.PatternExpressionOrRef{
					{CelPredicate: authorinov1beta3.CelPredicate{Predicate: "source.principal.startsWith('spiffe://')"}},
				},
				Overrides: authorinov1beta3.ExtendedProperties{
					"principal": authorinov1beta3.ValueOrSelector{Expression: "source.principal"},
				},
			},
		}
	}

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, authPolicy, func() error {
		if authPolicy.Labels == nil {
			authPolicy.Labels = map[string]string{}
		}
		authPolicy.Labels["app.kubernetes.io/managed-by"] = "accesspolicy-controller"

		if err := controllerutil.SetControllerReference(&gateway, authPolicy, r.Scheme); err != nil {
			log.Error(err, "unable to set owner reference")
		}

		authPolicy.Spec.TargetRef = gatewayapiv1.LocalPolicyTargetReferenceWithSectionName{
			LocalPolicyTargetReference: gatewayapiv1.LocalPolicyTargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  gatewayKind,
				Name:  gatewayapiv1.ObjectName(gateway.Name),
			},
		}

		if authPolicy.Spec.AuthScheme == nil {
			authPolicy.Spec.AuthScheme = &kuadrantv1.AuthSchemeSpec{}
		}

		authPolicy.Spec.AuthScheme.Authentication = authentications
		authPolicy.Spec.AuthScheme.Authorization = authorizations

		return nil
	})

	if err != nil {
		// Update all valid policies with ProgramError
		for _, p := range validPolicies {
			var currentTargetRef gatewayapiv1.LocalPolicyTargetReferenceWithSectionName
			for _, targetRef := range p.Spec.TargetRefs {
				if string(targetRef.Kind) == gatewayKind && string(targetRef.Name) == gateway.Name {
					currentTargetRef = targetRef
					break
				}
			}
			r.updateStatus(p, currentTargetRef, agenticv1alpha1.PolicyConditionAccepted, metav1.ConditionFalse, gatewayapiv1.PolicyReasonInvalid, "ProgramError: "+err.Error())
			_ = r.Status().Update(ctx, p)
		}
		return ctrl.Result{}, err
	}

	log.Info("Reconciled AuthPolicy", "operation", op)

	// Update successful status for all valid policies
	for _, p := range validPolicies {
		var currentTargetRef gatewayapiv1.LocalPolicyTargetReferenceWithSectionName
		for _, targetRef := range p.Spec.TargetRefs {
			if string(targetRef.Kind) == gatewayKind && string(targetRef.Name) == gateway.Name {
				currentTargetRef = targetRef
				break
			}
		}
		r.updateStatus(p, currentTargetRef, agenticv1alpha1.PolicyConditionAccepted, metav1.ConditionTrue, agenticv1alpha1.PolicyReasonAccepted, "Policy accepted and valid")
		_ = r.Status().Update(ctx, p)
	}

	return ctrl.Result{}, nil
}

func (r *AccessPolicyReconciler) updateStatus(policy *agenticv1alpha1.AccessPolicy, targetRef gatewayapiv1.LocalPolicyTargetReferenceWithSectionName, conditionType gatewayapiv1.PolicyConditionType, status metav1.ConditionStatus, reason gatewayapiv1.PolicyConditionReason, message string) {
	var ancestor *gatewayapiv1.PolicyAncestorStatus

	gwGroup := gatewayapiv1.Group("gateway.networking.k8s.io")
	gwKind := gatewayapiv1.Kind("Gateway")
	if targetRef.Group != "" {
		gwGroup = targetRef.Group
	}
	if targetRef.Kind != "" {
		gwKind = targetRef.Kind
	}
	gwNamespace := gatewayapiv1.Namespace(policy.Namespace)

	ancestorRef := gatewayapiv1.ParentReference{
		Group:     &gwGroup,
		Kind:      &gwKind,
		Namespace: &gwNamespace,
		Name:      targetRef.Name,
	}

	for i := range policy.Status.Ancestors {
		if policy.Status.Ancestors[i].AncestorRef.Group != nil && *policy.Status.Ancestors[i].AncestorRef.Group == gwGroup &&
			policy.Status.Ancestors[i].AncestorRef.Kind != nil && *policy.Status.Ancestors[i].AncestorRef.Kind == gwKind &&
			policy.Status.Ancestors[i].AncestorRef.Name == targetRef.Name {
			ancestor = &policy.Status.Ancestors[i]
			break
		}
	}

	if ancestor == nil {
		policy.Status.Ancestors = append(policy.Status.Ancestors, gatewayapiv1.PolicyAncestorStatus{
			AncestorRef:    ancestorRef,
			ControllerName: "agentic.networking.x-k8s.io/accesspolicy-controller",
		})
		ancestor = &policy.Status.Ancestors[len(policy.Status.Ancestors)-1]
	}

	meta.SetStatusCondition(&ancestor.Conditions, metav1.Condition{
		Type:               string(conditionType),
		Status:             status,
		Reason:             string(reason),
		Message:            message,
		ObservedGeneration: policy.Generation,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *AccessPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayapiv1.Gateway{}).
		Watches(
			&agenticv1alpha1.AccessPolicy{},
			handler.EnqueueRequestsFromMapFunc(r.findGatewaysForPolicy),
		).
		Owns(&kuadrantv1.AuthPolicy{}).
		Complete(r)
}

func (r *AccessPolicyReconciler) findGatewaysForPolicy(ctx context.Context, obj client.Object) []reconcile.Request {
	policy, ok := obj.(*agenticv1alpha1.AccessPolicy)
	if !ok {
		return nil
	}

	var requests []reconcile.Request
	for _, targetRef := range policy.Spec.TargetRefs {
		if string(targetRef.Kind) == gatewayKind {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      string(targetRef.Name),
					Namespace: policy.Namespace,
				},
			})
		}
	}
	return requests
}
