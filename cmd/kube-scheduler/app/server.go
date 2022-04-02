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

// Package app implements a Server object for running the scheduler.
package app

import (
	"context"
	"fmt"
	"io"
	"k8s.io/klog/v2"
	"net/http"
	"os"
	goruntime "runtime"

	"github.com/spf13/cobra"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapifilters "k8s.io/apiserver/pkg/endpoints/filters"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericfilters "k8s.io/apiserver/pkg/server/filters"
	"k8s.io/apiserver/pkg/server/healthz"
	"k8s.io/apiserver/pkg/server/mux"
	"k8s.io/apiserver/pkg/server/routes"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/tools/leaderelection"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/cli/globalflag"
	"k8s.io/component-base/configz"
	"k8s.io/component-base/logs"
	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/component-base/term"
	"k8s.io/component-base/version"
	"k8s.io/component-base/version/verflag"
	schedulerserverconfig "k8s.io/kubernetes/cmd/kube-scheduler/app/config"
	"k8s.io/kubernetes/cmd/kube-scheduler/app/options"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	"k8s.io/kubernetes/pkg/scheduler"
	kubeschedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	"k8s.io/kubernetes/pkg/scheduler/metrics"
	"k8s.io/kubernetes/pkg/scheduler/profile"
)

// Option configures a framework.Registry.
// 配置一个框架.注册表
type Option func(runtime.Registry) error

// NewSchedulerCommand creates a *cobra.Command object with default parameters and registryOptions
func NewSchedulerCommand(registryOptions ...Option) *cobra.Command {
	// 构造option
	opts, err := options.NewOptions()
	if err != nil {
		klog.Fatalf("unable to initialize command options: %v", err)
	}

	cmd := &cobra.Command{
		Use: "kube-scheduler",
		Long: `The Kubernetes scheduler is a control plane process which assigns
Pods to Nodes. The scheduler determines which Nodes are valid placements for
each Pod in the scheduling queue according to constraints and available
resources. The scheduler then ranks each valid Node and binds the Pod to a
suitable Node. Multiple different schedulers may be used within a cluster;
kube-scheduler is the reference implementation.
See [scheduling](https://kubernetes.io/docs/concepts/scheduling-eviction/)
for more information about scheduling and the kube-scheduler component.`,
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
	// 添加参数
	fs := cmd.Flags()
	// Flags为SchedulerServer添加指定的参数
	namedFlagSets := opts.Flags()
	verflag.AddFlags(namedFlagSets.FlagSet("global"))
	globalflag.AddGlobalFlags(namedFlagSets.FlagSet("global"), cmd.Name())
	for _, f := range namedFlagSets.FlagSets {
		fs.AddFlagSet(f)
	}

	usageFmt := "Usage:\n  %s\n"
	cols, _, _ := term.TerminalSize(cmd.OutOrStdout())
	cmd.SetUsageFunc(func(cmd *cobra.Command) error {
		fmt.Fprintf(cmd.OutOrStderr(), usageFmt, cmd.UseLine())
		cliflag.PrintSections(cmd.OutOrStderr(), namedFlagSets, cols)
		return nil
	})
	cmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n\n"+usageFmt, cmd.Long, cmd.UseLine())
		cliflag.PrintSections(cmd.OutOrStdout(), namedFlagSets, cols)
	})
	cmd.MarkFlagFilename("config", "yaml", "yml", "json")

	return cmd
}

// runCommand runs the scheduler.
// 运行调度程序
func runCommand(cmd *cobra.Command, opts *options.Options, registryOptions ...Option) error {
	// 检测 -version 标识。如果是，则打印版本并退出
	verflag.PrintAndExitIfRequested()
	// 输出打印 传入的 flag 集合
	cliflag.PrintFlags(cmd.Flags())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 基于命令参数和选项创建完整的配置和调度程序
	cc, sched, err := Setup(ctx, opts, registryOptions...)
	if err != nil {
		return err
	}

	// 如果指定保存配置留存路径（WriteConfigTo）
	if len(opts.WriteConfigTo) > 0 {
		if err := options.WriteConfigFile(opts.WriteConfigTo, &cc.ComponentConfig); err != nil {
			return err
		}
		klog.Infof("Wrote configuration to: %s\n", opts.WriteConfigTo)
		return nil
	}

	// 根据配置 cc 创建、运行调度器 sched
	return Run(ctx, cc, sched)
}

