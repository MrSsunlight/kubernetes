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

package scheduler

import (
	"context"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"strconv"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	coreinformers "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	"k8s.io/kubernetes/pkg/apis/core/validation"
	schedulerapi "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/apis/config/scheme"
	"k8s.io/kubernetes/pkg/scheduler/core"
	frameworkplugins "k8s.io/kubernetes/pkg/scheduler/framework/plugins"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	framework "k8s.io/kubernetes/pkg/scheduler/framework/v1alpha1"
	internalcache "k8s.io/kubernetes/pkg/scheduler/internal/cache"
	internalqueue "k8s.io/kubernetes/pkg/scheduler/internal/queue"
	"k8s.io/kubernetes/pkg/scheduler/metrics"
	"k8s.io/kubernetes/pkg/scheduler/profile"
	"k8s.io/kubernetes/pkg/scheduler/util"
)

const (
	// SchedulerError is the reason recorded for events when an error occurs during scheduling a pod.
	// 用于记录pod调度过程中发生错误时事件发生的原因
	SchedulerError = "SchedulerError"
	// Percentage of plugin metrics to be sampled.
	// 抽样插件指标的百分比
	pluginMetricsSamplePercent = 10
)

// Scheduler watches for new unscheduled pods. It attempts to find
// nodes that they fit on and writes bindings back to the api server.
// Scheduler 监视新的未调度的 pod。 它尝试找到它们适合的节点并将绑定写回 api server
type Scheduler struct {
	// It is expected that changes made via SchedulerCache will be observed
	// by NodeLister and Algorithm.
	// 预计通过SchedulerCache做出的改变将被NodeLister和Algorithm观察到
	SchedulerCache internalcache.Cache

	// NewGenericScheduler() [pkg/scheduler/core/generic_scheduler.go] 中 genericScheduler 实例化 ScheduleAlgorithm 接口
	Algorithm core.ScheduleAlgorithm

	// NextPod should be a function that blocks until the next pod
	// is available. We don't use a channel for this, because scheduling
	// a pod may take some amount of time and we don't want pods to get
	// stale while they sit in a channel.
	NextPod func() *framework.QueuedPodInfo

	// Error is called if there is an error. It is passed the pod in
	// question, and the error
	// 如果有错误，则调用Error。它传递了有问题的pod 和 错误
	Error func(*framework.QueuedPodInfo, error)

	// Close this to shut down the scheduler.
	StopEverything <-chan struct{}

	// SchedulingQueue holds pods to be scheduled
	// 保存要调度的 Pod
	SchedulingQueue internalqueue.SchedulingQueue

	// Profiles are the scheduling profiles.
	// 调度配置文件
	Profiles profile.Map

	scheduledPodsHasSynced func() bool

	client clientset.Interface
}

// Cache returns the cache in scheduler for test to check the data in scheduler.
func (sched *Scheduler) Cache() internalcache.Cache {
	return sched.SchedulerCache
}

type schedulerOptions struct {
	schedulerAlgorithmSource schedulerapi.SchedulerAlgorithmSource // 调度程序算法的源
	percentageOfNodesToScore int32                                 // 节点得分的百分比
	podInitialBackoffSeconds int64                                 // pod 初始后退秒数
	podMaxBackoffSeconds     int64                                 // pod 最大回退秒数
	// Contains out-of-tree plugins to be merged with the in-tree registry.
	frameworkOutOfTreeRegistry frameworkruntime.Registry
	profiles                   []schedulerapi.KubeSchedulerProfile
	extenders                  []schedulerapi.Extender
	frameworkCapturer          FrameworkCapturer
}

// Option configures a Scheduler
type Option func(*schedulerOptions)

// WithProfiles sets profiles for Scheduler. By default, there is one profile
// with the name "default-scheduler".
// WithProfiles 为调度器设置配置文件。默认情况下，有一个名为“default-scheduler”的配置文件
func WithProfiles(p ...schedulerapi.KubeSchedulerProfile) Option {
	return func(o *schedulerOptions) {
		o.profiles = p
	}
}

// WithAlgorithmSource sets schedulerAlgorithmSource for Scheduler, the default is a source with DefaultProvider.
// WithAlgorithmSource 设置Scheduler的schedulerAlgorithmSource，默认是一个带有DefaultProvider的源
func WithAlgorithmSource(source schedulerapi.SchedulerAlgorithmSource) Option {
	return func(o *schedulerOptions) {
		o.schedulerAlgorithmSource = source
	}
}

