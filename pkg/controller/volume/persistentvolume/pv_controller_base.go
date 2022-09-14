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

package persistentvolume

import (
	"context"
	"fmt"
	"strconv"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	storageinformers "k8s.io/client-go/informers/storage/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	cloudprovider "k8s.io/cloud-provider"
	csitrans "k8s.io/csi-translation-lib"
	"k8s.io/kubernetes/pkg/controller"
	"k8s.io/kubernetes/pkg/controller/volume/common"
	"k8s.io/kubernetes/pkg/controller/volume/persistentvolume/metrics"
	pvutil "k8s.io/kubernetes/pkg/controller/volume/persistentvolume/util"
	"k8s.io/kubernetes/pkg/util/goroutinemap"
	vol "k8s.io/kubernetes/pkg/volume"
	"k8s.io/kubernetes/pkg/volume/csimigration"

	"k8s.io/klog/v2"
)

// This file contains the controller base functionality, i.e. framework to
// process PV/PVC added/updated/deleted events. The real binding, provisioning,
// recycling and deleting is done in pv_controller.go

// ControllerParameters contains arguments for creation of a new
// PersistentVolume controller.
// ControllerParameters 包含用于创建新PersistentVolume控制器的参数
type ControllerParameters struct {
	KubeClient                clientset.Interface
	SyncPeriod                time.Duration
	VolumePlugins             []vol.VolumePlugin
	Cloud                     cloudprovider.Interface
	ClusterName               string
	VolumeInformer            coreinformers.PersistentVolumeInformer
	ClaimInformer             coreinformers.PersistentVolumeClaimInformer
	ClassInformer             storageinformers.StorageClassInformer
	PodInformer               coreinformers.PodInformer
	NodeInformer              coreinformers.NodeInformer
	EventRecorder             record.EventRecorder
	EnableDynamicProvisioning bool
}

