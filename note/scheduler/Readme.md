# scheduler 源码梳理

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
	...
	// 在调度之前等待所有缓存同步
	...

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

#### 1.5 `pkg/scheduler/scheduler.go` -> (sched *Scheduler) Run(ctx context.Context) 

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

#### 2.1 