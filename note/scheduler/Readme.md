# scheduler 源码梳理

## 目录结构
cmd/kube-scheduler
```shell
├── BUILD
├── OWNERS
├── app
│   ├── BUILD
│   ├── config
│   │   ├── BUILD
│   │   ├── config.go         // 处理未设置的参数项  
│   │   └── config_test.go
│   ├── options
│   │   ├── BUILD
│   │   ├── configfile.go     // 从文件中读取配置，将配置写入yaml文件
│   │   ├── deprecated.go     // 处理废弃的参数项
│   │   ├── deprecated_test.go
│   │   ├── insecure_serving.go // 针对 insecure_serving 配置， 类似deprecated
│   │   ├── insecure_serving_test.go
│   │   ├── options.go        // 调度器配置处理实现
│   │   └── options_test.go
│   ├── server.go                   // 调度器初始化运行以及调用pkg实现 
│   ├── server_test.go
│   └── testing
│       ├── BUILD
│       └── testserver.go
└── scheduler.go                          // 调度器主入口
```

## 函数流转：
### 1. 从main出发，找到了scheduler主框架的入口(cmd -> pkg)
#### 1.1 `cmd/kube-scheduler/scheduler.go` -> main()

- kube-scheduler这个二进制文件在运行的时候是调用了`command.Execute()`函数背后的那个Run，那个Run躲在`command := app.NewSchedulerCommand()`这行代码调用的`NewSchedulerCommand()`方法里，这个方法一定返回了一个`*cobra.Command`类型的对象。

```go
func main() {
	rand.Seed(time.Now().UnixNano())
	command := app.NewSchedulerCommand()
	...

	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}
```


#### 1.2 `cmd/kube-scheduler/app/server.go` -> NewSchedulerCommand()

- schduler启动时调用了runCommand(cmd, args, opts)

```go
func NewSchedulerCommand(registryOptions ...Option) *cobra.Command {
	opts, err := options.NewOptions()
	if err != nil {
		klog.Fatalf("unable to initialize command options: %v", err)
	}

	cmd := &cobra.Command{
		Use: "kube-scheduler",
		...
		Run: func(cmd *cobra.Command, args []string) {
			if err := runCommand(cmd, opts, registryOptions...); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
		},
		Args: func(cmd *cobra.Command, args []string) error {
			for _, arg := range args {
				if len(arg) > 0 {
					return fmt.Errorf("%q does not take any arguments, got %q", cmd.CommandPath(), args)
				}
			}
			return nil
		},
	}
	...
	return cmd
}
```

#### 1.3 `cmd/kube-scheduler/app/server.go` -> runCommand()

- 这里是处理配置问题后调用了一个Run()函数，Run()的作用是基于给定的配置启动scheduler，它只会在出错时或者channel stopCh被关闭时才退出

```go
// runCommand运行调度(scheduler)程序
func runCommand(cmd *cobra.Command, opts *options.Options, registryOptions ...Option) error {
	verflag.PrintAndExitIfRequested()
	cliflag.PrintFlags(cmd.Flags())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cc, sched, err := Setup(ctx, opts, registryOptions...)
	if err != nil {
		return err
	}

	if len(opts.WriteConfigTo) > 0 {
		if err := options.WriteConfigFile(opts.WriteConfigTo, &cc.ComponentConfig); err != nil {
			return err
		}
		klog.Infof("Wrote configuration to: %s\n", opts.WriteConfigTo)
		return nil
	}

	return Run(ctx, cc, sched)
}
```

#### 1.4 `cmd/kube-scheduler/app/server.go` -> run()

- 这里最终是要跑sched.Run()这个方法来启动scheduler，sched.Run()方法已经在pkg下，具体位置是`pkg/scheduler/scheduler.go`-> Run(ctx context.Context)，也就是scheduler框架真正运行的逻辑了

