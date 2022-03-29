/*
Copyright 2018 The Kubernetes Authors.

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

package options

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	apiserveroptions "k8s.io/apiserver/pkg/server/options"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
	cliflag "k8s.io/component-base/cli/flag"
	componentbaseconfig "k8s.io/component-base/config"
	"k8s.io/component-base/config/options"
	configv1alpha1 "k8s.io/component-base/config/v1alpha1"
	"k8s.io/component-base/logs"
	"k8s.io/component-base/metrics"
	"k8s.io/klog/v2"
	kubeschedulerconfigv1beta1 "k8s.io/kube-scheduler/config/v1beta1"
	schedulerappconfig "k8s.io/kubernetes/cmd/kube-scheduler/app/config"
	"k8s.io/kubernetes/pkg/scheduler"
	kubeschedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	kubeschedulerscheme "k8s.io/kubernetes/pkg/scheduler/apis/config/scheme"
	"k8s.io/kubernetes/pkg/scheduler/apis/config/validation"
)

// Options has all the params needed to run a Scheduler
// Options 拥有运行调度器所需的所有参数
type Options struct {
	// The default values. These are overridden if ConfigFile is set or by values in InsecureServing.
	// 默认值。如果设置了ConfigFile或InsecureServing中的值，这些值会被覆盖
	ComponentConfig kubeschedulerconfig.KubeSchedulerConfiguration

	SecureServing           *apiserveroptions.SecureServingOptionsWithLoopback
	CombinedInsecureServing *CombinedInsecureServingOptions
	Authentication          *apiserveroptions.DelegatingAuthenticationOptions
	Authorization           *apiserveroptions.DelegatingAuthorizationOptions
	Metrics                 *metrics.Options
	Logs                    *logs.Options
	Deprecated              *DeprecatedOptions

	// ConfigFile is the location of the scheduler server's configuration file.
	// ConfigFile 是调度程序服务器的配置文件的位置
	ConfigFile string

	// WriteConfigTo is the path where the default configuration will be written.
	// WriteConfigTo 是将写入默认配置的路径
	WriteConfigTo string

	Master string
}

// NewOptions returns default scheduler app options.
// NewOptions主要用来构造SchedulerServer使用的参数和上下文，其中核心参数是KubeSchedulerConfiguration
func NewOptions() (*Options, error) {
	cfg, err := newDefaultComponentConfig()
	if err != nil {
		return nil, err
	}

	hhost, hport, err := splitHostIntPort(cfg.HealthzBindAddress)
	if err != nil {
		return nil, err
	}

	o := &Options{
		ComponentConfig: *cfg,
		SecureServing:   apiserveroptions.NewSecureServingOptions().WithLoopback(),
		CombinedInsecureServing: &CombinedInsecureServingOptions{
			Healthz: (&apiserveroptions.DeprecatedInsecureServingOptions{
				BindNetwork: "tcp",
			}).WithLoopback(),
			Metrics: (&apiserveroptions.DeprecatedInsecureServingOptions{
				BindNetwork: "tcp",
			}).WithLoopback(),
			BindPort:    hport,
			BindAddress: hhost,
		},
		Authentication: apiserveroptions.NewDelegatingAuthenticationOptions(),
		Authorization:  apiserveroptions.NewDelegatingAuthorizationOptions(),
		Deprecated: &DeprecatedOptions{
			UseLegacyPolicyConfig:          false,
			PolicyConfigMapNamespace:       metav1.NamespaceSystem,
			SchedulerName:                  corev1.DefaultSchedulerName,
			HardPodAffinitySymmetricWeight: 1,
		},
		Metrics: metrics.NewOptions(),
		Logs:    logs.NewOptions(),
	}

	o.Authentication.TolerateInClusterLookupFailure = true
	o.Authentication.RemoteKubeConfigFileOptional = true
	o.Authorization.RemoteKubeConfigFileOptional = true
	o.Authorization.AlwaysAllowPaths = []string{"/healthz"}

	// Set the PairName but leave certificate directory blank to generate in-memory by default
	o.SecureServing.ServerCert.CertDirectory = ""
	o.SecureServing.ServerCert.PairName = "kube-scheduler"
	o.SecureServing.BindPort = kubeschedulerconfig.DefaultKubeSchedulerPort

	return o, nil
}

func splitHostIntPort(s string) (string, int, error) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, err
	}
	portInt, err := strconv.Atoi(port)
	if err != nil {
		return "", 0, err
	}
	return host, portInt, err
}

func newDefaultComponentConfig() (*kubeschedulerconfig.KubeSchedulerConfiguration, error) {
	versionedCfg := kubeschedulerconfigv1beta1.KubeSchedulerConfiguration{}
	versionedCfg.DebuggingConfiguration = *configv1alpha1.NewRecommendedDebuggingConfiguration()

	kubeschedulerscheme.Scheme.Default(&versionedCfg)
	cfg := kubeschedulerconfig.KubeSchedulerConfiguration{}
	if err := kubeschedulerscheme.Scheme.Convert(&versionedCfg, &cfg, nil); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Flags returns flags for a specific scheduler by section name
// Flags根据节名返回特定调度程序的标志
func (o *Options) Flags() (nfs cliflag.NamedFlagSets) {
	// 混杂模式
	fs := nfs.FlagSet("misc")
	// 下面参数可以被配置文件中的覆盖
	fs.StringVar(&o.ConfigFile, "config", o.ConfigFile, `The path to the configuration file. The following flags can overwrite fields in this file:
  --algorithm-provider
  --policy-config-file
  --policy-configmap
  --policy-configmap-namespace`)
	// 如果设置了，将配置值写入该文件并退出
	fs.StringVar(&o.WriteConfigTo, "write-config-to", o.WriteConfigTo, "If set, write the configuration values to this file and exit.")
	// Kubernetes API服务器的地址(覆盖kubeconfig中的任何值）
	fs.StringVar(&o.Master, "master", o.Master, "The address of the Kubernetes API server (overrides any value in kubeconfig)")

	o.SecureServing.AddFlags(nfs.FlagSet("secure serving"))
	o.CombinedInsecureServing.AddFlags(nfs.FlagSet("insecure serving"))
	o.Authentication.AddFlags(nfs.FlagSet("authentication"))
	o.Authorization.AddFlags(nfs.FlagSet("authorization"))
	// 弃用部分
	o.Deprecated.AddFlags(nfs.FlagSet("deprecated"), &o.ComponentConfig)

	// 集群leader选举
	options.BindLeaderElectionFlags(&o.ComponentConfig.LeaderElection, nfs.FlagSet("leader election"))
	utilfeature.DefaultMutableFeatureGate.AddFlag(nfs.FlagSet("feature gate"))
	o.Metrics.AddFlags(nfs.FlagSet("metrics"))
	o.Logs.AddFlags(nfs.FlagSet("logs"))

	return nfs
}

// ApplyTo applies the scheduler options to the given scheduler app configuration.
// ApplyTo 将调度器选项应用到给定的调度器应用配置中
func (o *Options) ApplyTo(c *schedulerappconfig.Config) error {
	if len(o.ConfigFile) == 0 {
		c.ComponentConfig = o.ComponentConfig

		// apply deprecated flags if no config file is loaded (this is the old behaviour).
		// 如果没有加载配置文件，应用弃用标志(这是旧的行为)
		o.Deprecated.ApplyTo(&c.ComponentConfig)
		if err := o.CombinedInsecureServing.ApplyTo(c, &c.ComponentConfig); err != nil {
			return err
		}
	} else {
		cfg, err := loadConfigFromFile(o.ConfigFile)
		if err != nil {
			return err
		}
		if err := validation.ValidateKubeSchedulerConfiguration(cfg).ToAggregate(); err != nil {
			return err
		}

		c.ComponentConfig = *cfg

		// apply any deprecated Policy flags, if applicable
		// 如果适用，应用任何已弃用的策略标志
		o.Deprecated.ApplyAlgorithmSourceTo(&c.ComponentConfig)

		// if the user has set CC profiles and is trying to use a Policy config, error out
		// these configs are no longer merged and they should not be used simultaneously
		// 如果用户已设置 CC 配置文件并尝试使用策略配置，则会出现这些配置不再合并且不应同时使用的错误
		if !emptySchedulerProfileConfig(c.ComponentConfig.Profiles) && c.ComponentConfig.AlgorithmSource.Policy != nil {
			return fmt.Errorf("cannot set a Plugin config and Policy config")
		}

		// use the loaded config file only, with the exception of --address and --port.
		// 仅使用加载的配置文件，但 --address 和 --port 除外
		if err := o.CombinedInsecureServing.ApplyToFromLoadedConfig(c, &c.ComponentConfig); err != nil {
			return err
		}
	}

	if err := o.SecureServing.ApplyTo(&c.SecureServing, &c.LoopbackClientConfig); err != nil {
		return err
	}
	if o.SecureServing != nil && (o.SecureServing.BindPort != 0 || o.SecureServing.Listener != nil) {
		if err := o.Authentication.ApplyTo(&c.Authentication, c.SecureServing, nil); err != nil {
			return err
		}
		if err := o.Authorization.ApplyTo(&c.Authorization); err != nil {
			return err
		}
	}
	o.Metrics.Apply()
	o.Logs.Apply()
	return nil
}

// emptySchedulerProfileConfig returns true if the list of profiles passed to it contains only
// the "default-scheduler" profile with no plugins or pluginconfigs registered
// (this is the default empty profile initialized by defaults.go)
func emptySchedulerProfileConfig(profiles []kubeschedulerconfig.KubeSchedulerProfile) bool {
	return len(profiles) == 1 &&
		len(profiles[0].PluginConfig) == 0 &&
		profiles[0].Plugins == nil
}

// Validate validates all the required options.
// Validate验证所有必需的选项
func (o *Options) Validate() []error {
	var errs []error

	if err := validation.ValidateKubeSchedulerConfiguration(&o.ComponentConfig).ToAggregate(); err != nil {
		errs = append(errs, err.Errors()...)
	}
	errs = append(errs, o.SecureServing.Validate()...)           // 安全服务
	errs = append(errs, o.CombinedInsecureServing.Validate()...) // 合并不安全的服务
	errs = append(errs, o.Authentication.Validate()...)          // 认证
	errs = append(errs, o.Authorization.Validate()...)           // 授权
	errs = append(errs, o.Deprecated.Validate()...)              // 已弃用
	errs = append(errs, o.Metrics.Validate()...)                 // 指标
	errs = append(errs, o.Logs.Validate()...)                    // 日志

	return errs
}

// Config return a scheduler config object
// 返回调度程序配置对象
/*
Config函数主要执行以下操作：
	构建scheduler client、leaderElectionClient、eventClient。
	创建event 记录器(recorder)
	设置leader选举
	创建informer对象，主要函数有NewSharedInformerFactory和NewPodInformer
*/
func (o *Options) Config() (*schedulerappconfig.Config, error) {
	// 安全检测 不安全就看是否使用默认证书
	if o.SecureServing != nil {
		// 检测是否使用使用默认自签名的证书
		if err := o.SecureServing.MaybeDefaultWithSelfSignedCerts("localhost", nil, []net.IP{net.ParseIP("127.0.0.1")}); err != nil {
			return nil, fmt.Errorf("error creating self-signed certificates: %v", err)
		}
	}

	// 将调度器选项应用到给定的调度器应用配置中
	c := &schedulerappconfig.Config{}
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}

	// Prepare kube clients.
	// 准备 kube 客户端
	client, leaderElectionClient, eventClient, err := createClients(c.ComponentConfig.ClientConnection, o.Master, c.ComponentConfig.LeaderElection.RenewDeadline.Duration)
	if err != nil {
		return nil, err
	}

	// 创建 event 记录器
	c.EventBroadcaster = events.NewEventBroadcasterAdapter(eventClient)

	// Set up leader election if enabled.
	// 如果启用的话，设置 leader 选举
	var leaderElectionConfig *leaderelection.LeaderElectionConfig
	if c.ComponentConfig.LeaderElection.LeaderElect {
		// Use the scheduler name in the first profile to record leader election.
		// 使用第一个配置文件中的调度器名称来记录领导人选举
		coreRecorder := c.EventBroadcaster.DeprecatedNewLegacyRecorder(c.ComponentConfig.Profiles[0].SchedulerName)
		// 构建一个leader选举配置
		leaderElectionConfig, err = makeLeaderElectionConfig(c.ComponentConfig.LeaderElection, leaderElectionClient, coreRecorder)
		if err != nil {
			return nil, err
		}
	}

	c.Client = client
	// 创建 informer 对象
	c.InformerFactory = informers.NewSharedInformerFactory(client, 0)
	// 创建一个共享索引通知器，只返回非终端pods
	c.PodInformer = scheduler.NewPodInformer(client, 0)
	c.LeaderElection = leaderElectionConfig

	return c, nil
}