// WithPercentageOfNodesToScore sets percentageOfNodesToScore for Scheduler, the default value is 50
// 为Scheduler设置节点得分百分比，默认值为50
func WithPercentageOfNodesToScore(percentageOfNodesToScore int32) Option {
	return func(o *schedulerOptions) {
		o.percentageOfNodesToScore = percentageOfNodesToScore
	}
}

// WithFrameworkOutOfTreeRegistry sets the registry for out-of-tree plugins. Those plugins
// will be appended to the default registry.
// 为列表外的插件设置注册表。这些插件将被附加到默认的注册表上
func WithFrameworkOutOfTreeRegistry(registry frameworkruntime.Registry) Option {
	return func(o *schedulerOptions) {
		o.frameworkOutOfTreeRegistry = registry
	}
}

// WithPodInitialBackoffSeconds sets podInitialBackoffSeconds for Scheduler, the default value is 1
// 设置Scheduler的podInitialBackoffSeconds，默认值为1
func WithPodInitialBackoffSeconds(podInitialBackoffSeconds int64) Option {
	return func(o *schedulerOptions) {
		o.podInitialBackoffSeconds = podInitialBackoffSeconds
	}
}

// WithPodMaxBackoffSeconds sets podMaxBackoffSeconds for Scheduler, the default value is 10
// 为 Scheduler 设置 podMaxBackoffSeconds，默认值为10
func WithPodMaxBackoffSeconds(podMaxBackoffSeconds int64) Option {
	return func(o *schedulerOptions) {
		o.podMaxBackoffSeconds = podMaxBackoffSeconds
	}
}

// WithExtenders sets extenders for the Scheduler
// 为Scheduler设置扩展程序
func WithExtenders(e ...schedulerapi.Extender) Option {
	return func(o *schedulerOptions) {
		o.extenders = e
	}
}

// FrameworkCapturer is used for registering a notify function in building framework.
// 是用来在建筑框架(build framework)中注册一个通知函数
type FrameworkCapturer func(schedulerapi.KubeSchedulerProfile)

// WithBuildFrameworkCapturer sets a notify function for getting buildFramework details.
// 设置一个通知函数来获取 buildFramework 的详细信息
func WithBuildFrameworkCapturer(fc FrameworkCapturer) Option {
	return func(o *schedulerOptions) {
		o.frameworkCapturer = fc
	}
}

var defaultSchedulerOptions = schedulerOptions{
	profiles: []schedulerapi.KubeSchedulerProfile{
		// Profiles' default plugins are set from the algorithm provider.
		{SchedulerName: v1.DefaultSchedulerName},
	},
	schedulerAlgorithmSource: schedulerapi.SchedulerAlgorithmSource{
		Provider: defaultAlgorithmSourceProviderName(),
	},
	percentageOfNodesToScore: schedulerapi.DefaultPercentageOfNodesToScore,
	podInitialBackoffSeconds: int64(internalqueue.DefaultPodInitialBackoffDuration.Seconds()),
	podMaxBackoffSeconds:     int64(internalqueue.DefaultPodMaxBackoffDuration.Seconds()),
}