// NewController creates a new PersistentVolume controller
// NewController 创建一个新的PersistentVolume控制器, 在 cmd/kube-controller-manager/app/controllermanager.go 代码里面被 NewControllerInitializers() 调用
func NewController(p ControllerParameters) (*PersistentVolumeController, error) {
	// 初始化 eventRecorder
	eventRecorder := p.EventRecorder
	if eventRecorder == nil {
		broadcaster := record.NewBroadcaster()
		broadcaster.StartStructuredLogging(0)
		broadcaster.StartRecordingToSink(&v1core.EventSinkImpl{Interface: p.KubeClient.CoreV1().Events("")})
		eventRecorder = broadcaster.NewRecorder(scheme.Scheme, v1.EventSource{Component: "persistentvolume-controller"})
	}

	// 初始化 PersistentVolumeController 对象
	controller := &PersistentVolumeController{
		volumes:                       newPersistentVolumeOrderedIndex(),
		claims:                        cache.NewStore(cache.DeletionHandlingMetaNamespaceKeyFunc),
		kubeClient:                    p.KubeClient,
		eventRecorder:                 eventRecorder,
		runningOperations:             goroutinemap.NewGoRoutineMap(true /* exponentialBackOffOnError */),
		cloud:                         p.Cloud,
		enableDynamicProvisioning:     p.EnableDynamicProvisioning,
		clusterName:                   p.ClusterName,
		createProvisionedPVRetryCount: createProvisionedPVRetryCount,
		createProvisionedPVInterval:   createProvisionedPVInterval,
		claimQueue:                    workqueue.NewNamed("claims"),
		volumeQueue:                   workqueue.NewNamed("volumes"),
		resyncPeriod:                  p.SyncPeriod,
		operationTimestamps:           metrics.NewOperationStartTimeCache(),
	}

	// Prober is nil because PV is not aware of Flexvolume.
	// 探测器(Prober)为nil，因为PV不知道Flexvolume
	// 调用 VolumePluginMgr.InitPlugins()  方法 初始化存储插件
	if err := controller.volumePluginMgr.InitPlugins(p.VolumePlugins, nil /* prober */, controller); err != nil {
		return nil, fmt.Errorf("Could not initialize volume plugins for PersistentVolume Controller: %v", err)
	}

	// 开始创建 informer 监听集群内的资源，初始化了如下 informer, 并且将 PV & PVC 的 event 分别放入 volumeQueue & claimQueue
	// 初始化 PersistentVolumeInformer
	p.VolumeInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { controller.enqueueWork(controller.volumeQueue, obj) },
			UpdateFunc: func(oldObj, newObj interface{}) { controller.enqueueWork(controller.volumeQueue, newObj) },
			DeleteFunc: func(obj interface{}) { controller.enqueueWork(controller.volumeQueue, obj) },
		},
	)
	controller.volumeLister = p.VolumeInformer.Lister()
	controller.volumeListerSynced = p.VolumeInformer.Informer().HasSynced

	// 初始化 PersistentVolumeClaimInformer
	p.ClaimInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { controller.enqueueWork(controller.claimQueue, obj) },
			UpdateFunc: func(oldObj, newObj interface{}) { controller.enqueueWork(controller.claimQueue, newObj) },
			DeleteFunc: func(obj interface{}) { controller.enqueueWork(controller.claimQueue, obj) },
		},
	)
	controller.claimLister = p.ClaimInformer.Lister()
	controller.claimListerSynced = p.ClaimInformer.Informer().HasSynced

	// 初始化 StorageClassInformer
	controller.classLister = p.ClassInformer.Lister()
	controller.classListerSynced = p.ClassInformer.Informer().HasSynced

	// 初始化 PodInformer
	controller.podLister = p.PodInformer.Lister()
	controller.podIndexer = p.PodInformer.Informer().GetIndexer()
	controller.podListerSynced = p.PodInformer.Informer().HasSynced

	// 初始化 NodeInformer
	controller.NodeLister = p.NodeInformer.Lister()
	controller.NodeListerSynced = p.NodeInformer.Informer().HasSynced

	// This custom indexer will index pods by its PVC keys. Then we don't need
	// to iterate all pods every time to find pods which reference given PVC.
	// 这个自定义索引器将通过其 PVC 键索引 pod。 然后不需要每次都迭代所有的 pod 来找到引用给定 PVC 的 pod
	// 为了不每次都迭代 pods ，自定义一个通过 pvc 键索引 pod 的索引器
	if err := common.AddPodPVCIndexerIfNotPresent(controller.podIndexer); err != nil {
		return nil, fmt.Errorf("Could not initialize attach detach controller: %v", err)
	}

	// 初始化 intree 存储 -> csi 迁移相关功能的 manager
	csiTranslator := csitrans.New()
	controller.translator = csiTranslator
	controller.csiMigratedPluginManager = csimigration.NewPluginManager(csiTranslator)

	return controller, nil
}

// initializeCaches fills all controller caches with initial data from etcd in
// order to have the caches already filled when first addClaim/addVolume to
// perform initial synchronization of the controller.
// initializeCaches 用etcd中的初始数据填充所有的控制器缓存，以便在第一次addClaim/addVolume执行控制器初始同步时，缓存已经被填充
func (ctrl *PersistentVolumeController) initializeCaches(volumeLister corelisters.PersistentVolumeLister, claimLister corelisters.PersistentVolumeClaimLister) {
	// 这里不访问 apiserver，是从本地缓存拿出的对象，这些对象不可以被外部函数修改
	volumeList, err := volumeLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("PersistentVolumeController can't initialize caches: %v", err)
		return
	}
	for _, volume := range volumeList {
		// 不能改变 volume 对象，所以这里copy一份新对象，对新对象进行操作
		volumeClone := volume.DeepCopy()
		if _, err = ctrl.storeVolumeUpdate(volumeClone); err != nil {
			klog.Errorf("error updating volume cache: %v", err)
		}
	}

	// 该方法将 cache.listener 里面的缓存转存在 persistentVolumeOrderedIndex 中，它是按 AccessModes 索引并按存储容量排序的 persistentVolume 的缓存
	claimList, err := claimLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("PersistentVolumeController can't initialize caches: %v", err)
		return
	}
	for _, claim := range claimList {
		if _, err = ctrl.storeClaimUpdate(claim.DeepCopy()); err != nil {
			klog.Errorf("error updating claim cache: %v", err)
		}
	}
	klog.V(4).Infof("controller initialized")
}

