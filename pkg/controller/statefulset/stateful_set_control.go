/*
Copyright 2016 The Kubernetes Authors.

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

package statefulset

import (
	"sort"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/history"
)

// StatefulSetControl实现了用于更新StatefulSets及其子Pods的控制逻辑。它被实现为一个接口，允许提供不同语义的扩展。目前，只有一种实现.
type StatefulSetControlInterface interface {
	// UpdateStatefulSet实现了用于Pod创建，更新和删除以及持久卷创建，更新和删除的控制逻辑。
	// 如果实现返回非零错误，则将使用速率限制策略重试调用。
	// 实现者应该清除他们不希望触发重试的任何错误，并且他们可以随时在任何时候异常退出，只要他们希望在以后的某个时间重新运行该更新.
	UpdateStatefulSet(set *apps.StatefulSet, pods []*v1.Pod) error
	// ListRevisions返回一个ControllerRevisions数组，这些数组代表set的修订版。 如果返回的错误为nil，则ControllerRevisions的返回片有效.
	ListRevisions(set *apps.StatefulSet) ([]*apps.ControllerRevision, error)
	// AdoptOrphanRevisions adopts any orphaned ControllerRevisions that match set's Selector. If all adoptions are
	// successful the returned error is nil.
	// AdoptOrphanRevisions采用任何与 set 的选择器相匹配的孤立的ControllerRevisions。如果所有的采用都成功，则返回的错误为nil
	AdoptOrphanRevisions(set *apps.StatefulSet, revisions []*apps.ControllerRevision) error
}

// NewDefaultStatefulSetControl returns a new instance of the default implementation StatefulSetControlInterface that
// implements the documented semantics for StatefulSets. podControl is the PodControlInterface used to create, update,
// and delete Pods and to create PersistentVolumeClaims. statusUpdater is the StatefulSetStatusUpdaterInterface used
// to update the status of StatefulSets. You should use an instance returned from NewRealStatefulPodControl() for any
// scenario other than testing.
func NewDefaultStatefulSetControl(
	podControl StatefulPodControlInterface,
	statusUpdater StatefulSetStatusUpdaterInterface,
	controllerHistory history.Interface,
	recorder record.EventRecorder) StatefulSetControlInterface {
	return &defaultStatefulSetControl{podControl, statusUpdater, controllerHistory, recorder}
}

type defaultStatefulSetControl struct {
	podControl        StatefulPodControlInterface
	statusUpdater     StatefulSetStatusUpdaterInterface
	controllerHistory history.Interface
	recorder          record.EventRecorder
}

// UpdateStatefulSet executes the core logic loop for a stateful set, applying the predictable and
// consistent monotonic update strategy by default - scale up proceeds in ordinal order, no new pod
// is created while any pod is unhealthy, and pods are terminated in descending order. The burst
// strategy allows these constraints to be relaxed - pods will be created and deleted eagerly and
// in no particular order. Clients using the burst strategy should be careful to ensure they
// understand the consistency implications of having unpredictable numbers of pods available.
// UpdateStatefulSet 执行有状态集的核心逻辑循环，默认情况下应用可预测和一致的单调更新策略——按顺序递增，当任何pod不健康时不创建新的pod，并按降序终止pod。
// 突发策略允许这些限制被放松-pods将被立即创建和删除，没有特定的顺序。使用burst策略的客户端应该小心地确保他们理解使用不可预测数量的pod所带来的一致性影响
func (ssc *defaultStatefulSetControl) UpdateStatefulSet(set *apps.StatefulSet, pods []*v1.Pod) error {

	// list all revisions and sort them
	revisions, err := ssc.ListRevisions(set)
	if err != nil {
		return err
	}
	history.SortControllerRevisions(revisions)
	// StatefulSet主要的更新逻辑
	currentRevision, updateRevision, err := ssc.performUpdate(set, pods, revisions)
	if err != nil {
		return utilerrors.NewAggregate([]error{err, ssc.truncateHistory(set, pods, revisions, currentRevision, updateRevision)})
	}

	// maintain the set's revision history limit
	return ssc.truncateHistory(set, pods, revisions, currentRevision, updateRevision)
}

func (ssc *defaultStatefulSetControl) performUpdate(
	set *apps.StatefulSet, pods []*v1.Pod, revisions []*apps.ControllerRevision) (*apps.ControllerRevision, *apps.ControllerRevision, error) {

	// get the current, and update revisions 获取当前版本，并更新修订版本
	// 获取各个Revision，通过不同的Revision来进行版本的控制
	currentRevision, updateRevision, collisionCount, err := ssc.getStatefulSetRevisions(set, revisions)
	if err != nil {
		return currentRevision, updateRevision, err
	}

	// perform the main update function and get the status
	// 主要执行更新操作，包括pod的创建、更新、删除，并返回最后的StatefulSet执行状态
	status, err := ssc.updateStatefulSet(set, currentRevision, updateRevision, collisionCount, pods)
	if err != nil {
		return currentRevision, updateRevision, err
	}

	// update the set's status
	// 最后更新StatefulSet的状态
	err = ssc.updateStatefulSetStatus(set, status)
	if err != nil {
		return currentRevision, updateRevision, err
	}

	klog.V(4).Infof("StatefulSet %s/%s pod status replicas=%d ready=%d current=%d updated=%d",
		set.Namespace,
		set.Name,
		status.Replicas,
		status.ReadyReplicas,
		status.CurrentReplicas,
		status.UpdatedReplicas)

	klog.V(4).Infof("StatefulSet %s/%s revisions current=%s update=%s",
		set.Namespace,
		set.Name,
		status.CurrentRevision,
		status.UpdateRevision)

	return currentRevision, updateRevision, nil
}

func (ssc *defaultStatefulSetControl) ListRevisions(set *apps.StatefulSet) ([]*apps.ControllerRevision, error) {
	selector, err := metav1.LabelSelectorAsSelector(set.Spec.Selector)
	if err != nil {
		return nil, err
	}
	return ssc.controllerHistory.ListControllerRevisions(set, selector)
}

func (ssc *defaultStatefulSetControl) AdoptOrphanRevisions(
	set *apps.StatefulSet,
	revisions []*apps.ControllerRevision) error {
	for i := range revisions {
		adopted, err := ssc.controllerHistory.AdoptControllerRevision(set, controllerKind, revisions[i])
		if err != nil {
			return err
		}
		revisions[i] = adopted
	}
	return nil
}

// truncateHistory truncates any non-live ControllerRevisions in revisions from set's history. The UpdateRevision and
// CurrentRevision in set's Status are considered to be live. Any revisions associated with the Pods in pods are also
// considered to be live. Non-live revisions are deleted, starting with the revision with the lowest Revision, until
// only RevisionHistoryLimit revisions remain. If the returned error is nil the operation was successful. This method
// expects that revisions is sorted when supplied.
func (ssc *defaultStatefulSetControl) truncateHistory(
	set *apps.StatefulSet,
	pods []*v1.Pod,
	revisions []*apps.ControllerRevision,
	current *apps.ControllerRevision,
	update *apps.ControllerRevision) error {
	history := make([]*apps.ControllerRevision, 0, len(revisions))
	// mark all live revisions
	live := map[string]bool{}
	if current != nil {
		live[current.Name] = true
	}
	if update != nil {
		live[update.Name] = true
	}
	for i := range pods {
		live[getPodRevision(pods[i])] = true
	}
	// collect live revisions and historic revisions
	for i := range revisions {
		if !live[revisions[i].Name] {
			history = append(history, revisions[i])
		}
	}
	historyLen := len(history)
	historyLimit := int(*set.Spec.RevisionHistoryLimit)
	if historyLen <= historyLimit {
		return nil
	}
	// delete any non-live history to maintain the revision limit.
	history = history[:(historyLen - historyLimit)]
	for i := 0; i < len(history); i++ {
		if err := ssc.controllerHistory.DeleteControllerRevision(history[i]); err != nil {
			return err
		}
	}
	return nil
}

// getStatefulSetRevisions returns the current and update ControllerRevisions for set. It also
// returns a collision count that records the number of name collisions set saw when creating
// new ControllerRevisions. This count is incremented on every name collision and is used in
// building the ControllerRevision names for name collision avoidance. This method may create
// a new revision, or modify the Revision of an existing revision if an update to set is detected.
// This method expects that revisions is sorted when supplied.
func (ssc *defaultStatefulSetControl) getStatefulSetRevisions(
	set *apps.StatefulSet,
	revisions []*apps.ControllerRevision) (*apps.ControllerRevision, *apps.ControllerRevision, int32, error) {
	var currentRevision, updateRevision *apps.ControllerRevision

	revisionCount := len(revisions)
	history.SortControllerRevisions(revisions)

	// Use a local copy of set.Status.CollisionCount to avoid modifying set.Status directly.
	// This copy is returned so the value gets carried over to set.Status in updateStatefulSet.
	var collisionCount int32
	if set.Status.CollisionCount != nil {
		collisionCount = *set.Status.CollisionCount
	}

	// create a new revision from the current set
	updateRevision, err := newRevision(set, nextRevision(revisions), &collisionCount)
	if err != nil {
		return nil, nil, collisionCount, err
	}

	// find any equivalent revisions
	equalRevisions := history.FindEqualRevisions(revisions, updateRevision)
	equalCount := len(equalRevisions)

	if equalCount > 0 && history.EqualRevision(revisions[revisionCount-1], equalRevisions[equalCount-1]) {
		// if the equivalent revision is immediately prior the update revision has not changed
		updateRevision = revisions[revisionCount-1]
	} else if equalCount > 0 {
		// if the equivalent revision is not immediately prior we will roll back by incrementing the
		// Revision of the equivalent revision
		updateRevision, err = ssc.controllerHistory.UpdateControllerRevision(
			equalRevisions[equalCount-1],
			updateRevision.Revision)
		if err != nil {
			return nil, nil, collisionCount, err
		}
	} else {
		//if there is no equivalent revision we create a new one
		updateRevision, err = ssc.controllerHistory.CreateControllerRevision(set, updateRevision, &collisionCount)
		if err != nil {
			return nil, nil, collisionCount, err
		}
	}

	// attempt to find the revision that corresponds to the current revision
	for i := range revisions {
		if revisions[i].Name == set.Status.CurrentRevision {
			currentRevision = revisions[i]
			break
		}
	}

	// if the current revision is nil we initialize the history by setting it to the update revision
	if currentRevision == nil {
		currentRevision = updateRevision
	}

	return currentRevision, updateRevision, collisionCount, nil
}

// updateStatefulSet performs the update function for a StatefulSet. This method creates, updates, and deletes Pods in
// the set in order to conform the system to the target state for the set. The target state always contains
// set.Spec.Replicas Pods with a Ready Condition. If the UpdateStrategy.Type for the set is
// RollingUpdateStatefulSetStrategyType then all Pods in the set must be at set.Status.CurrentRevision.
// If the UpdateStrategy.Type for the set is OnDeleteStatefulSetStrategyType, the target state implies nothing about
// the revisions of Pods in the set. If the UpdateStrategy.Type for the set is PartitionStatefulSetStrategyType, then
// all Pods with ordinal less than UpdateStrategy.Partition.Ordinal must be at Status.CurrentRevision and all other
// Pods must be at Status.UpdateRevision. If the returned error is nil, the returned StatefulSetStatus is valid and the
// update must be recorded. If the error is not nil, the method should be retried until successful.
// updateStatefulSet对StatefulSet执行更新功能。 此方法创建，更新和删除集合中的Pod，以使系统符合集合的目标状态。
// Pod目标状态始终包含具有就绪条件的set.Spec.Replicas。
// 如果集合的UpdateStrategy.Type为RollingUpdateStatefulSetStrategyType，则集合中的所有Pod必须位于set.Status.CurrentRevision。
// 如果集合的UpdateStrategy.Type为OnDeleteStatefulSetStrategyType，则目标状态不暗示集合中Pod的修订。
// 如果集合的UpdateStrategy.Type为PartitionStatefulSetStrategyType，则所有序号小于UpdateStrategy.Partition.Ordinal的Pod必须位于Status.CurrentRevision，
// 所有其他Pod必须均处于Status.UpdateRevision。 如果返回的错误为nil，则返回的StatefulSetStatus有效，并且必须记录更新。 如果错误不是nil，则应重试该方法，直到成功为止.
func (ssc *defaultStatefulSetControl) updateStatefulSet(
	set *apps.StatefulSet,
	currentRevision *apps.ControllerRevision,
	updateRevision *apps.ControllerRevision,
	collisionCount int32,
	pods []*v1.Pod) (*apps.StatefulSetStatus, error) {
	// get the current and update revisions of the set.
	currentSet, err := ApplyRevision(set, currentRevision)
	if err != nil {
		return nil, err
	}
	updateSet, err := ApplyRevision(set, updateRevision)
	if err != nil {
		return nil, err
	}

	// set the generation, and revisions in the returned status
	status := apps.StatefulSetStatus{}
	status.ObservedGeneration = set.Generation
	status.CurrentRevision = currentRevision.Name
	status.UpdateRevision = updateRevision.Name
	status.CollisionCount = new(int32)
	*status.CollisionCount = collisionCount

	replicaCount := int(*set.Spec.Replicas)
	// slice that will contain all Pods such that 0 <= getOrdinal(pod) < set.Spec.Replicas
	replicas := make([]*v1.Pod, replicaCount)
	// slice that will contain all Pods such that set.Spec.Replicas <= getOrdinal(pod)
	condemned := make([]*v1.Pod, 0, len(pods))
	unhealthy := 0
	var firstUnhealthyPod *v1.Pod

	// First we partition pods into two lists valid replicas and condemned Pods
	// 将pod列表区分为有效的和失效的列表
	// 遍历pod列表，然后将pod分表存到replicas列表和condemned列表中，在condemned列表中的pod表示这些pod是多余的，超过了statefulset设置的副本数，需要被删除掉的；
	for i := range pods {
		status.Replicas++

		// count the number of running and ready replicas
		// 如果已经ready了，那么计数加一
		if isRunningAndReady(pods[i]) {
			status.ReadyReplicas++
		}

		// count the number of current and update replicas
		// 为需要更新的pod计数
		if isCreated(pods[i]) && !isTerminating(pods[i]) {
			if getPodRevision(pods[i]) == currentRevision.Name {
				status.CurrentReplicas++
			}
			if getPodRevision(pods[i]) == updateRevision.Name {
				status.UpdatedReplicas++
			}
		}

		// getOrdinal是获取pod的序号
		if ord := getOrdinal(pods[i]); 0 <= ord && ord < replicaCount {
			// if the ordinal of the pod is within the range of the current number of replicas,
			// insert it at the indirection of its ordinal
			replicas[ord] = pods[i]
		// 如果序号大于statefulset设置的副本数，那么放入到condemned集合中，等待销毁
		} else if ord >= replicaCount {
			// if the ordinal is greater than the number of replicas add it to the condemned list
			condemned = append(condemned, pods[i])
		}
		// If the ordinal could not be parsed (ord < 0), ignore the Pod.
	}

	// for any empty indices in the sequence [0,set.Spec.Replicas) create a new Pod at the correct revision
	// 如果对应的序号中没有对应的pod，那么需要创建新的pod
	// 遍历replicas列表和condemned列表，找到pod中序号最小的不健康的pod
	for ord := 0; ord < replicaCount; ord++ {
		if replicas[ord] == nil {
			replicas[ord] = newVersionedStatefulSetPod(
				currentSet,
				updateSet,
				currentRevision.Name,
				updateRevision.Name, ord)
		}
	}

	// sort the condemned Pods by their ordinals
	sort.Sort(ascendingOrdinal(condemned))

	// find the first unhealthy Pod
	// 找到副本集合中状态不正常的pod
	for i := range replicas {
		if !isHealthy(replicas[i]) {
			unhealthy++
			// 找到第一个不正常的pod的序号
			if firstUnhealthyPod == nil {
				firstUnhealthyPod = replicas[i]
			}
		}
	}

	// 从失效pod集合中找到第一个不正常pod的序号
	for i := range condemned {
		if !isHealthy(condemned[i]) {
			unhealthy++
			if firstUnhealthyPod == nil {
				firstUnhealthyPod = condemned[i]
			}
		}
	}

	if unhealthy > 0 {
		klog.V(4).Infof("StatefulSet %s/%s has %d unhealthy Pods starting with %s",
			set.Namespace,
			set.Name,
			unhealthy,
			firstUnhealthyPod.Name)
	}

	// If the StatefulSet is being deleted, don't do anything other than updating
	// status.
	// 检查StatefulSet是否已经被删除
	if set.DeletionTimestamp != nil {
		return &status, nil
	}

	// 我们默认的状态是OrderedReady，所以monotonic是true; 也就是说在扩缩容的时候会等待pod状态为ready才会继续
	monotonic := !allowsBurst(set)

	// Examine each replica with respect to its ordinal
	// 检查副本集合里面是不是所有的pod都遵循序号递增原则
	for i := range replicas {
		// delete and recreate failed pods
		// 删除然后创新创建 fail状态的pod
		if isFailed(replicas[i]) {
			ssc.recorder.Eventf(set, v1.EventTypeWarning, "RecreatingFailedPod",
				"StatefulSet %s/%s is recreating failed Pod %s",
				set.Namespace,
				set.Name,
				replicas[i].Name)
			if err := ssc.podControl.DeleteStatefulPod(set, replicas[i]); err != nil {
				return &status, err
			}
			if getPodRevision(replicas[i]) == currentRevision.Name {
				status.CurrentReplicas--
			}
			if getPodRevision(replicas[i]) == updateRevision.Name {
				status.UpdatedReplicas--
			}
			status.Replicas--
			replicas[i] = newVersionedStatefulSetPod(
				currentSet,
				updateSet,
				currentRevision.Name,
				updateRevision.Name,
				i)
		}
		// If we find a Pod that has not been created we create the Pod
		// 如果发现一个pod还没被创建，那么创建一下这个pod
		if !isCreated(replicas[i]) {
			if err := ssc.podControl.CreateStatefulPod(set, replicas[i]); err != nil {
				return &status, err
			}
			status.Replicas++
			if getPodRevision(replicas[i]) == currentRevision.Name {
				status.CurrentReplicas++
			}
			if getPodRevision(replicas[i]) == updateRevision.Name {
				status.UpdatedReplicas++
			}

			// if the set does not allow bursting, return immediately
			if monotonic {
				return &status, nil
			}
			// pod created, no more work possible for this round
			continue
		}
		// If we find a Pod that is currently terminating, we must wait until graceful deletion
		// completes before we continue to make progress.
		// 如果发现这个pod处于terminating状态，需要等到这个pod被优雅的删除后才继续执行，所以先return
		if isTerminating(replicas[i]) && monotonic {
			klog.V(4).Infof(
				"StatefulSet %s/%s is waiting for Pod %s to Terminate",
				set.Namespace,
				set.Name,
				replicas[i].Name)
			return &status, nil
		}
		// If we have a Pod that has been created but is not running and ready we can not make progress.
		// We must ensure that all for each Pod, when we create it, all of its predecessors, with respect to its
		// ordinal, are Running and Ready.
		// 如果一个pod不是处于running和ready中动态，那么也不能继续
		if !isRunningAndReady(replicas[i]) && monotonic {
			klog.V(4).Infof(
				"StatefulSet %s/%s is waiting for Pod %s to be Running and Ready",
				set.Namespace,
				set.Name,
				replicas[i].Name)
			return &status, nil
		}
		// Enforce the StatefulSet invariants
		// 强制执行StatefulSet不变性
		if identityMatches(set, replicas[i]) && storageMatches(set, replicas[i]) {
			continue
		}
		// Make a deep copy so we don't mutate the shared cache
		replica := replicas[i].DeepCopy()
		// 指定更新操作
		if err := ssc.podControl.UpdateStatefulPod(updateSet, replica); err != nil {
			return &status, err
		}
	}

	// At this point, all of the current Replicas are Running and Ready, we can consider termination.
	// We will wait for all predecessors to be Running and Ready prior to attempting a deletion.
	// We will terminate Pods in a monotonically decreasing order over [len(pods),set.Spec.Replicas).
	// Note that we do not resurrect Pods in this interval. Also note that scaling will take precedence over
	// updates.
	// 此时，所有当前副本都在运行并准备就绪，我们可以考虑终止。在尝试删除之前，我们将等待所有的前任程序运行并准备就绪。
	// 我们将在[len(Pods)，set.Spec.Replicas)之上以单调递减的顺序终止Pods。注意，我们不会在这个时间间隔中复活pod。还要注意，扩展优先于更新
	// 遍历condemned列表的时候是从后往前遍历的，扩容将优于更新
	for target := len(condemned) - 1; target >= 0; target-- {
		// wait for terminating pods to expire
		// 等待处于Terminating的pod终止
		if isTerminating(condemned[target]) {
			klog.V(4).Infof(
				"StatefulSet %s/%s is waiting for Pod %s to Terminate prior to scale down",
				set.Namespace,
				set.Name,
				condemned[target].Name)
			// block if we are in monotonic mode
			if monotonic {
				return &status, nil
			}
			continue
		}
		// if we are in monotonic mode and the condemned target is not the first unhealthy Pod block
		// 如果pod没有处于Running 或Ready状态，并且这个pod不是第一个不正常的pod，那么等待此pod运行
		if !isRunningAndReady(condemned[target]) && monotonic && condemned[target] != firstUnhealthyPod {
			klog.V(4).Infof(
				"StatefulSet %s/%s is waiting for Pod %s to be Running and Ready prior to scale down",
				set.Namespace,
				set.Name,
				firstUnhealthyPod.Name)
			return &status, nil
		}
		klog.V(2).Infof("StatefulSet %s/%s terminating Pod %s for scale down",
			set.Namespace,
			set.Name,
			condemned[target].Name)

		// 删除此pod
		if err := ssc.podControl.DeleteStatefulPod(set, condemned[target]); err != nil {
			return &status, err
		}
		if getPodRevision(condemned[target]) == currentRevision.Name {
			status.CurrentReplicas--
		}
		if getPodRevision(condemned[target]) == updateRevision.Name {
			status.UpdatedReplicas--
		}
		if monotonic {
			return &status, nil
		}
	}

	// for the OnDelete strategy we short circuit. Pods will be updated when they are manually deleted.
	// 如果UpdateStrategy是OnDelete，那么pod需要手动删除，所以直接返回
	if set.Spec.UpdateStrategy.Type == apps.OnDeleteStatefulSetStrategyType {
		return &status, nil
	}

	// we compute the minimum ordinal of the target sequence for a destructive update based on the strategy.
	updateMin := 0
	// 滚动更新策略，没有设置Partition，那么默认是0
	if set.Spec.UpdateStrategy.RollingUpdate != nil {
		updateMin = int(*set.Spec.UpdateStrategy.RollingUpdate.Partition)
	}
	// we terminate the Pod with the largest ordinal that does not match the update revision.
	// 只会更新序号大于updateMin的pod，并且是倒序更新
	for target := len(replicas) - 1; target >= updateMin; target-- {

		// delete the Pod if it is not already terminating and does not match the update revision.
		// 如果该pod状态不是terminating，并且该pod没有被更新，那么删除该pod
		if getPodRevision(replicas[target]) != updateRevision.Name && !isTerminating(replicas[target]) {
			klog.V(2).Infof("StatefulSet %s/%s terminating Pod %s for update",
				set.Namespace,
				set.Name,
				replicas[target].Name)
			err := ssc.podControl.DeleteStatefulPod(set, replicas[target])
			status.CurrentReplicas--
			return &status, err
		}

		// wait for unhealthy Pods on update
		// 等待不健康的pod更新
		if !isHealthy(replicas[target]) {
			klog.V(4).Infof(
				"StatefulSet %s/%s is waiting for Pod %s to update",
				set.Namespace,
				set.Name,
				replicas[target].Name)
			return &status, nil
		}

	}
	return &status, nil
}

// updateStatefulSetStatus updates set's Status to be equal to status. If status indicates a complete update, it is
// mutated to indicate completion. If status is semantically equivalent to set's Status no update is performed. If the
// returned error is nil, the update is successful.
// updateStatefulSetStatus更新set的状态为与Status相等。如果状态指示完成更新，则将其更改为指示完成。
// 如果status在语义上等价于set的状态，则不执行更新。如果返回的错误是nil，则更新成功
func (ssc *defaultStatefulSetControl) updateStatefulSetStatus(
	set *apps.StatefulSet,
	status *apps.StatefulSetStatus) error {

	// complete any in progress rolling update if necessary
	completeRollingUpdate(set, status)

	// if the status is not inconsistent do not perform an update
	if !inconsistentStatus(set, status) {
		return nil
	}

	// copy set and update its status
	set = set.DeepCopy()
	if err := ssc.statusUpdater.UpdateStatefulSetStatus(set, status); err != nil {
		return err
	}

	return nil
}

var _ StatefulSetControlInterface = &defaultStatefulSetControl{}