// makeLeaderElectionConfig builds a leader election configuration. It will
// create a new resource lock associated with the configuration.
// makeLeaderElectionConfig 构建一个leader选举配置。它将创建一个与配置关联的新资源锁
func makeLeaderElectionConfig(config componentbaseconfig.LeaderElectionConfiguration, client clientset.Interface, recorder record.EventRecorder) (*leaderelection.LeaderElectionConfig, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("unable to get hostname: %v", err)
	}
	// add a uniquifier so that two processes on the same host don't accidentally both become active
	// 添加一个唯一限定符，这样同一主机上的两个进程就不会意外地同时变成活动的
	id := hostname + "_" + string(uuid.NewUUID())

	rl, err := resourcelock.New(config.ResourceLock,
		config.ResourceNamespace,
		config.ResourceName,
		client.CoreV1(),
		client.CoordinationV1(),
		resourcelock.ResourceLockConfig{
			Identity:      id,
			EventRecorder: recorder,
		})
	if err != nil {
		return nil, fmt.Errorf("couldn't create resource lock: %v", err)
	}

	return &leaderelection.LeaderElectionConfig{
		Lock:          rl,
		LeaseDuration: config.LeaseDuration.Duration,
		RenewDeadline: config.RenewDeadline.Duration,
		RetryPeriod:   config.RetryPeriod.Duration,
		WatchDog:      leaderelection.NewLeaderHealthzAdaptor(time.Second * 20),
		Name:          "kube-scheduler",
	}, nil
}