// enqueueWork adds volume or claim to given work queue.
// enqueueWork 向给定的工作队列添加 volume or claim
func (ctrl *PersistentVolumeController) enqueueWork(queue workqueue.Interface, obj interface{}) {
	// Beware of "xxx deleted" events
	if unknown, ok := obj.(cache.DeletedFinalStateUnknown); ok && unknown.Obj != nil {
		obj = unknown.Obj
	}
	objName, err := controller.KeyFunc(obj)
	if err != nil {
		klog.Errorf("failed to get key from object: %v", err)
		return
	}
	klog.V(5).Infof("enqueued %q for sync", objName)
	queue.Add(objName)
}

func (ctrl *PersistentVolumeController) storeVolumeUpdate(volume interface{}) (bool, error) {
	return storeObjectUpdate(ctrl.volumes.store, volume, "volume")
}

func (ctrl *PersistentVolumeController) storeClaimUpdate(claim interface{}) (bool, error) {
	return storeObjectUpdate(ctrl.claims, claim, "claim")
}

// updateVolume runs in worker thread and handles "volume added",
// "volume updated" and "periodic sync" events.
// updateVolume 在worker线程中运行并处理“添加卷”、“更新卷”和“定期同步”事件。
// updateVolume 方法是对于集群内的 events 实际 handler 方法，它里面主要调用了 ctrl.syncVolume 方法来处理
func (ctrl *PersistentVolumeController) updateVolume(volume *v1.PersistentVolume) {
	// Store the new volume version in the cache and do not process it if this
	// is an old version.
	// 将新卷版本存储在缓存中，如果是旧版本，则不要处理它
	new, err := ctrl.storeVolumeUpdate(volume)
	if err != nil {
		klog.Errorf("%v", err)
	}
	if !new {
		return
	}

	// Tips: 重点关注
	err = ctrl.syncVolume(volume)
	if err != nil {
		if errors.IsConflict(err) {
			// Version conflict error happens quite often and the controller
			// recovers from it easily.
			// 版本冲突错误经常发生，控制器很容易从中恢复
			klog.V(3).Infof("could not sync volume %q: %+v", volume.Name, err)
		} else {
			klog.Errorf("could not sync volume %q: %+v", volume.Name, err)
		}
	}
}

// deleteVolume runs in worker thread and handles "volume deleted" event.
func (ctrl *PersistentVolumeController) deleteVolume(volume *v1.PersistentVolume) {
	if err := ctrl.volumes.store.Delete(volume); err != nil {
		klog.Errorf("volume %q deletion encountered : %v", volume.Name, err)
	} else {
		klog.V(4).Infof("volume %q deleted", volume.Name)
	}
	// record deletion metric if a deletion start timestamp is in the cache
	// the following calls will be a no-op if there is nothing for this volume in the cache
	// end of timestamp cache entry lifecycle, "RecordMetric" will do the clean
	metrics.RecordMetric(volume.Name, &ctrl.operationTimestamps, nil)

	if volume.Spec.ClaimRef == nil {
		return
	}
	// sync the claim when its volume is deleted. Explicitly syncing the
	// claim here in response to volume deletion prevents the claim from
	// waiting until the next sync period for its Lost status.
	claimKey := claimrefToClaimKey(volume.Spec.ClaimRef)
	klog.V(5).Infof("deleteVolume[%s]: scheduling sync of claim %q", volume.Name, claimKey)
	ctrl.claimQueue.Add(claimKey)
}

// updateClaim runs in worker thread and handles "claim added",
// "claim updated" and "periodic sync" events.
// updateClaim 运行在工作线程中并处理“声明已添加”、“声明已更新”和“定期同步”事件。
// 它里面主要调用了 ctrl.syncClaim 方法来处理， 在 syncClaim 里面根据 pvc 的状态分别调用了 ctrl.syncUnboundClaim & ctrl.syncBoundClaim 方法来处理
func (ctrl *PersistentVolumeController) updateClaim(claim *v1.PersistentVolumeClaim) {
	// Store the new claim version in the cache and do not process it if this is
	// an old version.
	// 将新的claim版本存储在缓存中，如果这是旧版本，则不要对其进行处理
	new, err := ctrl.storeClaimUpdate(claim)
	if err != nil {
		klog.Errorf("%v", err)
	}
	if !new {
		return
	}
	err = ctrl.syncClaim(claim)
	if err != nil {
		if errors.IsConflict(err) {
			// Version conflict error happens quite often and the controller
			// recovers from it easily.
			// 版本冲突错误经常发生，控制器很容易从版本冲突中恢复过来
			klog.V(3).Infof("could not sync claim %q: %+v", claimToClaimKey(claim), err)
		} else {
			klog.Errorf("could not sync volume %q: %+v", claimToClaimKey(claim), err)
		}
	}
}