// New returns a Scheduler
// New 返回一个调度器
func New(client clientset.Interface,
	informerFactory informers.SharedInformerFactory,
	podInformer coreinformers.PodInformer,
	recorderFactory profile.RecorderFactory,
	stopCh <-chan struct{},
	opts ...Option) (*Scheduler, error) {

	// 如果为空 则赋值默认值
	stopEverything := stopCh
	if stopEverything == nil {
		stopEverything = wait.NeverStop
	}

	options := defaultSchedulerOptions
	// 执行 opts 中的 WithProfiles、WithPercentageOfNodesToScore ...等函数项
	for _, opt := range opts {
		opt(&options)
	}

	// 创建调度器缓存过期处理函数
	schedulerCache := internalcache.New(30*time.Second, stopEverything)

	// 建立注册表
	registry := frameworkplugins.NewInTreeRegistry()
	if err := registry.Merge(options.frameworkOutOfTreeRegistry); err != nil {
		return nil, err
	}

	// 初始化快照数据结构
	snapshot := internalcache.NewEmptySnapshot()

	configurator := &Configurator{
		client:                   client,
		recorderFactory:          recorderFactory, // 记录器
		informerFactory:          informerFactory, // 通知器
		podInformer:              podInformer,
		schedulerCache:           schedulerCache,                   // 调度器缓存处理函数
		StopEverything:           stopEverything,                   // 调度器结束控制 chan
		percentageOfNodesToScore: options.percentageOfNodesToScore, // 节点得分的百分比
		podInitialBackoffSeconds: options.podInitialBackoffSeconds,
		podMaxBackoffSeconds:     options.podMaxBackoffSeconds,
		profiles:                 append([]schedulerapi.KubeSchedulerProfile(nil), options.profiles...), // 配置文件
		registry:                 registry,                                                              // 插件注册表
		nodeInfoSnapshot:         snapshot,                                                              // 快照
		extenders:                options.extenders,                                                     // 扩展
		frameworkCapturer:        options.frameworkCapturer,                                             // framework 捕获器
	}

	// 注册所有指标
	metrics.Register()

	var sched *Scheduler
	// 调度程序算法的源
	source := options.schedulerAlgorithmSource
	switch {
	// 提供者 不为空
	case source.Provider != nil:
		// Create the config from a named algorithm provider.
		// 从指定的算法提供程序创建配置
		sc, err := configurator.createFromProvider(*source.Provider)
		if err != nil {
			return nil, fmt.Errorf("couldn't create scheduler using provider %q: %v", *source.Provider, err)
		}
		sched = sc
	// 策略不为空
	case source.Policy != nil:
		// Create the config from a user specified policy source.
		// 从用户指定的策略源创建配置
		policy := &schedulerapi.Policy{}
		switch {
		case source.Policy.File != nil:
			if err := initPolicyFromFile(source.Policy.File.Path, policy); err != nil {
				return nil, err
			}
		case source.Policy.ConfigMap != nil:
			if err := initPolicyFromConfigMap(client, source.Policy.ConfigMap, policy); err != nil {
				return nil, err
			}
		}
		// Set extenders on the configurator now that we've decoded the policy
		// In this case, c.extenders should be nil since we're using a policy (and therefore not componentconfig,
		// which would have set extenders in the above instantiation of Configurator from CC options)
		// 既然我们已经解码了策略，就在配置器上设置扩展器。在这种情况下，c.extenders 应该是 nil，
		// 因为我们使用的是策略（因此不是 componentconfig，它会在上面的 CC 选项的 Configurator 实例化中设置扩展器 )
		configurator.extenders = policy.Extenders
		sc, err := configurator.createFromConfig(*policy)
		if err != nil {
			return nil, fmt.Errorf("couldn't create scheduler from policy: %v", err)
		}
		sched = sc
	default:
		return nil, fmt.Errorf("unsupported algorithm source: %v", source)
	}
	// Additional tweaks to the config produced by the configurator.
	// 对配置器产生的配置进行额外调整
	sched.StopEverything = stopEverything
	sched.client = client
	// 同步调度的 pod
	sched.scheduledPodsHasSynced = podInformer.Informer().HasSynced

	// 添加事件处理程序
	addAllEventHandlers(sched, informerFactory, podInformer)
	return sched, nil
}

// initPolicyFromFile initialize policy from file
// 从文件中初始化策略
func initPolicyFromFile(policyFile string, policy *schedulerapi.Policy) error {
	// Use a policy serialized in a file.
	_, err := os.Stat(policyFile)
	if err != nil {
		return fmt.Errorf("missing policy config file %s", policyFile)
	}
	data, err := ioutil.ReadFile(policyFile)
	if err != nil {
		return fmt.Errorf("couldn't read policy config: %v", err)
	}
	err = runtime.DecodeInto(scheme.Codecs.UniversalDecoder(), []byte(data), policy)
	if err != nil {
		return fmt.Errorf("invalid policy: %v", err)
	}
	return nil
}

