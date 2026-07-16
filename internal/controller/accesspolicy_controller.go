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
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
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

	"k8s.io/apimachinery/pkg/api/meta"
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

	var policy agenticv1alpha1.AccessPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if len(policy.Spec.TargetRefs) == 0 {
		return ctrl.Result{}, nil
	}

	targetRef := policy.Spec.TargetRefs[0]
	if string(targetRef.Kind) != gatewayKind {
		r.updateStatus(&policy, targetRef, agenticv1alpha1.PolicyConditionAccepted, metav1.ConditionFalse, gatewayapiv1.PolicyReasonInvalid, "TargetRef must be Gateway")
		return ctrl.Result{}, r.Status().Update(ctx, &policy)
	}

	var gateway gatewayapiv1.Gateway
	gwName := types.NamespacedName{Namespace: policy.Namespace, Name: string(targetRef.Name)}
	if err := r.Get(ctx, gwName, &gateway); err != nil {
		r.updateStatus(&policy, targetRef, agenticv1alpha1.PolicyConditionAccepted, metav1.ConditionFalse, gatewayapiv1.PolicyReasonTargetNotFound, "Gateway not found")
		_ = r.Status().Update(ctx, &policy)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Fetch all XAccessPolicies targeting this gateway
	var policyList agenticv1alpha1.AccessPolicyList
	if err := r.List(ctx, &policyList, client.InNamespace(policy.Namespace)); err != nil {
		return ctrl.Result{}, err
	}

	// Enforcement of ExternalAuth limits (only one allowed)
	if policy.Spec.Action == agenticv1alpha1.ActionTypeExternalAuth {
		r.updateStatus(&policy, targetRef, agenticv1alpha1.PolicyConditionAccepted, metav1.ConditionFalse, gatewayapiv1.PolicyReasonInvalid, "ExternalAuth action is out of scope and not supported")
		return ctrl.Result{}, r.Status().Update(ctx, &policy)
	}

	var allowPredicates []string
	var denyPredicates []string
	allValid := true

	for i := range policyList.Items {
		p := &policyList.Items[i]
		if len(p.Spec.TargetRefs) == 0 {
			continue
		}

		isTargetingGateway := false
		for _, targetRef := range p.Spec.TargetRefs {
			if string(targetRef.Kind) == gatewayKind && string(targetRef.Name) == gateway.Name {
				isTargetingGateway = true
				break
			}
		}

		if !isTargetingGateway {
			continue
		}

		if p.Spec.Action == agenticv1alpha1.ActionTypeExternalAuth {
			// Skip processing ExternalAuth policies for generating AuthPolicy combined-rules
			continue
		}

		for _, rule := range p.Spec.Rules {
			if rule.Source.Type == agenticv1alpha1.AuthorizationSourceTypeSPIFFE {
				continue
			}

			// Bypass source identity check for MVP since we are only testing Tool Name auth via anonymous proxy
			var ruleExpr string

			var authExpr string
			if rule.Authorization != nil {
				if string(rule.Authorization.Type) == "CEL" && rule.Authorization.CEL != nil {
					authExpr = rule.Authorization.CEL.Expression
					// Translate MCP Tool Name pseudo-variable to a safe proxy HTTP header check
					safeHeaderCheck := "(has(request.headers) && 'x-mcp-toolname' in request.headers ? request.headers['x-mcp-toolname'] : '')"
					authExpr = strings.ReplaceAll(authExpr, "request.mcp.tool_name", safeHeaderCheck)
				} else if string(rule.Authorization.Type) == "Inline" {
					var methodExprs []string
					if len(rule.Authorization.MCP.Methods) > 0 {
						for _, m := range rule.Authorization.MCP.Methods {
							if string(m.Name) == "tools/call" && len(m.Params) > 0 {
								methodExprs = append(methodExprs, fmt.Sprintf("request.headers['x-mcp-toolname'] == '%s'", m.Params[0]))
							}
						}
					}
					if len(methodExprs) > 0 {
						authExpr = strings.Join(methodExprs, " || ")
					}
				}
			}

			finalExpr := ""
			if ruleExpr != "" && authExpr != "" {
				finalExpr = fmt.Sprintf("(%s) && (%s)", ruleExpr, authExpr)
			} else if ruleExpr != "" {
				finalExpr = ruleExpr
			} else if authExpr != "" {
				finalExpr = authExpr
			} else {
				finalExpr = "true"
			}

			if p.Spec.Action == agenticv1alpha1.ActionTypeAllow || p.Spec.Action == "" {
				allowPredicates = append(allowPredicates, finalExpr)
			} else if string(p.Spec.Action) == "Deny" {
				denyPredicates = append(denyPredicates, finalExpr)
			}
		}
	}

	var combinedExpr string
	if len(allowPredicates) > 0 {
		allowExpr := strings.Join(allowPredicates, " || ")
		combinedExpr = fmt.Sprintf("(%s)", allowExpr)
	}
	if len(denyPredicates) > 0 {
		denyExpr := strings.Join(denyPredicates, " || ")
		if combinedExpr != "" {
			combinedExpr = fmt.Sprintf("%s && !(%s)", combinedExpr, denyExpr)
		} else {
			combinedExpr = fmt.Sprintf("!(%s)", denyExpr)
		}
	}

	var combinedPredicates []authorinov1beta3.PatternExpressionOrRef
	if combinedExpr != "" {
		combinedPredicates = append(combinedPredicates, authorinov1beta3.PatternExpressionOrRef{
			CelPredicate: authorinov1beta3.CelPredicate{
				Predicate: combinedExpr,
			},
		})
	}

	authPolicyName := fmt.Sprintf("%s-auth", gateway.Name)
	authPolicy := &kuadrantv1.AuthPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      authPolicyName,
			Namespace: gateway.Namespace,
		},
	}

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, authPolicy, func() error {
		if authPolicy.Labels == nil {
			authPolicy.Labels = map[string]string{}
		}
		authPolicy.Labels["app.kubernetes.io/managed-by"] = "accesspolicy-controller"

		if err := controllerutil.SetControllerReference(&policy, authPolicy, r.Scheme); err != nil {
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
		if authPolicy.Spec.AuthScheme.Authorization == nil {
			authPolicy.Spec.AuthScheme.Authorization = map[string]kuadrantv1.MergeableAuthorizationSpec{}
		}

		if len(combinedPredicates) > 0 {
			authPolicy.Spec.AuthScheme.Authorization["combined-rules"] = kuadrantv1.MergeableAuthorizationSpec{
				AuthorizationSpec: authorinov1beta3.AuthorizationSpec{
					AuthorizationMethodSpec: authorinov1beta3.AuthorizationMethodSpec{
						PatternMatching: &authorinov1beta3.PatternMatchingAuthorizationSpec{
							Patterns: combinedPredicates,
						},
					},
				},
			}
		} else {
			delete(authPolicy.Spec.AuthScheme.Authorization, "combined-rules")
		}

		return nil
	})

	if err != nil {
		r.updateStatus(&policy, targetRef, agenticv1alpha1.PolicyConditionAccepted, metav1.ConditionFalse, gatewayapiv1.PolicyReasonInvalid, "ProgramError: "+err.Error())
		_ = r.Status().Update(ctx, &policy)
		return ctrl.Result{}, err
	}

	log.Info("Reconciled AuthPolicy", "operation", op)

	if allValid {
		r.updateStatus(&policy, targetRef, agenticv1alpha1.PolicyConditionAccepted, metav1.ConditionTrue, agenticv1alpha1.PolicyReasonAccepted, "Policy accepted and valid")
		if err := r.Status().Update(ctx, &policy); err != nil {
			return ctrl.Result{}, err
		}
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
		For(&agenticv1alpha1.AccessPolicy{}).
		Owns(&kuadrantv1.AuthPolicy{}).
		Watches(
			&gatewayapiv1.Gateway{},
			handler.EnqueueRequestsFromMapFunc(r.findPoliciesForGateway),
		).
		Complete(r)
}

func (r *AccessPolicyReconciler) findPoliciesForGateway(ctx context.Context, obj client.Object) []reconcile.Request {
	gateway, ok := obj.(*gatewayapiv1.Gateway)
	if !ok {
		return nil
	}

	var policyList agenticv1alpha1.AccessPolicyList
	if err := r.List(ctx, &policyList, client.InNamespace(gateway.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, p := range policyList.Items {
		for _, targetRef := range p.Spec.TargetRefs {
			if string(targetRef.Name) == gateway.Name {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      p.Name,
						Namespace: p.Namespace,
					},
				})
				break // Only need to enqueue this policy once
			}
		}
	}
	return requests
}