// Unit test [5-5] [5-6] [5-7]
// deleteClaim runs in worker thread and handles "claim deleted" event.
func (ctrl *PersistentVolumeController) deleteClaim(claim *v1.PersistentVolumeClaim) {
	if err := ctrl.claims.Delete(claim); err != nil {
		klog.Errorf("claim %q deletion encountered : %v", claim.Name, err)
	}
	claimKey := claimToClaimKey(claim)
	klog.V(4).Infof("claim %q deleted", claimKey)
	// clean any possible unfinished provision start timestamp from cache
	// Unit test [5-8] [5-9]
	ctrl.operationTimestamps.Delete(claimKey)

	volumeName := claim.Spec.VolumeName
	if volumeName == "" {
		klog.V(5).Infof("deleteClaim[%q]: volume not bound", claimKey)
		return
	}

	// sync the volume when its claim is deleted.  Explicitly sync'ing the
	// volume here in response to claim deletion prevents the volume from
	// waiting until the next sync period for its Release.
	klog.V(5).Infof("deleteClaim[%q]: scheduling sync of volume %s", claimKey, volumeName)
	ctrl.volumeQueue.Add(volumeName)
}

// Run starts all of this controller's control loops
// Run 启动所有这个控制器的控制循环
func (ctrl *PersistentVolumeController) Run(stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ctrl.claimQueue.ShutDown()
	defer ctrl.volumeQueue.ShutDown()

	klog.Infof("Starting persistent volume controller")
	defer klog.Infof("Shutting down persistent volume controller")

	// 同步缓存
	if !cache.WaitForNamedCacheSync("persistent volume", stopCh, ctrl.volumeListerSynced, ctrl.claimListerSynced, ctrl.classListerSynced, ctrl.podListerSynced, ctrl.NodeListerSynced) {
		return
	}

	// 用etcd中的初始数据填充所有的控制器缓存
	ctrl.initializeCaches(ctrl.volumeLister, ctrl.claimLister)

	// 同步缓存之后开始周期性执行 ctrl.resync, ctrl.volumeWorker , ctrl.claimWorker
	// 将集群内所有的 pvc/pv 统一都放到对应的 claimQueue & volumeQueue 里面重新处理, 这个 resyncPeriod 等于一个 random time.Duration * config.time(在 kcm 启动时设置)
	go wait.Until(ctrl.resync, ctrl.resyncPeriod, stopCh)
	// 一个无限循环， 不断的处理从 volumeQueue 里面获取到的 PersistentVolume
	go wait.Until(ctrl.volumeWorker, time.Second, stopCh)
	// 一个无限循环，不断的处理从 claimQueue 里面获取到的 PersistentVolumeClaim
	go wait.Until(ctrl.claimWorker, time.Second, stopCh)

	metrics.Register(ctrl.volumes.store, ctrl.claims)

	<-stopCh
}

func (ctrl *PersistentVolumeController) updateClaimMigrationAnnotations(claim *v1.PersistentVolumeClaim) (*v1.PersistentVolumeClaim, error) {
	// TODO: update[Claim|Volume]MigrationAnnotations can be optimized to not
	// copy the claim/volume if no modifications are required. Though this
	// requires some refactoring as well as an interesting change in the
	// semantics of the function which may be undesirable. If no copy is made
	// when no modifications are required this function could sometimes return a
	// copy of the volume and sometimes return a ref to the original
	claimClone := claim.DeepCopy()
	modified := updateMigrationAnnotations(ctrl.csiMigratedPluginManager, ctrl.translator, claimClone.Annotations, pvutil.AnnStorageProvisioner)
	if !modified {
		return claimClone, nil
	}
	newClaim, err := ctrl.kubeClient.CoreV1().PersistentVolumeClaims(claimClone.Namespace).Update(context.TODO(), claimClone, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("persistent Volume Controller can't anneal migration annotations: %v", err)
	}
	_, err = ctrl.storeClaimUpdate(newClaim)
	if err != nil {
		return nil, fmt.Errorf("persistent Volume Controller can't anneal migration annotations: %v", err)
	}
	return newClaim, nil
}