// initPolicyFromConfigMap initialize policy from configMap
// 从configMap中初始化策略
func initPolicyFromConfigMap(client clientset.Interface, policyRef *schedulerapi.SchedulerPolicyConfigMapSource, policy *schedulerapi.Policy) error {
	// Use a policy serialized in a config map value.
	policyConfigMap, err := client.CoreV1().ConfigMaps(policyRef.Namespace).Get(context.TODO(), policyRef.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("couldn't get policy config map %s/%s: %v", policyRef.Namespace, policyRef.Name, err)
	}
	data, found := policyConfigMap.Data[schedulerapi.SchedulerPolicyConfigMapKey]
	if !found {
		return fmt.Errorf("missing policy config map value at key %q", schedulerapi.SchedulerPolicyConfigMapKey)
	}
	err = runtime.DecodeInto(scheme.Codecs.UniversalDecoder(), []byte(data), policy)
	if err != nil {
		return fmt.Errorf("invalid policy: %v", err)
	}
	return nil
}

// Run begins watching and scheduling. It waits for cache to be synced, then starts scheduling and blocked until the context is done.
// 开始运行watching和scheduling。它等待缓存被同步，然后开始scheduling，并封锁直到上下文完成;
// 先等待cache同步，然后开启调度逻辑的goroutine
func (sched *Scheduler) Run(ctx context.Context) {
	if !cache.WaitForCacheSync(ctx.Done(), sched.scheduledPodsHasSynced) {
		return
	}

	// SchedulingQueue 保存要调度的pod, 启动管理队列的 goroutine
	// pkg/scheduler/internal/queue/scheduling_queue.go --> (p *PriorityQueue) Run()
	sched.SchedulingQueue.Run()
	// 循环执行 sched.scheduleOne 没有时间间隔
	wait.UntilWithContext(ctx, sched.scheduleOne, 0)
	// 停止管理队列的 goroutine
	sched.SchedulingQueue.Close()
}

// recordSchedulingFailure records an event for the pod that indicates the
// pod has failed to schedule. Also, update the pod condition and nominated node name if set.
// 为pod记录一个事件，表明该pod未能被调度。同时，如果设置了更新 pod 条件和指定节点名称，则更新 pod 条件和指定节点名称。
func (sched *Scheduler) recordSchedulingFailure(prof *profile.Profile, podInfo *framework.QueuedPodInfo, err error, reason string, nominatedNode string) {
	sched.Error(podInfo, err)

	// Update the scheduling queue with the nominated pod information. Without
	// this, there would be a race condition between the next scheduling cycle
	// and the time the scheduler receives a Pod Update for the nominated pod.
	// Here we check for nil only for tests.
	if sched.SchedulingQueue != nil {
		sched.SchedulingQueue.AddNominatedPod(podInfo.Pod, nominatedNode)
	}

	pod := podInfo.Pod
	msg := truncateMessage(err.Error())
	prof.Recorder.Eventf(pod, nil, v1.EventTypeWarning, "FailedScheduling", "Scheduling", msg)
	if err := updatePod(sched.client, pod, &v1.PodCondition{
		Type:    v1.PodScheduled,
		Status:  v1.ConditionFalse,
		Reason:  reason,
		Message: err.Error(),
	}, nominatedNode); err != nil {
		klog.Errorf("Error updating pod %s/%s: %v", pod.Namespace, pod.Name, err)
	}
}

// truncateMessage truncates a message if it hits the NoteLengthLimit.
func truncateMessage(message string) string {
	max := validation.NoteLengthLimit
	if len(message) <= max {
		return message
	}
	suffix := " ..."
	return message[:max-len(suffix)] + suffix
}

func updatePod(client clientset.Interface, pod *v1.Pod, condition *v1.PodCondition, nominatedNode string) error {
	klog.V(3).Infof("Updating pod condition for %s/%s to (%s==%s, Reason=%s)", pod.Namespace, pod.Name, condition.Type, condition.Status, condition.Reason)
	podCopy := pod.DeepCopy()
	// NominatedNodeName is updated only if we are trying to set it, and the value is
	// different from the existing one.
	if !podutil.UpdatePodCondition(&podCopy.Status, condition) &&
		(len(nominatedNode) == 0 || pod.Status.NominatedNodeName == nominatedNode) {
		return nil
	}
	if nominatedNode != "" {
		podCopy.Status.NominatedNodeName = nominatedNode
	}
	return util.PatchPod(client, pod, podCopy)
}

