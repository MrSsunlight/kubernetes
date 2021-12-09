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

package config

import (
	apiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/informers"
	coreinformers "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/tools/leaderelection"
	kubeschedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
)

// Config has all the context to run a Scheduler
// 配置具有运行一个调度程序的所有环境
type Config struct {
	// ComponentConfig is the scheduler server's configuration object.
	// ComponentConfig是调度器服务器的配置对象
	ComponentConfig kubeschedulerconfig.KubeSchedulerConfiguration

	// LoopbackClientConfig is a config for a privileged loopback connection
	// LoopbackClientConfig 是一个特权loopback连接的配置
	LoopbackClientConfig *restclient.Config

	// 不安全的服务
	InsecureServing        *apiserver.DeprecatedInsecureServingInfo // nil will disable serving on an insecure port
	// 不安全的指标服务
	InsecureMetricsServing *apiserver.DeprecatedInsecureServingInfo // non-nil if metrics should be served independently
	Authentication         apiserver.AuthenticationInfo // 验证
	Authorization          apiserver.AuthorizationInfo  // 授权
	SecureServing          *apiserver.SecureServingInfo // 安全服务

	Client          clientset.Interface
	InformerFactory informers.SharedInformerFactory
	PodInformer     coreinformers.PodInformer

	//lint:ignore SA1019 this deprecated field still needs to be used for now. It will be removed once the migration is done.
	EventBroadcaster events.EventBroadcasterAdapter

	// LeaderElection is optional.
	LeaderElection *leaderelection.LeaderElectionConfig
}

// 已完成的配置
type completedConfig struct {
	*Config
}

// CompletedConfig same as Config, just to swap private object.
// CompletedConfig 与Config相同，只是交换了私有对象
type CompletedConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	// 嵌入一个不能在此包之外实例化的私有指针
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
// Complete 填写所有未设置的字段，这些字段需要具有有效数据。 它正在改变接收器
func (c *Config) Complete() CompletedConfig {
	cc := completedConfig{c}

	if c.InsecureServing != nil {
		c.InsecureServing.Name = "healthz"
	}
	if c.InsecureMetricsServing != nil {
		c.InsecureMetricsServing.Name = "metrics"
	}
	// 如果指定了Loopback客户端配置并且它具有承载令牌，AuthorizeClientBearerToken 会将身份验证器和授权器包装在Loopback身份验证逻辑中。
	// 请注意，如果 authn 或 authz 为 nil，则此函数不会添加令牌验证器或授权器
	apiserver.AuthorizeClientBearerToken(c.LoopbackClientConfig, &c.Authentication, &c.Authorization)

	return CompletedConfig{&cc}
}