func (ctrl *PersistentVolumeController) updateVolumeMigrationAnnotations(volume *v1.PersistentVolume) (*v1.PersistentVolume, error) {
	volumeClone := volume.DeepCopy()
	modified := updateMigrationAnnotations(ctrl.csiMigratedPluginManager, ctrl.translator, volumeClone.Annotations, pvutil.AnnDynamicallyProvisioned)
	if !modified {
		return volumeClone, nil
	}
	newVol, err := ctrl.kubeClient.CoreV1().PersistentVolumes().Update(context.TODO(), volumeClone, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("persistent Volume Controller can't anneal migration annotations: %v", err)
	}
	_, err = ctrl.storeVolumeUpdate(newVol)
	if err != nil {
		return nil, fmt.Errorf("persistent Volume Controller can't anneal migration annotations: %v", err)
	}
	return newVol, nil

}

// updateMigrationAnnotations takes an Annotations map and checks for a
// provisioner name using the provisionerKey. It will then add a
// "volume.beta.kubernetes.io/migrated-to" annotation if migration with the CSI
// driver name for that provisioner is "on" based on feature flags, it will also
// remove the annotation is migration is "off" for that provisioner in rollback
// scenarios. Returns true if the annotations map was modified and false otherwise.
func updateMigrationAnnotations(cmpm CSIMigratedPluginManager, translator CSINameTranslator, ann map[string]string, provisionerKey string) bool {
	var csiDriverName string
	var err error

	if ann == nil {
		// No annotations so we can't get the provisioner and don't know whether
		// this is migrated - no change
		return false
	}
	provisioner, ok := ann[provisionerKey]
	if !ok {
		// Volume not dynamically provisioned. Ignore
		return false
	}

	migratedToDriver := ann[pvutil.AnnMigratedTo]
	if cmpm.IsMigrationEnabledForPlugin(provisioner) {
		csiDriverName, err = translator.GetCSINameFromInTreeName(provisioner)
		if err != nil {
			klog.Errorf("Could not update volume migration annotations. Migration enabled for plugin %s but could not find corresponding driver name: %v", provisioner, err)
			return false
		}
		if migratedToDriver != csiDriverName {
			ann[pvutil.AnnMigratedTo] = csiDriverName
			return true
		}
	} else {
		if migratedToDriver != "" {
			// Migration annotation exists but the driver isn't migrated currently
			delete(ann, pvutil.AnnMigratedTo)
			return true
		}
	}
	return false
}

// volumeWorker processes items from volumeQueue. It must run only once,
// syncVolume is not assured to be reentrant.
// volumeWorker 处理来自 volumeQueue 的项。 它只能运行一次，不能保证syncVolume是可重入的
func (ctrl *PersistentVolumeController) volumeWorker() {
	workFunc := func() bool {
		// 获取 PV
		keyObj, quit := ctrl.volumeQueue.Get()
		if quit {
			return true
		}
		// 将对应的key 标记为 已完成
		defer ctrl.volumeQueue.Done(keyObj)
		key := keyObj.(string)
		klog.V(5).Infof("volumeWorker[%s]", key)

		// 切割 meta 下的 namespace 和 name
		_, name, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			klog.V(4).Infof("error getting name of volume %q to get volume from informer: %v", key, err)
			return false
		}
		// 根据 PV 名 获取对应的 volume
		volume, err := ctrl.volumeLister.Get(name)
		if err == nil {
			// The volume still exists in informer cache, the event must have
			// been add/update/sync
			// volume 仍然存在于 informer 缓存中，event(事件)一定是添加/更新/同步
			ctrl.updateVolume(volume)
			return false
		}
		if !errors.IsNotFound(err) {
			klog.V(2).Infof("error getting volume %q from informer: %v", key, err)
			return false
		}

		// The volume is not in informer cache, the event must have been
		// "delete"
		// 该volume不在 Informer 缓存中，event 必须已被 delete(删除)
		volumeObj, found, err := ctrl.volumes.store.GetByKey(key)
		if err != nil {
			klog.V(2).Infof("error getting volume %q from cache: %v", key, err)
			return false
		}
		if !found {
			// The controller has already processed the delete event and
			// deleted the volume from its cache
			// controller已经处理了删除事件，并将 volume 从cache中删除
			klog.V(2).Infof("deletion of volume %q was already processed", key)
			return false
		}
		volume, ok := volumeObj.(*v1.PersistentVolume)
		if !ok {
			klog.Errorf("expected volume, got %+v", volumeObj)
			return false
		}
		ctrl.deleteVolume(volume)
		return false
	}
	for {
		if quit := workFunc(); quit {
			klog.Infof("volume worker queue shutting down")
			return
		}
	}
}