// createClients creates a kube client and an event client from the given config and masterOverride.
// createClients 从给定的配置和 masterOverride 创建一个 kube 客户端和一个 event 客户端
// TODO remove masterOverride when CLI flags are removed.
func createClients(config componentbaseconfig.ClientConnectionConfiguration, masterOverride string, timeout time.Duration) (clientset.Interface, clientset.Interface, clientset.Interface, error) {
	if len(config.Kubeconfig) == 0 && len(masterOverride) == 0 {
		klog.Warningf("Neither --kubeconfig nor --master was specified. Using default API client. This might not work.")
	}

	// This creates a client, first loading any specified kubeconfig
	// file, and then overriding the Master flag, if non-empty.
	kubeConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: config.Kubeconfig},
		&clientcmd.ConfigOverrides{ClusterInfo: clientcmdapi.Cluster{Server: masterOverride}}).ClientConfig()
	if err != nil {
		return nil, nil, nil, err
	}

	kubeConfig.DisableCompression = true
	kubeConfig.AcceptContentTypes = config.AcceptContentTypes
	kubeConfig.ContentType = config.ContentType
	kubeConfig.QPS = config.QPS
	//TODO make config struct use int instead of int32?
	kubeConfig.Burst = int(config.Burst)

	client, err := clientset.NewForConfig(restclient.AddUserAgent(kubeConfig, "scheduler"))
	if err != nil {
		return nil, nil, nil, err
	}

	// shallow copy, do not modify the kubeConfig.Timeout.
	restConfig := *kubeConfig
	restConfig.Timeout = timeout
	leaderElectionClient, err := clientset.NewForConfig(restclient.AddUserAgent(&restConfig, "leader-election"))
	if err != nil {
		return nil, nil, nil, err
	}

	eventClient, err := clientset.NewForConfig(kubeConfig)
	if err != nil {
		return nil, nil, nil, err
	}

	return client, leaderElectionClient, eventClient, nil
}