// assume signals to the cache that a pod is already in the cache, so that binding can be asynchronous.
// assume modifies `assumed`.
// 向缓存发出信号，表明pod已经在缓存中，因此绑定可以是异步的。假设修改 'assumed'
func (sched *Scheduler) assume(assumed *v1.Pod, host string) error {
	// Optimistically assume that the binding will succeed and send it to apiserver
	// in the background.
	// If the binding fails, scheduler will release resources allocated to assumed pod
	// immediately.
	assumed.Spec.NodeName = host

	// 8.假定选中pod  -->AssumePod() [pkg/scheduler/internal/cache/cache.go]
	if err := sched.SchedulerCache.AssumePod(assumed); err != nil {
		klog.Errorf("scheduler cache AssumePod failed: %v", err)
		return err
	}
	// if "assumed" is a nominated pod, we should remove it from internal cache
	if sched.SchedulingQueue != nil {
		sched.SchedulingQueue.DeleteNominatedPodIfExists(assumed)
	}

	return nil
}

// bind binds a pod to a given node defined in a binding object.
// The precedence for binding is: (1) extenders and (2) framework plugins.
// We expect this to run asynchronously, so we handle binding metrics internally.
func (sched *Scheduler) bind(ctx context.Context, prof *profile.Profile, assumed *v1.Pod, targetNode string, state *framework.CycleState) (err error) {
	start := time.Now()
	defer func() {
		sched.finishBinding(prof, assumed, targetNode, start, err)
	}()

	bound, err := sched.extendersBinding(assumed, targetNode)
	if bound {
		return err
	}
	bindStatus := prof.RunBindPlugins(ctx, state, assumed, targetNode)
	if bindStatus.IsSuccess() {
		return nil
	}
	if bindStatus.Code() == framework.Error {
		return bindStatus.AsError()
	}
	return fmt.Errorf("bind status: %s, %v", bindStatus.Code().String(), bindStatus.Message())
}

// TODO(#87159): Move this to a Plugin.
func (sched *Scheduler) extendersBinding(pod *v1.Pod, node string) (bool, error) {
	for _, extender := range sched.Algorithm.Extenders() {
		if !extender.IsBinder() || !extender.IsInterested(pod) {
			continue
		}
		return true, extender.Bind(&v1.Binding{
			ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID},
			Target:     v1.ObjectReference{Kind: "Node", Name: node},
		})
	}
	return false, nil
}

func (sched *Scheduler) finishBinding(prof *profile.Profile, assumed *v1.Pod, targetNode string, start time.Time, err error) {
	if finErr := sched.SchedulerCache.FinishBinding(assumed); finErr != nil {
		klog.Errorf("scheduler cache FinishBinding failed: %v", finErr)
	}
	if err != nil {
		klog.V(1).Infof("Failed to bind pod: %v/%v", assumed.Namespace, assumed.Name)
		return
	}

	metrics.BindingLatency.Observe(metrics.SinceInSeconds(start))
	metrics.DeprecatedSchedulingDuration.WithLabelValues(metrics.Binding).Observe(metrics.SinceInSeconds(start))
	prof.Recorder.Eventf(assumed, nil, v1.EventTypeNormal, "Scheduled", "Binding", "Successfully assigned %v/%v to %v", assumed.Namespace, assumed.Name, targetNode)
}