```go
// Run基于给定的配置执行调度程序。 它只在错误或上下文完成时返回
func Run(ctx context.Context, cc *schedulerserverconfig.CompletedConfig, sched *scheduler.Scheduler) error {
	...
	// 配置注册 
	...
	// 准备事件广播器
	...
	// 设置健康检查
	...
	// 启动健康检查服务器
	...
	// 不安全的指标服务
	...
	// 安全服务 -> 对于安全处理程序，提前失败，从上面删除旧的错误循环
	...
	// 启动所有通知
	// 运行PodInformer，并运行InformerFactory。此部分的逻辑为client-go的informer机制
	go cc.PodInformer.Informer().Run(ctx.Done())
    cc.InformerFactory.Start(ctx.Done())
	
	// 在调度之前等待所有缓存同步
	cc.InformerFactory.WaitForCacheSync(ctx.Done())

	// 如果启用了领导者选举，则通过 LeaderElector 运行命令直到完成并退出
	if cc.LeaderElection != nil {
		cc.LeaderElection.Callbacks = leaderelection.LeaderCallbacks{
			OnStartedLeading: sched.Run,
			OnStoppedLeading: func() {
				klog.Fatalf("leaderelection lost")
			},
		}
		leaderElector, err := leaderelection.NewLeaderElector(*cc.LeaderElection)
		if err != nil {
			return fmt.Errorf("couldn't create leader elector: %v", err)
		}

		leaderElector.Run(ctx)

		return fmt.Errorf("lost lease")
	}

	// Leader的选举是禁用的，所以运行命令直到完成。
	sched.Run(ctx)
	return fmt.Errorf("finished without leader elect")
}
```


#### 1.5 `k8s.io/client-go/informers/factory.go` -> type SharedInformerFactory interface

在调度前等待cache同步

```go
type SharedInformerFactory interface {
	...
	WaitForCacheSync(stopCh <-chan struct{}) map[reflect.Type]bool
	...
}
```

InformerFactory.WaitForCacheSync等待所有启动的informer的cache进行同步，保持本地的store信息与etcd的信息是最新一致的。

```go
func (f *sharedInformerFactory) WaitForCacheSync(stopCh <-chan struct{}) map[reflect.Type]bool {
	informers := func() map[reflect.Type]cache.SharedIndexInformer {
		f.lock.Lock()
		defer f.lock.Unlock()

		informers := map[reflect.Type]cache.SharedIndexInformer{}
		for informerType, informer := range f.informers {
			if f.startedInformers[informerType] {
				informers[informerType] = informer
			}
		}
		return informers
	}()

	res := map[reflect.Type]bool{}
	for informType, informer := range informers {
		res[informType] = cache.WaitForCacheSync(stopCh, informer.HasSynced)
	}
	return res
}
```

调用cache.WaitForCacheSync

#### 1.6 `k8s.io/client-go/tools/cache/shared_informer.go` -> WaitForCacheSync(stopCh <-chan struct{}, cacheSyncs ...InformerSynced) bool

```go
func WaitForCacheSync(stopCh <-chan struct{}, cacheSyncs ...InformerSynced) bool {
	err := wait.PollImmediateUntil(syncedPollPeriod,
		func() (bool, error) {
			for _, syncFunc := range cacheSyncs {
				if !syncFunc() {
					return false, nil
				}
			}
			return true, nil
		},
		stopCh)
	if err != nil {
		klog.V(2).Infof("stop requested")
		return false
	}

	klog.V(4).Infof("caches populated")
	return true
}
```

#### 1.7 `pkg/scheduler/scheduler.go` -> (sched *Scheduler) Run(ctx context.Context) 

Scheduler这个struct和Scheduler的Run()方法

```go
// Scheduler 监视新的未调度的 pod。 它尝试找到它们适合的节点并将绑定写回 api server
type Scheduler struct {
	// It is expected that changes made via SchedulerCache will be observed
	// by NodeLister and Algorithm.
	// 预计通过SchedulerCache做出的改变将被NodeLister和Algorithm观察到
	SchedulerCache internalcache.Cache

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
	// SchedulingQueue 保存要调度的 Pod
	SchedulingQueue internalqueue.SchedulingQueue

	// Profiles are the scheduling profiles.
	// Profiles 是调度配置文件
	Profiles profile.Map

	scheduledPodsHasSynced func() bool

	client clientset.Interface
}
```

先等待cache同步，然后开启调度逻辑的goroutine

```go
// 开始运行 守望(watch) 和 调度(schedule )。 它等待缓存被同步，然后开始调度和阻塞，直到上下文完成。 
func (sched *Scheduler) Run(ctx context.Context) {
	if !cache.WaitForCacheSync(ctx.Done(), sched.scheduledPodsHasSynced) {
		return
	}
	sched.SchedulingQueue.Run()
	wait.UntilWithContext(ctx, sched.scheduleOne, 0)
	sched.SchedulingQueue.Close()
}
```


### 2. Scheduler的工作过程(pkg)

#### 2.1 `pkg/scheduler/internal/queue/scheduling_queue.go` -> func (p *PriorityQueue) Run()

sched.SchedulingQueue.Run() -> (p *PriorityQueue) Run()
```go
func (p *PriorityQueue) Run() {
	go wait.Until(p.flushBackoffQCompleted, 1.0*time.Second, p.stop)
	go wait.Until(p.flushUnschedulableQLeftover, 30*time.Second, p.stop)
}
```