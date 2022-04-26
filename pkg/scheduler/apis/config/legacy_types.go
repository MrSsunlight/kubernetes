/*
Copyright 2014 The Kubernetes Authors.

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

package config

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Policy describes a struct of a policy resource in api.
// 描述 api 中策略资源的结构
type Policy struct {
	metav1.TypeMeta
	// Holds the information to configure the fit predicate functions.
	// If unspecified, the default predicate functions will be applied.
	// If empty list, all predicates (except the mandatory ones) will be
	// bypassed.
	// 保存用于配置适合的谓词函数的信息。如果未指定，将应用默认的谓词函数。如果是空列表，所有的谓词（除了强制性的）将被绕过。
	Predicates []PredicatePolicy
	// Holds the information to configure the priority functions.
	// If unspecified, the default priority functions will be applied.
	// If empty list, all priority functions will be bypassed.
	// 保存用于配置优先权功能的信息。如果没有指定，将应用默认的优先权功能。如果是空列表，所有的优先级功能将被绕过。
	Priorities []PriorityPolicy
	// Holds the information to communicate with the extender(s)
	// 保存与扩展器进行通信的信息
	Extenders []Extender
	// RequiredDuringScheduling affinity is not symmetric, but there is an implicit PreferredDuringScheduling affinity rule
	// corresponding to every RequiredDuringScheduling affinity rule.
	// HardPodAffinitySymmetricWeight represents the weight of implicit PreferredDuringScheduling affinity rule, in the range 1-100.
	// RequiredDuringScheduling 亲缘关系不是对称的，但是有一个隐式的 PreferredDuringScheduling 亲缘关系规则对应于每个RequiredDuringScheduling亲缘关系规则。
	// HardPodAffinitySymmetricWeight 表示隐式的 PreferredDuringScheduling 亲和性规则的权重，取值范围为1 ~ 100
	HardPodAffinitySymmetricWeight int32

	// When AlwaysCheckAllPredicates is set to true, scheduler checks all
	// the configured predicates even after one or more of them fails.
	// When the flag is set to false, scheduler skips checking the rest
	// of the predicates after it finds one predicate that failed.
	// 当 AlwaysCheckAllPredicates 设置为 true 时，调度程序会检查所有配置的谓词，即使其中一个或多个谓词失败。
	// 当标志设置为 false 时，调度程序在找到一个失败的谓词后跳过检查其余谓词
	AlwaysCheckAllPredicates bool
}

// PredicatePolicy describes a struct of a predicate policy.
// 描述谓词策略的结构
type PredicatePolicy struct {
	// Identifier of the predicate policy
	// For a custom predicate, the name can be user-defined
	// For the Kubernetes provided predicates, the name is the identifier of the pre-defined predicate
	// 谓词策略的标识符,对于自定义谓词名称可以是用户定义的。对于Kubernetes提供的谓词，名称是预定义谓词的标识符
	Name string
	// Holds the parameters to configure the given predicate
	// 保存参数以配置给定的谓词
	Argument *PredicateArgument
}

// PriorityPolicy describes a struct of a priority policy.
// 描述优先级策略的结构
type PriorityPolicy struct {
	// Identifier of the priority policy
	// For a custom priority, the name can be user-defined
	// For the Kubernetes provided priority functions, the name is the identifier of the pre-defined priority function
	// 优先级策略标识符, 对于自定义优先级名称可以自定义 对于Kubernetes提供的优先级函数，名称是预定义的优先级函数的标识符
	Name string
	// The numeric multiplier for the node scores that the priority function generates
	// The weight should be a positive integer
	// 优先级函数生成的节点分数的数字乘数(权重应该是正整数)
	Weight int64
	// Holds the parameters to configure the given priority function
	// 保存配置给定优先级函数的参数
	Argument *PriorityArgument
}

// PredicateArgument represents the arguments to configure predicate functions in scheduler policy configuration.
// Only one of its members may be specified
type PredicateArgument struct {
	// The predicate that provides affinity for pods belonging to a service
	// It uses a label to identify nodes that belong to the same "group"
	ServiceAffinity *ServiceAffinity
	// The predicate that checks whether a particular node has a certain label
	// defined or not, regardless of value
	LabelsPresence *LabelsPresence
}

// PriorityArgument represents the arguments to configure priority functions in scheduler policy configuration.
// Only one of its members may be specified
type PriorityArgument struct {
	// The priority function that ensures a good spread (anti-affinity) for pods belonging to a service
	// It uses a label to identify nodes that belong to the same "group"
	ServiceAntiAffinity *ServiceAntiAffinity
	// The priority function that checks whether a particular node has a certain label
	// defined or not, regardless of value
	LabelPreference *LabelPreference
	// The RequestedToCapacityRatio priority function is parametrized with function shape.
	RequestedToCapacityRatioArguments *RequestedToCapacityRatioArguments
}

// ServiceAffinity holds the parameters that are used to configure the corresponding predicate in scheduler policy configuration.
type ServiceAffinity struct {
	// The list of labels that identify node "groups"
	// All of the labels should match for the node to be considered a fit for hosting the pod
	Labels []string
}

// LabelsPresence holds the parameters that are used to configure the corresponding predicate in scheduler policy configuration.
type LabelsPresence struct {
	// The list of labels that identify node "groups"
	// All of the labels should be either present (or absent) for the node to be considered a fit for hosting the pod
	Labels []string
	// The boolean flag that indicates whether the labels should be present or absent from the node
	Presence bool
}

// ServiceAntiAffinity holds the parameters that are used to configure the corresponding priority function
type ServiceAntiAffinity struct {
	// Used to identify node "groups"
	Label string
}

// LabelPreference holds the parameters that are used to configure the corresponding priority function
type LabelPreference struct {
	// Used to identify node "groups"
	Label string
	// This is a boolean flag
	// If true, higher priority is given to nodes that have the label
	// If false, higher priority is given to nodes that do not have the label
	Presence bool
}

// RequestedToCapacityRatioArguments holds arguments specific to RequestedToCapacityRatio priority function.
type RequestedToCapacityRatioArguments struct {
	// Array of point defining priority function shape.
	Shape     []UtilizationShapePoint `json:"shape"`
	Resources []ResourceSpec          `json:"resources,omitempty"`
}