// scheduleOne does the entire scheduling workflow for a single pod.  It is serialized on the scheduling algorithm's host fitting.
// scheduleOne 为单个 pod 执行整个调度工作流程。它被序列化在调度算法的适合的主机上
func (sched *Scheduler) scheduleOne(ctx context.Context) {
	// 在setup 中初始化赋值 nextpod   (c *Configurator) create() [pkg/scheduler/factory.go] --> internalqueue.MakeNextPodFunc(podQueue) [pkg/scheduler/internal/queue/scheduling_queue.go]
	// 获取待调度的pod，返回调度器的额外信息（首次加入队列时间、重复调度次数等）
	podInfo := sched.NextPod()
	// pod could be nil when schedulerQueue is closed
	// 当 schedulerQueue 被关闭时，pod可能为零
	if podInfo == nil || podInfo.Pod == nil {
		return
	}
	pod := podInfo.Pod
	// 获取 pod 调度配置文件
	prof, err := sched.profileForPod(pod)
	if err != nil {
		// This shouldn't happen, because we only accept for scheduling the pods
		// which specify a scheduler name that matches one of the profiles.
		klog.Error(err)
		return
	}
	// 监测 pod 是否被跳过
	if sched.skipPodSchedule(prof, pod) {
		return
	}

	klog.V(3).Infof("Attempting to schedule pod: %v/%v", pod.Namespace, pod.Name)

	// Synchronously attempt to find a fit for the pod.
	// 同步尝试找到适合 pod
	// 记录开始时间
	start := time.Now()
	// 初始化一个新的 CycleState 并返回它的指针, CycleState 是插件存储和检索的机制
	state := framework.NewCycleState()
	// 设置 记录插件指标 的初始值
	state.SetRecordPluginMetrics(rand.Intn(100) < pluginMetricsSamplePercent)
	schedulingCycleCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// 尝试将给定的 pod 调度到节点列表中的一个节点。如果成功，它将返回 node 的名称。如果失败，它将返回一个带有原因的fiiterror错误
	// Algorithm --> genericScheduler; func (g *genericScheduler) Schedule(...) [pkg/scheduler/core/generic_scheduler.go]
	scheduleResult, err := sched.Algorithm.Schedule(schedulingCycleCtx, prof, state, pod)
	// 如果筛选过程出错
	if err != nil {
		// Schedule() may have failed because the pod would not fit on any host, so we try to
		// preempt, with the expectation that the next time the pod is tried for scheduling it
		// will fit due to the preemption. It is also possible that a different pod will schedule
		// into the resources that were preempted, but this is harmless.
		// Schedule() 可能因为 Pod 不适合任何主机而失败，因此我们尝试抢占，期望下次尝试调度 Pod 时由于抢占而适合。
		// 也有可能不同的 pod 会调度到被抢占的资源中，但这是无害的
		nominatedNode := ""
		if fitError, ok := err.(*core.FitError); ok {
			if !prof.HasPostFilterPlugins() {
				// 没有注册PostFilter插件，所以不会进行抢占
				klog.V(3).Infof("No PostFilter plugins are registered, so no preemption will be performed.")
			} else {
				// Run PostFilter plugins to try to make the pod schedulable in a future scheduling cycle.
				// 运行 PostFilter 插件以尝试使 pod 在未来的调度周期中可调度
				result, status := prof.RunPostFilterPlugins(ctx, state, pod, fitError.FilteredNodesStatuses)
				if status.Code() == framework.Error {
					klog.Errorf("Status after running PostFilter plugins for pod %v/%v: %v", pod.Namespace, pod.Name, status)
				} else {
					klog.V(5).Infof("Status after running PostFilter plugins for pod %v/%v: %v", pod.Namespace, pod.Name, status)
				}
				if status.IsSuccess() && result != nil {
					// 被提名的节点
					nominatedNode = result.NominatedNodeName
				}
			}
			// Pod did not fit anywhere, so it is counted as a failure. If preemption
			// succeeds, the pod should get counted as a success the next time we try to
			// schedule it. (hopefully)
			// Pod 不适合任何地方，因此被视为失败。 如果抢占成功，下次我们尝试调度它时，该 pod 应该被视为成功。 （希望）
			metrics.PodUnschedulable(prof.Name, metrics.SinceInSeconds(start))
		} else if err == core.ErrNoNodesAvailable { // 不可调度
			// No nodes available is counted as unschedulable rather than an error.
			// 没有可用节点被计为不可调度而不是错误
			metrics.PodUnschedulable(prof.Name, metrics.SinceInSeconds(start))
		} else { // 调度错误
			klog.ErrorS(err, "Error selecting node for pod", "pod", klog.KObj(pod))
			metrics.PodScheduleError(prof.Name, metrics.SinceInSeconds(start))
		}
		// 记录pod调度失败事件
		sched.recordSchedulingFailure(prof, podInfo, err, v1.PodReasonUnschedulable, nominatedNode)
		return
	}
	metrics.SchedulingAlgorithmLatency.Observe(metrics.SinceInSeconds(start))
	// Tell the cache to assume that a pod now is running on a given node, even though it hasn't been bound yet.
	// This allows us to keep scheduling without waiting on binding to occur.
	// 告诉缓存假设一个pod现在正在一个给定的节点上运行，即使它还没有被绑定, 这使得我们可以在不等待绑定发生的情况下进行调度
	assumedPodInfo := podInfo.DeepCopy()
	assumedPod := assumedPodInfo.Pod
	// assume modifies `assumedPod` by setting NodeName=scheduleResult.SuggestedHost
	// 假设通过设置 NodeName = scheduleResult.SuggestedHost 修改 `assumedPod`
	err = sched.assume(assumedPod, scheduleResult.SuggestedHost)
	if err != nil {
		metrics.PodScheduleError(prof.Name, metrics.SinceInSeconds(start))
		// This is most probably result of a BUG in retrying logic.
		// We report an error here so that pod scheduling can be retried.
		// This relies on the fact that Error will check if the pod has been bound
		// to a node and if so will not add it back to the unscheduled pods queue
		// (otherwise this would cause an infinite loop).
		sched.recordSchedulingFailure(prof, assumedPodInfo, err, SchedulerError, "")
		return
	}

	// Run the Reserve method of reserve plugins.
	// 运行储备插件的储备方法
	if sts := prof.RunReservePluginsReserve(schedulingCycleCtx, state, assumedPod, scheduleResult.SuggestedHost); !sts.IsSuccess() {
		metrics.PodScheduleError(prof.Name, metrics.SinceInSeconds(start))
		// trigger un-reserve to clean up state associated with the reserved Pod
		prof.RunReservePluginsUnreserve(schedulingCycleCtx, state, assumedPod, scheduleResult.SuggestedHost)
		if forgetErr := sched.Cache().ForgetPod(assumedPod); forgetErr != nil {
			klog.Errorf("scheduler cache ForgetPod failed: %v", forgetErr)
		}
		sched.recordSchedulingFailure(prof, assumedPodInfo, sts.AsError(), SchedulerError, "")
		return
	}

	// Run "permit" plugins.
	// 运行“许可”插件
	runPermitStatus := prof.RunPermitPlugins(schedulingCycleCtx, state, assumedPod, scheduleResult.SuggestedHost)
	if runPermitStatus.Code() != framework.Wait && !runPermitStatus.IsSuccess() {
		var reason string
		if runPermitStatus.IsUnschedulable() {
			metrics.PodUnschedulable(prof.Name, metrics.SinceInSeconds(start))
			reason = v1.PodReasonUnschedulable
		} else {
			metrics.PodScheduleError(prof.Name, metrics.SinceInSeconds(start))
			reason = SchedulerError
		}
		// One of the plugins returned status different than success or wait.
		prof.RunReservePluginsUnreserve(schedulingCycleCtx, state, assumedPod, scheduleResult.SuggestedHost)
		if forgetErr := sched.Cache().ForgetPod(assumedPod); forgetErr != nil {
			klog.Errorf("scheduler cache ForgetPod failed: %v", forgetErr)
		}
		sched.recordSchedulingFailure(prof, assumedPodInfo, runPermitStatus.AsError(), reason, "")
		return
	}

	// bind the pod to its host asynchronously (we can do this b/c of the assumption step above).
	// 将 pod 异步绑定到其主机
	go func() {
		bindingCycleCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		metrics.SchedulerGoroutines.WithLabelValues("binding").Inc()
		defer metrics.SchedulerGoroutines.WithLabelValues("binding").Dec()

		waitOnPermitStatus := prof.WaitOnPermit(bindingCycleCtx, assumedPod)
		if !waitOnPermitStatus.IsSuccess() {
			var reason string
			if waitOnPermitStatus.IsUnschedulable() {
				metrics.PodUnschedulable(prof.Name, metrics.SinceInSeconds(start))
				reason = v1.PodReasonUnschedulable
			} else {
				metrics.PodScheduleError(prof.Name, metrics.SinceInSeconds(start))
				reason = SchedulerError
			}
			// trigger un-reserve plugins to clean up state associated with the reserved Pod
			prof.RunReservePluginsUnreserve(bindingCycleCtx, state, assumedPod, scheduleResult.SuggestedHost)
			if forgetErr := sched.Cache().ForgetPod(assumedPod); forgetErr != nil {
				klog.Errorf("scheduler cache ForgetPod failed: %v", forgetErr)
			}
			sched.recordSchedulingFailure(prof, assumedPodInfo, waitOnPermitStatus.AsError(), reason, "")
			return
		}

		// Run "prebind" plugins.
		preBindStatus := prof.RunPreBindPlugins(bindingCycleCtx, state, assumedPod, scheduleResult.SuggestedHost)
		if !preBindStatus.IsSuccess() {
			metrics.PodScheduleError(prof.Name, metrics.SinceInSeconds(start))
			// trigger un-reserve plugins to clean up state associated with the reserved Pod
			prof.RunReservePluginsUnreserve(bindingCycleCtx, state, assumedPod, scheduleResult.SuggestedHost)
			if forgetErr := sched.Cache().ForgetPod(assumedPod); forgetErr != nil {
				klog.Errorf("scheduler cache ForgetPod failed: %v", forgetErr)
			}
			sched.recordSchedulingFailure(prof, assumedPodInfo, preBindStatus.AsError(), SchedulerError, "")
			return
		}

		// 9.绑定 pod
		err := sched.bind(bindingCycleCtx, prof, assumedPod, scheduleResult.SuggestedHost, state)
		if err != nil {
			metrics.PodScheduleError(prof.Name, metrics.SinceInSeconds(start))
			// trigger un-reserve plugins to clean up state associated with the reserved Pod
			prof.RunReservePluginsUnreserve(bindingCycleCtx, state, assumedPod, scheduleResult.SuggestedHost)
			if err := sched.SchedulerCache.ForgetPod(assumedPod); err != nil {
				klog.Errorf("scheduler cache ForgetPod failed: %v", err)
			}
			sched.recordSchedulingFailure(prof, assumedPodInfo, fmt.Errorf("Binding rejected: %v", err), SchedulerError, "")
		} else {
			// Calculating nodeResourceString can be heavy. Avoid it if klog verbosity is below 2.
			if klog.V(2).Enabled() {
				klog.InfoS("Successfully bound pod to node", "pod", klog.KObj(pod), "node", scheduleResult.SuggestedHost, "evaluatedNodes", scheduleResult.EvaluatedNodes, "feasibleNodes", scheduleResult.FeasibleNodes)
			}
			metrics.PodScheduled(prof.Name, metrics.SinceInSeconds(start))
			metrics.PodSchedulingAttempts.Observe(float64(podInfo.Attempts))
			metrics.PodSchedulingDuration.WithLabelValues(getAttemptsLabel(podInfo)).Observe(metrics.SinceInSeconds(podInfo.InitialAttemptTimestamp))

			// Run "postbind" plugins.
			prof.RunPostBindPlugins(bindingCycleCtx, state, assumedPod, scheduleResult.SuggestedHost)
		}
	}()
}

