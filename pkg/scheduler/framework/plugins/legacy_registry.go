/*
Copyright 2019 The Kubernetes Authors.

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

package plugins

import (
	"k8s.io/apimachinery/pkg/util/sets"

	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/imagelocality"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/interpodaffinity"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeaffinity"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodelabel"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodename"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeports"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodepreferavoidpods"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/noderesources"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodeunschedulable"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodevolumelimits"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/podtopologyspread"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/selectorspread"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/serviceaffinity"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/tainttoleration"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/volumebinding"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/volumerestrictions"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/volumezone"
)

// 在 createFromConfig 初始化注册  定义了优先级函数
const (
	// EqualPriority defines the name of prioritizer function that gives an equal weight of one to all nodes.
	// 定义优先排序器函数的名称，该函数为所有节点赋予相等的权重1
	EqualPriority = "EqualPriority"
	// MostRequestedPriority defines the name of prioritizer function that gives used nodes higher priority.
	// 定义优先排序器函数的名称，该函数为使用的节点提供更高的优先级
	MostRequestedPriority = "MostRequestedPriority"
	// RequestedToCapacityRatioPriority defines the name of RequestedToCapacityRatioPriority.
	// 定义 requestdtocapacityratiopriority 的名称
	RequestedToCapacityRatioPriority = "RequestedToCapacityRatioPriority"
	// SelectorSpreadPriority defines the name of prioritizer function that spreads pods by minimizing
	// the number of pods (belonging to the same service or replication controller) on the same node.
	// 定义优先级函数的名称，通过最小化同一节点上的 pod（属于同一服务或复制控制器）的数量来扩展 pod
	SelectorSpreadPriority = "SelectorSpreadPriority"
	// ServiceSpreadingPriority is largely replaced by "SelectorSpreadPriority".
	// 被 "SelectorSpreadPriority" 取代
	ServiceSpreadingPriority = "ServiceSpreadingPriority"
	// InterPodAffinityPriority defines the name of prioritizer function that decides which pods should or
	// should not be placed in the same topological domain as some other pods.
	// 定义优先级函数的名称，该函数决定哪些 pod 应该或不应该放置在与其他一些 pod 相同的拓扑域中
	InterPodAffinityPriority = "InterPodAffinityPriority"
	// LeastRequestedPriority defines the name of prioritizer function that prioritize nodes by least
	// requested utilization.
	// 定义了优先级函数的名称，该函数按照最小请求利用率对节点进行优先级排序。
	LeastRequestedPriority = "LeastRequestedPriority"
	// BalancedResourceAllocation defines the name of prioritizer function that prioritizes nodes
	// to help achieve balanced resource usage.
	// 定义优先级函数的名称，该函数对节点进行优先级排序以帮助实现平衡的资源使用
	BalancedResourceAllocation = "BalancedResourceAllocation"
	// NodePreferAvoidPodsPriority defines the name of prioritizer function that priorities nodes according to
	// the node annotation "scheduler.alpha.kubernetes.io/preferAvoidPods".
	// 定义优先级函数的名称，该函数根据节点注解 "scheduler.alpha.kubernetes.io/preferAvoidPods" 确定节点的优先级。
	NodePreferAvoidPodsPriority = "NodePreferAvoidPodsPriority"
	// NodeAffinityPriority defines the name of prioritizer function that prioritizes nodes which have labels
	// matching NodeAffinity.
	// 定义优先级函数的名称，该函数对具有与 NodeAffinity 匹配的标签的节点进行优先级排序
	NodeAffinityPriority = "NodeAffinityPriority"
	// TaintTolerationPriority defines the name of prioritizer function that prioritizes nodes that marked
	// with taint which pod can tolerate.
	// 定义优先级函数的名称，该函数对标记为污染(taint)的节点进行优先级排序
	TaintTolerationPriority = "TaintTolerationPriority"
	// ImageLocalityPriority defines the name of prioritizer function that prioritizes nodes that have images
	// requested by the pod present.
	// 定义优先级函数的名称，该函数对存在 pod 请求的镜像的节点进行优先级排序
	ImageLocalityPriority = "ImageLocalityPriority"
	// EvenPodsSpreadPriority defines the name of prioritizer function that prioritizes nodes
	// which have pods and labels matching the incoming pod's topologySpreadConstraints.
	// 定义优先排序器函数的名称，该函数对具有与传入pod的拓扑结构(topologySpreadConstraints)匹配的pod和标签的节点进行优先排序
	EvenPodsSpreadPriority = "EvenPodsSpreadPriority"
)

// 谓词(predicate)宏定义
const (
	// MatchInterPodAffinityPred defines the name of predicate MatchInterPodAffinity.
	MatchInterPodAffinityPred = "MatchInterPodAffinity"
	// CheckVolumeBindingPred defines the name of predicate CheckVolumeBinding.
	CheckVolumeBindingPred = "CheckVolumeBinding"
	// GeneralPred defines the name of predicate GeneralPredicates.
	GeneralPred = "GeneralPredicates"
	// HostNamePred defines the name of predicate HostName.
	// 定义谓词 HostName 的名称
	HostNamePred = "HostName"
	// PodFitsHostPortsPred defines the name of predicate PodFitsHostPorts.
	// 定义谓词 PodFitsHostPorts 的名称
	PodFitsHostPortsPred = "PodFitsHostPorts"
	// MatchNodeSelectorPred defines the name of predicate MatchNodeSelector.
	MatchNodeSelectorPred = "MatchNodeSelector"
	// PodFitsResourcesPred defines the name of predicate PodFitsResources.
	PodFitsResourcesPred = "PodFitsResources"
	// NoDiskConflictPred defines the name of predicate NoDiskConflict.
	NoDiskConflictPred = "NoDiskConflict"
	// PodToleratesNodeTaintsPred defines the name of predicate PodToleratesNodeTaints.
	PodToleratesNodeTaintsPred = "PodToleratesNodeTaints"
	// CheckNodeUnschedulablePred defines the name of predicate CheckNodeUnschedulablePredicate.
	CheckNodeUnschedulablePred = "CheckNodeUnschedulable"
	// CheckNodeLabelPresencePred defines the name of predicate CheckNodeLabelPresence.
	CheckNodeLabelPresencePred = "CheckNodeLabelPresence"
	// CheckServiceAffinityPred defines the name of predicate checkServiceAffinity.
	CheckServiceAffinityPred = "CheckServiceAffinity"
	// MaxEBSVolumeCountPred defines the name of predicate MaxEBSVolumeCount.
	// DEPRECATED
	// All cloudprovider specific predicates are deprecated in favour of MaxCSIVolumeCountPred.
	MaxEBSVolumeCountPred = "MaxEBSVolumeCount"
	// MaxGCEPDVolumeCountPred defines the name of predicate MaxGCEPDVolumeCount.
	// DEPRECATED
	// All cloudprovider specific predicates are deprecated in favour of MaxCSIVolumeCountPred.
	MaxGCEPDVolumeCountPred = "MaxGCEPDVolumeCount"
	// MaxAzureDiskVolumeCountPred defines the name of predicate MaxAzureDiskVolumeCount.
	// DEPRECATED
	// All cloudprovider specific predicates are deprecated in favour of MaxCSIVolumeCountPred.
	MaxAzureDiskVolumeCountPred = "MaxAzureDiskVolumeCount"
	// MaxCinderVolumeCountPred defines the name of predicate MaxCinderDiskVolumeCount.
	// DEPRECATED
	// All cloudprovider specific predicates are deprecated in favour of MaxCSIVolumeCountPred.
	MaxCinderVolumeCountPred = "MaxCinderVolumeCount"
	// MaxCSIVolumeCountPred defines the predicate that decides how many CSI volumes should be attached.
	MaxCSIVolumeCountPred = "MaxCSIVolumeCountPred"
	// NoVolumeZoneConflictPred defines the name of predicate NoVolumeZoneConflict.
	NoVolumeZoneConflictPred = "NoVolumeZoneConflict"
	// EvenPodsSpreadPred defines the name of predicate EvenPodsSpread.
	EvenPodsSpreadPred = "EvenPodsSpread"
)

// PredicateOrdering returns the ordering of predicate execution.
// 返回谓词执行的顺序
func PredicateOrdering() []string {
	return []string{CheckNodeUnschedulablePred,
		GeneralPred, HostNamePred, PodFitsHostPortsPred,
		MatchNodeSelectorPred, PodFitsResourcesPred, NoDiskConflictPred,
		PodToleratesNodeTaintsPred, CheckNodeLabelPresencePred,
		CheckServiceAffinityPred, MaxEBSVolumeCountPred, MaxGCEPDVolumeCountPred, MaxCSIVolumeCountPred,
		MaxAzureDiskVolumeCountPred, MaxCinderVolumeCountPred, CheckVolumeBindingPred, NoVolumeZoneConflictPred,
		EvenPodsSpreadPred, MatchInterPodAffinityPred}
}

// LegacyRegistry is used to store current state of registered predicates and priorities.
// 用于存储已注册谓词和优先级的当前状态
type LegacyRegistry struct {
	// maps that associate predicates/priorities with framework plugin configurations.
	// 将谓词/优先级与框架插件配置相关联的映射
	PredicateToConfigProducer map[string]ConfigProducer // 谓词插件映射表
	PriorityToConfigProducer  map[string]ConfigProducer // 优先选择插件映射表
	// predicates that will always be configured.
	// 必须配置的谓词
	MandatoryPredicates sets.String
	// predicates and priorities that will be used if either was set to nil in a
	// given v1.Policy configuration.
	// 如果在给定的v1.Policy配置中任何一个被设置为nil，将使用默认的谓词和优先级。
	DefaultPredicates sets.String
	DefaultPriorities map[string]int64
}

// ConfigProducerArgs contains arguments that are passed to the producer.
// As we add more predicates/priorities to framework plugins mappings, more arguments
// may be added here.
// 包含传递给生产者的参数。当在框架插件的映射中添加更多的谓词/优先级时，可能会在这里添加更多的参数
type ConfigProducerArgs struct {
	// Weight used for priority functions.
	// 用于优先级(priority)函数
	Weight int32
	// NodeLabelArgs is the args for the NodeLabel plugin.
	// NodeLabel 插件的参数
	NodeLabelArgs *config.NodeLabelArgs
	// RequestedToCapacityRatioArgs is the args for the RequestedToCapacityRatio plugin.
	// requestdtocapacityratio 插件的参数
	RequestedToCapacityRatioArgs *config.RequestedToCapacityRatioArgs
	// ServiceAffinityArgs is the args for the ServiceAffinity plugin.
	// ServiceAffinity 插件的参数
	ServiceAffinityArgs *config.ServiceAffinityArgs
	// NodeResourcesFitArgs is the args for the NodeResources fit filter.
	// NodeResources 适配过滤器的参数
	NodeResourcesFitArgs *config.NodeResourcesFitArgs
	// InterPodAffinityArgs is the args for InterPodAffinity plugin
	// InterPodAffinity 插件的参数
	InterPodAffinityArgs *config.InterPodAffinityArgs
}

// ConfigProducer returns the set of plugins and their configuration for a
// predicate/priority given the args.
// 在给定参数的情况下返回谓词/优先级的插件集及其配置; 在 NewLegacyRegistry() 的 registry.registerPredicateConfigProducer() 中被赋值
type ConfigProducer func(args ConfigProducerArgs) (config.Plugins, []config.PluginConfig)

// NewLegacyRegistry returns a legacy algorithm registry of predicates and priorities.
// 返回谓词和优先级的遗留算法注册表
func NewLegacyRegistry() *LegacyRegistry {
	registry := &LegacyRegistry{
		// MandatoryPredicates the set of keys for predicates that the scheduler will
		// be configured with all the time.
		// 调度程序将始终配置的谓词的键集
		MandatoryPredicates: sets.NewString(
			// Pod 容忍节点污染
			PodToleratesNodeTaintsPred,
			// 检查节点不可调度
			CheckNodeUnschedulablePred,
		),

		// Used as the default set of predicates if Policy was specified, but predicates was nil.
		// 如果指定了 Policy，但 predicates 为 nil，则作为默认的 predicates 集使用。
		DefaultPredicates: sets.NewString(
			NoVolumeZoneConflictPred,
			MaxEBSVolumeCountPred,
			MaxGCEPDVolumeCountPred,
			MaxAzureDiskVolumeCountPred,
			MaxCSIVolumeCountPred,
			MatchInterPodAffinityPred,
			NoDiskConflictPred,
			GeneralPred,
			PodToleratesNodeTaintsPred,
			CheckVolumeBindingPred,
			CheckNodeUnschedulablePred,
			EvenPodsSpreadPred,
		),

		// Used as the default set of predicates if Policy was specified, but priorities was nil.
		// 如果指定了 Policy，但 priorities 为 nil，则作为默认的 priorities 集使用
		DefaultPriorities: map[string]int64{
			SelectorSpreadPriority:      1,
			InterPodAffinityPriority:    1,
			LeastRequestedPriority:      1,
			BalancedResourceAllocation:  1,
			NodePreferAvoidPodsPriority: 10000,
			NodeAffinityPriority:        1,
			TaintTolerationPriority:     1,
			ImageLocalityPriority:       1,
			EvenPodsSpreadPriority:      2,
		},

		PredicateToConfigProducer: make(map[string]ConfigProducer),
		PriorityToConfigProducer:  make(map[string]ConfigProducer),
	}

	// 设置谓词/优选中各个插件的权重占比
	// 谓词注册
	// 常规
	registry.registerPredicateConfigProducer(GeneralPred,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			// GeneralPredicate is a combination of predicates.
			plugins.Filter = appendToPluginSet(plugins.Filter, noderesources.FitName, nil)
			plugins.PreFilter = appendToPluginSet(plugins.PreFilter, noderesources.FitName, nil)
			if args.NodeResourcesFitArgs != nil {
				pluginConfig = append(pluginConfig,
					config.PluginConfig{Name: noderesources.FitName, Args: args.NodeResourcesFitArgs})
			}
			plugins.Filter = appendToPluginSet(plugins.Filter, nodename.Name, nil)
			plugins.Filter = appendToPluginSet(plugins.Filter, nodeports.Name, nil)
			plugins.PreFilter = appendToPluginSet(plugins.PreFilter, nodeports.Name, nil)
			plugins.Filter = appendToPluginSet(plugins.Filter, nodeaffinity.Name, nil)
			return
		})
	// 污点
	registry.registerPredicateConfigProducer(PodToleratesNodeTaintsPred,
		func(_ ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Filter = appendToPluginSet(plugins.Filter, tainttoleration.Name, nil)
			return
		})
	// 资源
	registry.registerPredicateConfigProducer(PodFitsResourcesPred,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Filter = appendToPluginSet(plugins.Filter, noderesources.FitName, nil)
			plugins.PreFilter = appendToPluginSet(plugins.PreFilter, noderesources.FitName, nil)
			if args.NodeResourcesFitArgs != nil {
				pluginConfig = append(pluginConfig,
					config.PluginConfig{Name: noderesources.FitName, Args: args.NodeResourcesFitArgs})
			}
			return
		})
	// 主机名
	registry.registerPredicateConfigProducer(HostNamePred,
		func(_ ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Filter = appendToPluginSet(plugins.Filter, nodename.Name, nil)
			return
		})
	// 主机端口
	registry.registerPredicateConfigProducer(PodFitsHostPortsPred,
		func(_ ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Filter = appendToPluginSet(plugins.Filter, nodeports.Name, nil)
			plugins.PreFilter = appendToPluginSet(plugins.PreFilter, nodeports.Name, nil)
			return
		})
	// 匹配节点
	registry.registerPredicateConfigProducer(MatchNodeSelectorPred,
		func(_ ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Filter = appendToPluginSet(plugins.Filter, nodeaffinity.Name, nil)
			return
		})
	// 不可调度节点
	registry.registerPredicateConfigProducer(CheckNodeUnschedulablePred,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Filter = appendToPluginSet(plugins.Filter, nodeunschedulable.Name, nil)
			return
		})
	// 磁盘
	registry.registerPredicateConfigProducer(CheckVolumeBindingPred,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.PreFilter = appendToPluginSet(plugins.PreFilter, volumebinding.Name, nil)
			plugins.Filter = appendToPluginSet(plugins.Filter, volumebinding.Name, nil)
			plugins.Reserve = appendToPluginSet(plugins.Reserve, volumebinding.Name, nil)
			plugins.PreBind = appendToPluginSet(plugins.PreBind, volumebinding.Name, nil)
			return
		})
	// 不存在磁盘冲突
	registry.registerPredicateConfigProducer(NoDiskConflictPred,
		func(_ ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Filter = appendToPluginSet(plugins.Filter, volumerestrictions.Name, nil)
			return
		})
	// 无卷区冲突
	registry.registerPredicateConfigProducer(NoVolumeZoneConflictPred,
		func(_ ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Filter = appendToPluginSet(plugins.Filter, volumezone.Name, nil)
			return
		})
	// 最大的CSI容量数
	registry.registerPredicateConfigProducer(MaxCSIVolumeCountPred,
		func(_ ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Filter = appendToPluginSet(plugins.Filter, nodevolumelimits.CSIName, nil)
			return
		})
	registry.registerPredicateConfigProducer(MaxEBSVolumeCountPred,
		func(_ ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Filter = appendToPluginSet(plugins.Filter, nodevolumelimits.EBSName, nil)
			return
		})
	registry.registerPredicateConfigProducer(MaxGCEPDVolumeCountPred,
		func(_ ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Filter = appendToPluginSet(plugins.Filter, nodevolumelimits.GCEPDName, nil)
			return
		})
	registry.registerPredicateConfigProducer(MaxAzureDiskVolumeCountPred,
		func(_ ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Filter = appendToPluginSet(plugins.Filter, nodevolumelimits.AzureDiskName, nil)
			return
		})
	registry.registerPredicateConfigProducer(MaxCinderVolumeCountPred,
		func(_ ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Filter = appendToPluginSet(plugins.Filter, nodevolumelimits.CinderName, nil)
			return
		})
	// 匹配 Pod 间亲和力
	registry.registerPredicateConfigProducer(MatchInterPodAffinityPred,
		func(_ ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Filter = appendToPluginSet(plugins.Filter, interpodaffinity.Name, nil)
			plugins.PreFilter = appendToPluginSet(plugins.PreFilter, interpodaffinity.Name, nil)
			return
		})
	// 检查节点标签是否存在
	registry.registerPredicateConfigProducer(CheckNodeLabelPresencePred,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Filter = appendToPluginSet(plugins.Filter, nodelabel.Name, nil)
			if args.NodeLabelArgs != nil {
				pluginConfig = append(pluginConfig,
					config.PluginConfig{Name: nodelabel.Name, Args: args.NodeLabelArgs})
			}
			return
		})
	// 检查服务关联性
	registry.registerPredicateConfigProducer(CheckServiceAffinityPred,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Filter = appendToPluginSet(plugins.Filter, serviceaffinity.Name, nil)
			if args.ServiceAffinityArgs != nil {
				pluginConfig = append(pluginConfig,
					config.PluginConfig{Name: serviceaffinity.Name, Args: args.ServiceAffinityArgs})
			}
			plugins.PreFilter = appendToPluginSet(plugins.PreFilter, serviceaffinity.Name, nil)
			return
		})
	// pod 水平扩展
	registry.registerPredicateConfigProducer(EvenPodsSpreadPred,
		func(_ ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.PreFilter = appendToPluginSet(plugins.PreFilter, podtopologyspread.Name, nil)
			plugins.Filter = appendToPluginSet(plugins.Filter, podtopologyspread.Name, nil)
			return
		})

	// 优先级注册
	// Register Priorities.
	// 通过最小化同一节点上的 pod（属于同一服务或复制控制器）的数量来扩展 pod
	registry.registerPriorityConfigProducer(SelectorSpreadPriority,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Score = appendToPluginSet(plugins.Score, selectorspread.Name, &args.Weight)
			plugins.PreScore = appendToPluginSet(plugins.PreScore, selectorspread.Name, nil)
			return
		})
	// 对标记为污染(taint)的节点进行优先级排序
	registry.registerPriorityConfigProducer(TaintTolerationPriority,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.PreScore = appendToPluginSet(plugins.PreScore, tainttoleration.Name, nil)
			plugins.Score = appendToPluginSet(plugins.Score, tainttoleration.Name, &args.Weight)
			return
		})
	// 对具有与 NodeAffinity 匹配的标签的节点进行优先级排序
	registry.registerPriorityConfigProducer(NodeAffinityPriority,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Score = appendToPluginSet(plugins.Score, nodeaffinity.Name, &args.Weight)
			return
		})
	// 对存在 pod 请求的镜像的节点进行优先级排序
	registry.registerPriorityConfigProducer(ImageLocalityPriority,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Score = appendToPluginSet(plugins.Score, imagelocality.Name, &args.Weight)
			return
		})
	// 决定哪些 pod 应该或不应该放置在与其他一些 pod 相同的拓扑域中
	registry.registerPriorityConfigProducer(InterPodAffinityPriority,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.PreScore = appendToPluginSet(plugins.PreScore, interpodaffinity.Name, nil)
			plugins.Score = appendToPluginSet(plugins.Score, interpodaffinity.Name, &args.Weight)
			if args.InterPodAffinityArgs != nil {
				pluginConfig = append(pluginConfig,
					config.PluginConfig{Name: interpodaffinity.Name, Args: args.InterPodAffinityArgs})
			}
			return
		})
	// 根据节点注解 "scheduler.alpha.kubernetes.io/preferAvoidPods" 确定节点的优先级
	registry.registerPriorityConfigProducer(NodePreferAvoidPodsPriority,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Score = appendToPluginSet(plugins.Score, nodepreferavoidpods.Name, &args.Weight)
			return
		})
	// 使用的节点提供更高的优先
	registry.registerPriorityConfigProducer(MostRequestedPriority,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Score = appendToPluginSet(plugins.Score, noderesources.MostAllocatedName, &args.Weight)
			return
		})
	// 对节点进行优先级排序以帮助实现平衡的资源使用
	registry.registerPriorityConfigProducer(BalancedResourceAllocation,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Score = appendToPluginSet(plugins.Score, noderesources.BalancedAllocationName, &args.Weight)
			return
		})
	// 按照最小请求利用率对节点进行优先级排序
	registry.registerPriorityConfigProducer(LeastRequestedPriority,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Score = appendToPluginSet(plugins.Score, noderesources.LeastAllocatedName, &args.Weight)
			return
		})
	// 请求容量比
	registry.registerPriorityConfigProducer(noderesources.RequestedToCapacityRatioName,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.Score = appendToPluginSet(plugins.Score, noderesources.RequestedToCapacityRatioName, &args.Weight)
			if args.RequestedToCapacityRatioArgs != nil {
				pluginConfig = append(pluginConfig,
					config.PluginConfig{Name: noderesources.RequestedToCapacityRatioName, Args: args.RequestedToCapacityRatioArgs})
			}
			return
		})
	// 节点标签
	registry.registerPriorityConfigProducer(nodelabel.Name,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			// If there are n LabelPreference priorities in the policy, the weight for the corresponding
			// score plugin is n*weight (note that the validation logic verifies that all LabelPreference
			// priorities specified in Policy have the same weight).
			weight := args.Weight * int32(len(args.NodeLabelArgs.PresentLabelsPreference)+len(args.NodeLabelArgs.AbsentLabelsPreference))
			plugins.Score = appendToPluginSet(plugins.Score, nodelabel.Name, &weight)
			if args.NodeLabelArgs != nil {
				pluginConfig = append(pluginConfig,
					config.PluginConfig{Name: nodelabel.Name, Args: args.NodeLabelArgs})
			}
			return
		})
	// 服务亲和性
	registry.registerPriorityConfigProducer(serviceaffinity.Name,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			// If there are n ServiceAffinity priorities in the policy, the weight for the corresponding
			// score plugin is n*weight (note that the validation logic verifies that all ServiceAffinity
			// priorities specified in Policy have the same weight).
			weight := args.Weight * int32(len(args.ServiceAffinityArgs.AntiAffinityLabelsPreference))
			plugins.Score = appendToPluginSet(plugins.Score, serviceaffinity.Name, &weight)
			if args.ServiceAffinityArgs != nil {
				pluginConfig = append(pluginConfig,
					config.PluginConfig{Name: serviceaffinity.Name, Args: args.ServiceAffinityArgs})
			}
			return
		})
	// 对具有与传入pod的拓扑结构(topologySpreadConstraints)匹配的pod和标签的节点进行优先排序
	registry.registerPriorityConfigProducer(EvenPodsSpreadPriority,
		func(args ConfigProducerArgs) (plugins config.Plugins, pluginConfig []config.PluginConfig) {
			plugins.PreScore = appendToPluginSet(plugins.PreScore, podtopologyspread.Name, nil)
			plugins.Score = appendToPluginSet(plugins.Score, podtopologyspread.Name, &args.Weight)
			return
		})

	return registry
}

// registers a config producer for a predicate.
// 给对应谓词注册对应的生产者
func (lr *LegacyRegistry) registerPredicateConfigProducer(name string, producer ConfigProducer) {
	if _, exist := lr.PredicateToConfigProducer[name]; exist {
		klog.Fatalf("already registered %q", name)
	}
	lr.PredicateToConfigProducer[name] = producer
}

// registers a framework config producer for a priority.
func (lr *LegacyRegistry) registerPriorityConfigProducer(name string, producer ConfigProducer) {
	if _, exist := lr.PriorityToConfigProducer[name]; exist {
		klog.Fatalf("already registered %q", name)
	}
	lr.PriorityToConfigProducer[name] = producer
}

// 设置插件集合中对应插件的权重占比
func appendToPluginSet(set *config.PluginSet, name string, weight *int32) *config.PluginSet {
	if set == nil {
		set = &config.PluginSet{}
	}
	cfg := config.Plugin{Name: name}
	if weight != nil {
		cfg.Weight = *weight
	}
	set.Enabled = append(set.Enabled, cfg)
	return set
}

// ProcessPredicatePolicy given a PredicatePolicy, return the plugin name implementing the predicate and update
// the ConfigProducerArgs if necessary.
// 给定 PredicatePolicy，返回实现谓词的插件名称，并在必要时更新 ConfigProducerArgs
func (lr *LegacyRegistry) ProcessPredicatePolicy(policy config.PredicatePolicy, pluginArgs *ConfigProducerArgs) string {
	validatePredicateOrDie(policy)

	predicateName := policy.Name
	if policy.Name == "PodFitsPorts" {
		// For compatibility reasons, "PodFitsPorts" as a key is still supported.
		predicateName = PodFitsHostPortsPred
	}

	if _, ok := lr.PredicateToConfigProducer[predicateName]; ok {
		// checking to see if a pre-defined predicate is requested
		klog.V(2).Infof("Predicate type %s already registered, reusing.", policy.Name)
		return predicateName
	}

	if policy.Argument == nil || (policy.Argument.ServiceAffinity == nil &&
		policy.Argument.LabelsPresence == nil) {
		klog.Fatalf("Invalid configuration: Predicate type not found for %q", policy.Name)
	}

	// generate the predicate function, if a custom type is requested
	if policy.Argument.ServiceAffinity != nil {
		// map LabelsPresence policy to ConfigProducerArgs that's used to configure the ServiceAffinity plugin.
		if pluginArgs.ServiceAffinityArgs == nil {
			pluginArgs.ServiceAffinityArgs = &config.ServiceAffinityArgs{}
		}
		pluginArgs.ServiceAffinityArgs.AffinityLabels = append(pluginArgs.ServiceAffinityArgs.AffinityLabels, policy.Argument.ServiceAffinity.Labels...)

		// We use the ServiceAffinity predicate name for all ServiceAffinity custom predicates.
		// It may get called multiple times but we essentially only register one instance of ServiceAffinity predicate.
		// This name is then used to find the registered plugin and run the plugin instead of the predicate.
		predicateName = CheckServiceAffinityPred
	}

	if policy.Argument.LabelsPresence != nil {
		// Map LabelPresence policy to ConfigProducerArgs that's used to configure the NodeLabel plugin.
		if pluginArgs.NodeLabelArgs == nil {
			pluginArgs.NodeLabelArgs = &config.NodeLabelArgs{}
		}
		if policy.Argument.LabelsPresence.Presence {
			pluginArgs.NodeLabelArgs.PresentLabels = append(pluginArgs.NodeLabelArgs.PresentLabels, policy.Argument.LabelsPresence.Labels...)
		} else {
			pluginArgs.NodeLabelArgs.AbsentLabels = append(pluginArgs.NodeLabelArgs.AbsentLabels, policy.Argument.LabelsPresence.Labels...)
		}

		// We use the CheckNodeLabelPresencePred predicate name for all kNodeLabel custom predicates.
		// It may get called multiple times but we essentially only register one instance of NodeLabel predicate.
		// This name is then used to find the registered plugin and run the plugin instead of the predicate.
		predicateName = CheckNodeLabelPresencePred

	}
	return predicateName
}

// ProcessPriorityPolicy given a PriorityPolicy, return the plugin name implementing the priority and update
// the ConfigProducerArgs if necessary.
// 给定一个 PriorityPolicy，返回实现优先级的插件名称，并在必要时更新 ConfigProducerArgs
func (lr *LegacyRegistry) ProcessPriorityPolicy(policy config.PriorityPolicy, configProducerArgs *ConfigProducerArgs) string {
	// 验证优选有效性
	validatePriorityOrDie(policy)

	priorityName := policy.Name
	if policy.Name == ServiceSpreadingPriority {
		// For compatibility reasons, "ServiceSpreadingPriority" as a key is still supported.
		priorityName = SelectorSpreadPriority
	}

	if _, ok := lr.PriorityToConfigProducer[priorityName]; ok {
		klog.V(2).Infof("Priority type %s already registered, reusing.", priorityName)
		return priorityName
	}

	// generate the priority function, if a custom priority is requested
	if policy.Argument == nil ||
		(policy.Argument.ServiceAntiAffinity == nil &&
			policy.Argument.RequestedToCapacityRatioArguments == nil &&
			policy.Argument.LabelPreference == nil) {
		klog.Fatalf("Invalid configuration: Priority type not found for %q", priorityName)
	}

	if policy.Argument.ServiceAntiAffinity != nil {
		// We use the ServiceAffinity plugin name for all ServiceAffinity custom priorities.
		// It may get called multiple times but we essentially only register one instance of
		// ServiceAffinity priority.
		// This name is then used to find the registered plugin and run the plugin instead of the priority.
		priorityName = serviceaffinity.Name
		if configProducerArgs.ServiceAffinityArgs == nil {
			configProducerArgs.ServiceAffinityArgs = &config.ServiceAffinityArgs{}
		}
		configProducerArgs.ServiceAffinityArgs.AntiAffinityLabelsPreference = append(
			configProducerArgs.ServiceAffinityArgs.AntiAffinityLabelsPreference,
			policy.Argument.ServiceAntiAffinity.Label,
		)
	}

	if policy.Argument.LabelPreference != nil {
		// We use the NodeLabel plugin name for all NodeLabel custom priorities.
		// It may get called multiple times but we essentially only register one instance of NodeLabel priority.
		// This name is then used to find the registered plugin and run the plugin instead of the priority.
		priorityName = nodelabel.Name
		if configProducerArgs.NodeLabelArgs == nil {
			configProducerArgs.NodeLabelArgs = &config.NodeLabelArgs{}
		}
		if policy.Argument.LabelPreference.Presence {
			configProducerArgs.NodeLabelArgs.PresentLabelsPreference = append(
				configProducerArgs.NodeLabelArgs.PresentLabelsPreference,
				policy.Argument.LabelPreference.Label,
			)
		} else {
			configProducerArgs.NodeLabelArgs.AbsentLabelsPreference = append(
				configProducerArgs.NodeLabelArgs.AbsentLabelsPreference,
				policy.Argument.LabelPreference.Label,
			)
		}
	}

	if policy.Argument.RequestedToCapacityRatioArguments != nil {
		policyArgs := policy.Argument.RequestedToCapacityRatioArguments
		args := &config.RequestedToCapacityRatioArgs{}

		args.Shape = make([]config.UtilizationShapePoint, len(policyArgs.Shape))
		for i, s := range policyArgs.Shape {
			args.Shape[i] = config.UtilizationShapePoint{
				Utilization: s.Utilization,
				Score:       s.Score,
			}
		}

		args.Resources = make([]config.ResourceSpec, len(policyArgs.Resources))
		for i, r := range policyArgs.Resources {
			args.Resources[i] = config.ResourceSpec{
				Name:   r.Name,
				Weight: r.Weight,
			}
		}

		configProducerArgs.RequestedToCapacityRatioArgs = args

		// We do not allow specifying the name for custom plugins, see #83472
		priorityName = noderesources.RequestedToCapacityRatioName
	}

	return priorityName
}

// 验证谓词的有效性
func validatePredicateOrDie(predicate config.PredicatePolicy) {
	if predicate.Argument != nil {
		numArgs := 0
		if predicate.Argument.ServiceAffinity != nil {
			numArgs++
		}
		if predicate.Argument.LabelsPresence != nil {
			numArgs++
		}
		if numArgs != 1 {
			klog.Fatalf("Exactly 1 predicate argument is required, numArgs: %v, Predicate: %s", numArgs, predicate.Name)
		}
	}
}

// 验证优选的有效性
func validatePriorityOrDie(priority config.PriorityPolicy) {
	if priority.Argument != nil {
		numArgs := 0
		if priority.Argument.ServiceAntiAffinity != nil {
			numArgs++
		}
		if priority.Argument.LabelPreference != nil {
			numArgs++
		}
		if priority.Argument.RequestedToCapacityRatioArguments != nil {
			numArgs++
		}
		if numArgs != 1 {
			klog.Fatalf("Exactly 1 priority argument is required, numArgs: %v, Priority: %s", numArgs, priority.Name)
		}
	}
}