// claimWorker processes items from claimQueue. It must run only once,
// syncClaim is not reentrant.
// 处理来自claimQueue的项。 它只能运行一次，syncClaim是不可重入的。
func (ctrl *PersistentVolumeController) claimWorker() {
	workFunc := func() bool {
		// 查看队列是否为空，并且 shuttingDown 标志被赋值 True
		keyObj, quit := ctrl.claimQueue.Get()
		if quit {
			// 队列为空并且shuttingDown 为 True 结束循环
			return true
		}
		defer ctrl.claimQueue.Done(keyObj)
		key := keyObj.(string)
		klog.V(5).Infof("claimWorker[%s]", key)

		// 获取 namespace 和 name
		namespace, name, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			klog.V(4).Infof("error getting namespace & name of claim %q to get claim from informer: %v", key, err)
			return false
		}
		// 查找对应 PVC
		claim, err := ctrl.claimLister.PersistentVolumeClaims(namespace).Get(name)
		if err == nil {
			// The claim still exists in informer cache, the event must have
			// been add/update/sync
			// Tips: 该claim仍然存在于 Informer 缓存中，该event必须已添加/更新/同步
			ctrl.updateClaim(claim)
			return false
		}
		if !errors.IsNotFound(err) {
			klog.V(2).Infof("error getting claim %q from informer: %v", key, err)
			return false
		}

		// The claim is not in informer cache, the event must have been "delete"
		// claim不在 Informer 缓存中，event必须已“删除”
		claimObj, found, err := ctrl.claims.GetByKey(key)
		if err != nil {
			klog.V(2).Infof("error getting claim %q from cache: %v", key, err)
			return false
		}
		if !found {
			// The controller has already processed the delete event and
			// deleted the claim from its cache
			// 控制器已经处理了delete事件，并从其缓存中删除了claim
			klog.V(2).Infof("deletion of claim %q was already processed", key)
			return false
		}
		claim, ok := claimObj.(*v1.PersistentVolumeClaim)
		if !ok {
			klog.Errorf("expected claim, got %+v", claimObj)
			return false
		}
		ctrl.deleteClaim(claim)
		return false
	}
	for {
		if quit := workFunc(); quit {
			klog.Infof("claim worker queue shutting down")
			return
		}
	}
}

// resync supplements short resync period of shared informers - we don't want
// all consumers of PV/PVC shared informer to have a short resync period,
// therefore we do our own.
// 将集群内所有的 pvc/pv 统一都放到对应的 claimQueue & volumeQueue 里面重新处理。
func (ctrl *PersistentVolumeController) resync() {
	klog.V(4).Infof("resyncing PV controller")

	pvcs, err := ctrl.claimLister.List(labels.NewSelector())
	if err != nil {
		klog.Warningf("cannot list claims: %s", err)
		return
	}
	for _, pvc := range pvcs {
		// 将 pvc 添加到 claimQueue
		ctrl.enqueueWork(ctrl.claimQueue, pvc)
	}

	pvs, err := ctrl.volumeLister.List(labels.NewSelector())
	if err != nil {
		klog.Warningf("cannot list persistent volumes: %s", err)
		return
	}
	for _, pv := range pvs {
		// 将 pv 添加到 volumeQueue
		ctrl.enqueueWork(ctrl.volumeQueue, pv)
	}
}