func getAttemptsLabel(p *framework.QueuedPodInfo) string {
	// We breakdown the pod scheduling duration by attempts capped to a limit
	// to avoid ending up with a high cardinality metric.
	if p.Attempts >= 15 {
		return "15+"
	}
	return strconv.Itoa(p.Attempts)
}

// pod 调度配置文件
func (sched *Scheduler) profileForPod(pod *v1.Pod) (*profile.Profile, error) {
	// 调度配置文件
	prof, ok := sched.Profiles[pod.Spec.SchedulerName]
	if !ok {
		return nil, fmt.Errorf("profile not found for scheduler name %q", pod.Spec.SchedulerName)
	}
	return prof, nil
}

// skipPodSchedule returns true if we could skip scheduling the pod for specified cases.
// 如果可以在指定的情况下跳过调度pod，则返回true。
func (sched *Scheduler) skipPodSchedule(prof *profile.Profile, pod *v1.Pod) bool {
	// Case 1: pod is being deleted.
	// Pod正在被删除
	if pod.DeletionTimestamp != nil {
		prof.Recorder.Eventf(pod, nil, v1.EventTypeWarning, "FailedScheduling", "Scheduling", "skip schedule deleting pod: %v/%v", pod.Namespace, pod.Name)
		klog.V(3).Infof("Skip schedule deleting pod: %v/%v", pod.Namespace, pod.Name)
		return true
	}

	// Case 2: pod has been assumed and pod updates could be skipped.
	// An assumed pod can be added again to the scheduling queue if it got an update event
	// during its previous scheduling cycle but before getting assumed.
	// 假设pod已经更新 跳过
	if sched.skipPodUpdate(pod) {
		return true
	}

	return false
}

func defaultAlgorithmSourceProviderName() *string {
	provider := schedulerapi.SchedulerDefaultProviderName
	return &provider
}
