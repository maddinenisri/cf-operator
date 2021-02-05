/*
Copyright 2021.

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

package controllers

import (
	"context"
	coreerrors "errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/cloudformation/cloudformationiface"

	infrav1alpha1 "github.com/maddinenisri/cf-operator/api/v1alpha1"
)

const (
	controllerKey   = "kubernetes.io/controlled-by"
	controllerValue = "infra.mdstechinc.com/operator"
	cfFinalizer     = "finalizer.infra.mdstechinc.com"
	ownerKey        = "kubernetes.io/owned-by"
)

var (
	ErrStackNotFound = coreerrors.New("stack not found")
)

// CloudFormationReconciler reconciles a CloudFormation object
type CloudFormationReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the CloudFormation object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.7.0/pkg/reconcile

// +kubebuilder:rbac:groups=infra.mdstechinc.com,resources=cloudformations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infra.mdstechinc.com,resources=cloudformations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infra.mdstechinc.com,resources=cloudformations/finalizers,verbs=update
func (r *CloudFormationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("cloudformation", req.NamespacedName)
	log.Info("Reconciling CloudFormation CRD")

	instance := &infrav1alpha1.CloudFormation{}
	err := r.Client.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("CloudFormation resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get CloudFormation resource")
		return ctrl.Result{}, err
	}

	cfClient := r.awsClientSession()

	// Check if the Cloudformation instance is delete event
	isMarkedToBeDeleted := instance.GetDeletionTimestamp() != nil
	if isMarkedToBeDeleted {
		log.Info("Deleting resource")
		log.WithValues("Finalizers", instance.GetFinalizers())
		if contains(instance.GetFinalizers(), cfFinalizer) {
			log.Info("Finalizer is exist")
			if err := r.finalizeCloudFormation(cfClient, instance); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(instance, cfFinalizer)
			err := r.Client.Update(ctx, instance)
			if err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
	}

	// Add finalizer for this CR
	if !contains(instance.GetFinalizers(), cfFinalizer) {
		if err := r.addFinalizer(log, instance); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Added finalizers")
	}

	exists, err := r.stackExists(cfClient, instance)
	if err != nil {
		log.Info("Cloudformation object is not found in AWS")
	}

	if exists {
		return ctrl.Result{}, r.updateStack(ctx, cfClient, instance)
	}

	return ctrl.Result{}, r.createStack(ctx, cfClient, instance)
}

func (r *CloudFormationReconciler) createStack(ctx context.Context, cfClient cloudformationiface.CloudFormationAPI, cfStack *infrav1alpha1.CloudFormation) error {
	r.Log.WithValues("stack", cfStack.Name).Info("Create Stack")

	hasOwnership, err := r.hasOwnership(cfClient, cfStack)
	if err != nil {
		return err
	}

	if !hasOwnership {
		r.Log.WithValues("stack", cfStack.Name).Info("no ownerhsip")
		return nil
	}

	stackTags, err := r.stackTags(cfStack)
	if err != nil {
		r.Log.WithValues("stack", cfStack.Name).Error(err, "error compiling tags")
		return err
	}

	input := &cloudformation.CreateStackInput{
		// Capabilities: aws.StringSlice(r.defaultCapabilities),
		StackName:    aws.String(cfStack.Name),
		TemplateBody: aws.String(cfStack.Spec.Template),
		Parameters:   r.stackParameters(cfStack),
		Tags:         stackTags,
	}

	if _, err := cfClient.CreateStack(input); err != nil {
		return err
	}

	if err := r.waitWhile(cfClient, cfStack, cloudformation.StackStatusCreateInProgress); err != nil {
		return err
	}
	return r.updateStackStatus(ctx, cfClient, cfStack)
}

func (r *CloudFormationReconciler) updateStack(ctx context.Context, cfClient cloudformationiface.CloudFormationAPI, cfStack *infrav1alpha1.CloudFormation) error {
	r.Log.WithValues("stack", cfStack.Name).Info("Create Stack")

	hasOwnership, err := r.hasOwnership(cfClient, cfStack)
	if err != nil {
		return err
	}

	if !hasOwnership {
		r.Log.WithValues("stack", cfStack.Name).Info("no ownerhsip")
		return nil
	}

	stackTags, err := r.stackTags(cfStack)
	if err != nil {
		r.Log.WithValues("stack", cfStack.Name).Error(err, "error compiling tags")
		return err
	}

	input := &cloudformation.UpdateStackInput{
		// Capabilities: aws.StringSlice(r.defaultCapabilities),
		StackName:    aws.String(cfStack.Name),
		TemplateBody: aws.String(cfStack.Spec.Template),
		Parameters:   r.stackParameters(cfStack),
		Tags:         stackTags,
	}

	if _, err := cfClient.UpdateStack(input); err != nil {
		if strings.Contains(err.Error(), "No updates are to be performed.") {
			r.Log.WithValues("stack", cfStack.Name).Info("stack already updated")
			return nil
		}
		return err
	}

	if err := r.waitWhile(cfClient, cfStack, cloudformation.StackStatusCreateInProgress); err != nil {
		return err
	}
	return r.updateStackStatus(ctx, cfClient, cfStack)
}

func (r *CloudFormationReconciler) deleteStack(cfClient cloudformationiface.CloudFormationAPI, cfStack *infrav1alpha1.CloudFormation) error {
	r.Log.WithValues("stack", cfStack.Name).Info("Delete Stack")

	hasOwnership, err := r.hasOwnership(cfClient, cfStack)
	if err != nil {
		return err
	}

	if !hasOwnership {
		r.Log.WithValues("stack", cfStack.Name).Info("no ownerhsip")
		return nil
	}

	input := &cloudformation.DeleteStackInput{
		StackName: aws.String(cfStack.Name),
	}

	if _, err := cfClient.DeleteStack(input); err != nil {
		return err
	}

	return r.waitWhile(cfClient, cfStack, cloudformation.StackStatusDeleteInProgress)
}

func (r *CloudFormationReconciler) waitWhile(cfClient cloudformationiface.CloudFormationAPI, cfStack *infrav1alpha1.CloudFormation, status string) error {
	for {
		cfs, err := r.getStack(cfClient, cfStack)
		if err != nil {
			if err == ErrStackNotFound {
				return nil
			}
			return err
		}
		current := aws.StringValue(cfs.StackStatus)
		r.Log.WithValues("stack", cfStack.Name).WithValues("status", current).Info("waiting for stack")

		if current == status {
			time.Sleep(time.Second)
			continue
		}

		return nil
	}
}

func (r *CloudFormationReconciler) stackParameters(cfStack *infrav1alpha1.CloudFormation) []*cloudformation.Parameter {
	params := []*cloudformation.Parameter{}
	for k, v := range cfStack.Spec.Parameters {
		params = append(params, &cloudformation.Parameter{
			ParameterKey:   aws.String(k),
			ParameterValue: aws.String(v),
		})
	}
	return params
}

func (r *CloudFormationReconciler) stackTags(cfStack *infrav1alpha1.CloudFormation) ([]*cloudformation.Tag, error) {
	ref, err := r.getObjectReference(cfStack)
	if err != nil {
		return nil, err
	}

	// ownership tags
	tags := []*cloudformation.Tag{
		{
			Key:   aws.String(controllerKey),
			Value: aws.String(controllerValue),
		},
		{
			Key:   aws.String(ownerKey),
			Value: aws.String(string(ref)),
		},
	}

	// default tags
	// for k, v := range r.defaultTags {
	// 	tags = append(tags, &cloudformation.Tag{
	// 		Key:   aws.String(k),
	// 		Value: aws.String(v),
	// 	})
	// }

	// tags specified on the Stack resource
	for k, v := range cfStack.Spec.Tags {
		tags = append(tags, &cloudformation.Tag{
			Key:   aws.String(k),
			Value: aws.String(v),
		})
	}

	return tags, nil
}

func (r *CloudFormationReconciler) hasOwnership(cfClient cloudformationiface.CloudFormationAPI, cfStack *infrav1alpha1.CloudFormation) (bool, error) {
	exists, err := r.stackExists(cfClient, cfStack)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}

	cfs, err := r.getStack(cfClient, cfStack)
	if err != nil {
		return false, err
	}

	for _, tag := range cfs.Tags {
		if aws.StringValue(tag.Key) == controllerKey && aws.StringValue(tag.Value) == controllerValue {
			return true, nil
		}
	}

	return false, nil
}

func (r *CloudFormationReconciler) getObjectReference(owner metav1.Object) (types.UID, error) {
	ro, ok := owner.(runtime.Object)
	if !ok {
		return "", fmt.Errorf("%T is not a runtime.Object, cannot call SetControllerReference", owner)
	}

	gvk, err := apiutil.GVKForObject(ro, r.Scheme)
	if err != nil {
		return "", err
	}

	ref := *metav1.NewControllerRef(owner, schema.GroupVersionKind{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind})
	return ref.UID, nil
}

func (r *CloudFormationReconciler) updateStackStatus(ctx context.Context, cfClient cloudformationiface.CloudFormationAPI, cfStack *infrav1alpha1.CloudFormation) error {
	r.Log.Info("Update stack status")
	cfs, err := r.getStack(cfClient, cfStack)
	if err != nil {
		return err
	}

	stackID := aws.StringValue(cfs.StackId)
	outputs := map[string]string{}
	for _, output := range cfs.Outputs {
		outputs[aws.StringValue(output.OutputKey)] = aws.StringValue(output.OutputValue)
	}

	if stackID != cfStack.Status.StackID || !reflect.DeepEqual(outputs, cfStack.Status.Outputs) {
		cfStack.Status.StackID = stackID
		cfStack.Status.Outputs = outputs
		err := r.Client.Status().Update(ctx, cfStack)
		if err != nil {
			if errors.IsNotFound(err) {
				return nil
			}
			return err
		}
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CloudFormationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1alpha1.CloudFormation{}).
		Complete(r)
}

func (r *CloudFormationReconciler) stackExists(cfClient cloudformationiface.CloudFormationAPI, cfStack *infrav1alpha1.CloudFormation) (bool, error) {
	_, err := r.getStack(cfClient, cfStack)
	if err != nil {
		if err == ErrStackNotFound {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func (r *CloudFormationReconciler) getStack(cfClient cloudformationiface.CloudFormationAPI, cfStack *infrav1alpha1.CloudFormation) (*cloudformation.Stack, error) {
	resp, err := cfClient.DescribeStacks(&cloudformation.DescribeStacksInput{
		StackName: aws.String(cfStack.Name),
	})
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil, ErrStackNotFound
		}
		return nil, err
	}
	if len(resp.Stacks) != 1 {
		return nil, ErrStackNotFound
	}

	return resp.Stacks[0], nil
}

func (r *CloudFormationReconciler) awsClientSession() cloudformationiface.CloudFormationAPI {

	region, ok := os.LookupEnv("REGION")
	if !ok {
		region = "us-east-1"
	}
	var cfClient cloudformationiface.CloudFormationAPI
	sess := session.Must(session.NewSession())
	cfClient = cloudformation.New(sess, &aws.Config{
		Region: aws.String(region),
	})
	return cfClient
}

func (r *CloudFormationReconciler) finalizeCloudFormation(cfClient cloudformationiface.CloudFormationAPI, cfStack *infrav1alpha1.CloudFormation) error {
	if err := r.deleteStack(cfClient, cfStack); err != nil {
		return err
	}
	r.Log.Info("Successfully finalized cloudformation resource")
	return nil
}

func (r *CloudFormationReconciler) addFinalizer(reqLogger logr.Logger, cfStack *infrav1alpha1.CloudFormation) error {
	reqLogger.Info("Adding Finalizer for the Cloudformation resource")
	controllerutil.AddFinalizer(cfStack, cfFinalizer)

	// Update CR
	err := r.Update(context.TODO(), cfStack)
	if err != nil {
		reqLogger.Error(err, "Failed to update Cloudformation with finalizer")
		return err
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