// Run executes the scheduler based on the given configuration. It only returns on error or when context is done.
// Run 根据给定的配置执行调度程序。 它仅在错误或上下文完成时返回
/*
Run函数的主要内容如下：
	通过scheduler config来创建scheduler的结构体。
	运行event broadcaster、healthz server、metrics server。
	运行所有的informer并在调度前等待cache的同步（重点）。
	执行sched.Run()来运行scheduler的调度逻辑。
	如果多个scheduler并开启了LeaderElect，则执行leader选举。
*/
func Run(ctx context.Context, cc *schedulerserverconfig.CompletedConfig, sched *scheduler.Scheduler) error {
	// To help debugging, immediately log version
	klog.V(1).Infof("Starting Kubernetes Scheduler version %+v", version.Get())

	// Configz registration.
	// 配置注册
	if cz, err := configz.New("componentconfig"); err == nil {
		cz.Set(cc.ComponentConfig)
	} else {
		return fmt.Errorf("unable to register configz: %s", err)
	}

	// Prepare the event broadcaster.
	// 准备事件广播器
	cc.EventBroadcaster.StartRecordingToSink(ctx.Done())

	// Setup healthz checks.
	// 设置健康检查
	var checks []healthz.HealthChecker
	if cc.ComponentConfig.LeaderElection.LeaderElect {
		checks = append(checks, cc.LeaderElection.WatchDog)
	}

	// Start up the healthz server.
	// 启动 healthz 服务器
	if cc.InsecureServing != nil {
		separateMetrics := cc.InsecureMetricsServing != nil
		handler := buildHandlerChain(newHealthzHandler(&cc.ComponentConfig, separateMetrics, checks...), nil, nil)
		// 启动一个不安全的 http 服务器
		if err := cc.InsecureServing.Serve(handler, 0, ctx.Done()); err != nil {
			return fmt.Errorf("failed to start healthz server: %v", err)
		}
	}
	if cc.InsecureMetricsServing != nil {
		handler := buildHandlerChain(newMetricsHandler(&cc.ComponentConfig), nil, nil)
		// 启动一个不安全的 http 服务器
		if err := cc.InsecureMetricsServing.Serve(handler, 0, ctx.Done()); err != nil {
			return fmt.Errorf("failed to start metrics server: %v", err)
		}
	}
	if cc.SecureServing != nil {
		handler := buildHandlerChain(newHealthzHandler(&cc.ComponentConfig, false, checks...), cc.Authentication.Authenticator, cc.Authorization.Authorizer)
		// TODO: handle stoppedCh returned by c.SecureServing.Serve
		if _, err := cc.SecureServing.Serve(handler, 0, ctx.Done()); err != nil {
			// fail early for secure handlers, removing the old error loop from above
			return fmt.Errorf("failed to start secure server: %v", err)
		}
	}

	// Start all informers.
	// 运行PodInformer，并运行InformerFactory。此部分的逻辑为client-go的informer机制
	go cc.PodInformer.Informer().Run(ctx.Done())
	cc.InformerFactory.Start(ctx.Done())

	// Wait for all caches to sync before scheduling.
	// 在调度之前等待所有缓存同步
	cc.InformerFactory.WaitForCacheSync(ctx.Done())

	// If leader election is enabled, runCommand via LeaderElector until done and exit.
	// 如果有多个scheduler，并开启leader选举，则运行LeaderElector直到选举结束或退出
	if cc.LeaderElection != nil {
		cc.LeaderElection.Callbacks = leaderelection.LeaderCallbacks{
			OnStartedLeading: sched.Run,
			OnStoppedLeading: func() {
				klog.Fatalf("leaderelection lost")
			},
		}
		// 根据配置 创建 leader 候选人
		leaderElector, err := leaderelection.NewLeaderElector(*cc.LeaderElection)
		if err != nil {
			return fmt.Errorf("couldn't create leader elector: %v", err)
		}
		// 开始 leader 选举
		leaderElector.Run(ctx)

		return fmt.Errorf("lost lease")
	}

	// Leader election is disabled, so runCommand inline until done.
	// 领导者选举被禁用，所以 runCommand 内联直到完成
	sched.Run(ctx)
	return fmt.Errorf("finished without leader elect")
}

// buildHandlerChain wraps the given handler with the standard filters.
// 用标准过滤器包装给定的处理程序
func buildHandlerChain(handler http.Handler, authn authenticator.Request, authz authorizer.Authorizer) http.Handler {
	requestInfoResolver := &apirequest.RequestInfoFactory{}
	failedHandler := genericapifilters.Unauthorized(legacyscheme.Codecs)

	handler = genericapifilters.WithAuthorization(handler, authz, legacyscheme.Codecs)
	handler = genericapifilters.WithAuthentication(handler, authn, failedHandler, nil)
	handler = genericapifilters.WithRequestInfo(handler, requestInfoResolver)
	handler = genericapifilters.WithCacheControl(handler)
	handler = genericfilters.WithPanicRecovery(handler)

	return handler
}

func installMetricHandler(pathRecorderMux *mux.PathRecorderMux) {
	configz.InstallHandler(pathRecorderMux)
	//lint:ignore SA1019 See the Metrics Stability Migration KEP
	defaultMetricsHandler := legacyregistry.Handler().ServeHTTP
	pathRecorderMux.HandleFunc("/metrics", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "DELETE" {
			metrics.Reset()
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			io.WriteString(w, "metrics reset\n")
			return
		}
		defaultMetricsHandler(w, req)
	})
}