// setClaimProvisioner saves
// claim.Annotations[pvutil.AnnStorageProvisioner] = class.Provisioner
func (ctrl *PersistentVolumeController) setClaimProvisioner(claim *v1.PersistentVolumeClaim, provisionerName string) (*v1.PersistentVolumeClaim, error) {
	if val, ok := claim.Annotations[pvutil.AnnStorageProvisioner]; ok && val == provisionerName {
		// annotation is already set, nothing to do
		return claim, nil
	}

	// The volume from method args can be pointing to watcher cache. We must not
	// modify these, therefore create a copy.
	claimClone := claim.DeepCopy()
	metav1.SetMetaDataAnnotation(&claimClone.ObjectMeta, pvutil.AnnStorageProvisioner, provisionerName)
	updateMigrationAnnotations(ctrl.csiMigratedPluginManager, ctrl.translator, claimClone.Annotations, pvutil.AnnStorageProvisioner)
	newClaim, err := ctrl.kubeClient.CoreV1().PersistentVolumeClaims(claim.Namespace).Update(context.TODO(), claimClone, metav1.UpdateOptions{})
	if err != nil {
		return newClaim, err
	}
	_, err = ctrl.storeClaimUpdate(newClaim)
	if err != nil {
		return newClaim, err
	}
	return newClaim, nil
}

// Stateless functions

func getClaimStatusForLogging(claim *v1.PersistentVolumeClaim) string {
	bound := metav1.HasAnnotation(claim.ObjectMeta, pvutil.AnnBindCompleted)
	boundByController := metav1.HasAnnotation(claim.ObjectMeta, pvutil.AnnBoundByController)

	return fmt.Sprintf("phase: %s, bound to: %q, bindCompleted: %v, boundByController: %v", claim.Status.Phase, claim.Spec.VolumeName, bound, boundByController)
}

func getVolumeStatusForLogging(volume *v1.PersistentVolume) string {
	boundByController := metav1.HasAnnotation(volume.ObjectMeta, pvutil.AnnBoundByController)
	claimName := ""
	if volume.Spec.ClaimRef != nil {
		claimName = fmt.Sprintf("%s/%s (uid: %s)", volume.Spec.ClaimRef.Namespace, volume.Spec.ClaimRef.Name, volume.Spec.ClaimRef.UID)
	}
	return fmt.Sprintf("phase: %s, bound to: %q, boundByController: %v", volume.Status.Phase, claimName, boundByController)
}

// storeObjectUpdate updates given cache with a new object version from Informer
// callback (i.e. with events from etcd) or with an object modified by the
// controller itself. Returns "true", if the cache was updated, false if the
// object is an old version and should be ignored.
func storeObjectUpdate(store cache.Store, obj interface{}, className string) (bool, error) {
	objName, err := controller.KeyFunc(obj)
	if err != nil {
		return false, fmt.Errorf("Couldn't get key for object %+v: %v", obj, err)
	}
	oldObj, found, err := store.Get(obj)
	if err != nil {
		return false, fmt.Errorf("Error finding %s %q in controller cache: %v", className, objName, err)
	}

	objAccessor, err := meta.Accessor(obj)
	if err != nil {
		return false, err
	}

	if !found {
		// This is a new object
		klog.V(4).Infof("storeObjectUpdate: adding %s %q, version %s", className, objName, objAccessor.GetResourceVersion())
		if err = store.Add(obj); err != nil {
			return false, fmt.Errorf("Error adding %s %q to controller cache: %v", className, objName, err)
		}
		return true, nil
	}

	oldObjAccessor, err := meta.Accessor(oldObj)
	if err != nil {
		return false, err
	}

	objResourceVersion, err := strconv.ParseInt(objAccessor.GetResourceVersion(), 10, 64)
	if err != nil {
		return false, fmt.Errorf("Error parsing ResourceVersion %q of %s %q: %s", objAccessor.GetResourceVersion(), className, objName, err)
	}
	oldObjResourceVersion, err := strconv.ParseInt(oldObjAccessor.GetResourceVersion(), 10, 64)
	if err != nil {
		return false, fmt.Errorf("Error parsing old ResourceVersion %q of %s %q: %s", oldObjAccessor.GetResourceVersion(), className, objName, err)
	}

	// Throw away only older version, let the same version pass - we do want to
	// get periodic sync events.
	if oldObjResourceVersion > objResourceVersion {
		klog.V(4).Infof("storeObjectUpdate: ignoring %s %q version %s", className, objName, objAccessor.GetResourceVersion())
		return false, nil
	}

	klog.V(4).Infof("storeObjectUpdate updating %s %q with version %s", className, objName, objAccessor.GetResourceVersion())
	if err = store.Update(obj); err != nil {
		return false, fmt.Errorf("Error updating %s %q in controller cache: %v", className, objName, err)
	}
	return true, nil
}
