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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// CloudFormationSpec defines the desired state of CloudFormation
type CloudFormationSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Parameters is key value pair to pass to cloud formation template
	Parameters map[string]string `json:"parameters,omitempty"`
	// Tags is  key value pair to pass to cloud formation template
	Tags map[string]string `json:"tags,omitempty"`
	// Template is cloud formation temple body, this value is required in case TemplateURL is empty
	Template string `json:"template,omitempty"`
	// TemplateURL is existing Cloudformation URL
	TemplateURL string `json:"templateUrl,omitempty"`
	// DelegateRoleARN is used in case operator service account role has not have all required roles in order to execute CF Stack
	DelegateRoleARN string `json:"delegateRoleArn,omitempty"`
}

// CloudFormationStatus defines the observed state of CloudFormation
type CloudFormationStatus struct {
	// StackID is generated unique identifier by AWS Cloud formation
	StackID string `json:"stackID"`
	// Map of outputs of Cloud Formation Stack
	Outputs map[string]string `json:"outputs"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// CloudFormation is the Schema for the cloudformations API
type CloudFormation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CloudFormationSpec   `json:"spec,omitempty"`
	Status CloudFormationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CloudFormationList contains a list of CloudFormation
type CloudFormationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudFormation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CloudFormation{}, &CloudFormationList{})
}