// newMetricsHandler builds a metrics server from the config.
// 从配置构建一个指标服务器
func newMetricsHandler(config *kubeschedulerconfig.KubeSchedulerConfiguration) http.Handler {
	pathRecorderMux := mux.NewPathRecorderMux("kube-scheduler")
	installMetricHandler(pathRecorderMux)
	if config.EnableProfiling {
		routes.Profiling{}.Install(pathRecorderMux)
		if config.EnableContentionProfiling {
			goruntime.SetBlockProfileRate(1)
		}
		routes.DebugFlags{}.Install(pathRecorderMux, "v", routes.StringFlagPutHandler(logs.GlogSetter))
	}
	return pathRecorderMux
}

// newHealthzHandler creates a healthz server from the config, and will also
// embed the metrics handler if the healthz and metrics address configurations
// are the same.
// 从配置中创建healthz服务器，如果healthz和metrics地址配置相同，还将嵌入指标处理程序
func newHealthzHandler(config *kubeschedulerconfig.KubeSchedulerConfiguration, separateMetrics bool, checks ...healthz.HealthChecker) http.Handler {
	pathRecorderMux := mux.NewPathRecorderMux("kube-scheduler")
	// 注册用于健康检查的处理程序
	healthz.InstallHandler(pathRecorderMux, checks...)
	if !separateMetrics {
		installMetricHandler(pathRecorderMux)
	}
	if config.EnableProfiling {
		routes.Profiling{}.Install(pathRecorderMux)
		if config.EnableContentionProfiling {
			goruntime.SetBlockProfileRate(1)
		}
		routes.DebugFlags{}.Install(pathRecorderMux, "v", routes.StringFlagPutHandler(logs.GlogSetter))
	}
	return pathRecorderMux
}

// 获得 Factory 记录器
func getRecorderFactory(cc *schedulerserverconfig.CompletedConfig) profile.RecorderFactory {
	return func(name string) events.EventRecorder {
		return cc.EventBroadcaster.NewRecorder(name)
	}
}

// WithPlugin creates an Option based on plugin name and factory. Please don't remove this function: it is used to register out-of-tree plugins,
// hence there are no references to it from the kubernetes scheduler code base.
// WithPlugin 根据 plugin 的名称和 factory 创建一个 Option。请不要删除这个函数：它被用来注册列表外的插件，因此在kubernetes调度器的代码库中没有对它的引用。
func WithPlugin(name string, factory runtime.PluginFactory) Option {
	return func(registry runtime.Registry) error {
		return registry.Register(name, factory)
	}
}

// Setup creates a completed config and a scheduler based on the command args and options
// 基于命令参数和选项创建完整的配置和调度程序
func Setup(ctx context.Context, opts *options.Options, outOfTreeRegistryOptions ...Option) (*schedulerserverconfig.CompletedConfig, *scheduler.Scheduler, error) {
	// 验证所有必需的选项（安全、认证、授权、日志、指标等）
	if errs := opts.Validate(); len(errs) > 0 {
		return nil, nil, utilerrors.NewAggregate(errs)
	}

	// Config初始化调度器的配置对象
	c, err := opts.Config()
	if err != nil {
		return nil, nil, err
	}

	// Get the completed config
	// 填写所有未设置的字段, 获得完整配置
	cc := c.Complete()

	// 外部注册表, 用于扩展加载
	outOfTreeRegistry := make(runtime.Registry)
	for _, option := range outOfTreeRegistryOptions {
		if err := option(outOfTreeRegistry); err != nil {
			return nil, nil, err
		}
	}

	// 获得 Factory 记录器, 将要被废弃
	recorderFactory := getRecorderFactory(&cc)
	// Create the scheduler.
	// 创建调度器
	sched, err := scheduler.New(cc.Client,
		cc.InformerFactory,
		cc.PodInformer,
		recorderFactory,
		ctx.Done(),
		scheduler.WithProfiles(cc.ComponentConfig.Profiles...),                              // 为调度器设置配置文件
		scheduler.WithAlgorithmSource(cc.ComponentConfig.AlgorithmSource),                   // 设置Scheduler的schedulerAlgorithmSource
		scheduler.WithPercentageOfNodesToScore(cc.ComponentConfig.PercentageOfNodesToScore), // 设置节点得分百分比
		scheduler.WithFrameworkOutOfTreeRegistry(outOfTreeRegistry),                         // 为列表外的插件设置注册表。这些插件 将被追加到默认注册表中。
		scheduler.WithPodMaxBackoffSeconds(cc.ComponentConfig.PodMaxBackoffSeconds),         // 设置 podMaxBackoffSeconds，默认值为10
		scheduler.WithPodInitialBackoffSeconds(cc.ComponentConfig.PodInitialBackoffSeconds), // 设置 podInitialBackoffSeconds，默认值为1
		scheduler.WithExtenders(cc.ComponentConfig.Extenders...),                            // 为Scheduler设置扩展程序
	)
	if err != nil {
		return nil, nil, err
	}

	return &cc, sched, nil
}
