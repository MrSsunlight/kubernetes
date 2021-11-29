# 源码学习笔记

## 代码分支：
    release-1.19

## 环境
- go version
```shell
  go version go1.17.3 linux/amd64
```
- kubectl version
```shell
  Client Version: version.Info{Major:"1", Minor:"22", GitVersion:"v1.22.4", GitCommit:"b695d79d4f967c403a96986f1750a35eb75e75f1", GitTreeState:"clean", BuildDate:"2021-11-17T15:48:33Z", GoVersion:"go1.16.10", Compiler:"gc", Platform:"linux/amd64"}
```
- uname -a
```shell
  Linux localhost.localhost 3.10.0-1160.45.1.el7.x86_64 #1 SMP Wed Oct 13 17:20:51 UTC 2021 x86_64 x86_64 x86_64 GNU/Linux
```
- docker version
```shell
  Client: Docker Engine - Community
  Version:           20.10.11
  API version:       1.41
  Go version:        go1.16.9
  Git commit:        dea9396
  Built:             Thu Nov 18 00:38:53 2021
  OS/Arch:           linux/amd64
  Context:           default
  Experimental:      true
```
- gcc --version
```shell
  gcc (GCC) 4.8.5 20150623 (Red Hat 4.8.5-44)
```

## 目录结构：

| cmd     | 每个组件代码入口（main函数） |
| ------- | ---------------------------- |
| pkg     | 各个组件的具体功能实现       |
| staging | 已经分库的项目               |
| vendor  | 依赖                         |

## 模块划分：
- Kubernetes 主要由以下几个核心组件组成:
    - etcd 保存了整个集群的状态，就是一个数据库；
    - apiserver 提供了资源操作的唯一入口，并提供认证、授权、访问控制、API 注册和发现等机制；
    - controller manager 负责维护集群的状态，比如故障检测、自动扩展、滚动更新等；
    - scheduler 负责资源的调度，按照预定的调度策略将 Pod 调度到相应的机器上；
    - kubelet 负责维护容器的生命周期，同时也负责 Volume（CSI）和网络（CNI）的管理；
    - Container runtime 负责镜像管理以及 Pod 和容器的真正运行（CRI）；
    - kube-proxy 负责为 Service 提供 cluster 内部的服务发现和负载均衡；
    
- 除了上面的这些核心组件，还有一些推荐的插件：
    - kube-dns 负责为整个集群提供 DNS 服务
    - Ingress Controller 为服务提供外网入口
    - Heapster 提供资源监控
    - Dashboard 提供 GUI

## 编译
### 安装依赖：
`docker、git、make`
`brew install gnu-tar`
`brew install coreutils`
### 下载源码：
- `cd $GOPATH/src/`
- `mkdir k8s.io && cd k8s.io`
- `git clone -v https://github.com/kubernetes/kubernetes`
- `cd kubernetes`
- `git checkout -b release-1.19 origin/release-1.19`

### 解决源码依赖：
- `go mod init`
- `go mod vendor`
> 一般版本直接进入下一步进行编译即可
### 源码编译：
- `make`、`make clean`
- 编译全部组件：`KUBE_BUILD_PLATFORMS=linux/amd64 make all GOFLAGS=-v GOGCFLAGS="-N -l"`
- 编译指定组件：`cd cmd/kube-apiserver && go build -v` 或 `KUBE_BUILD_PLATFORMS=linux/amd64 make WHAT=cmd/kube-apiserver GOFLAGS=-v GOGCFLAGS="-N -l"`
```shell
- KUBE_BUILD_PLATFORMS=linux/amd64 指定当前编译平台环境类型为 linux/amd64, mac 为 darwin/amd64
- make all 表示在本地环境中编译所有组件。
- GOFLAGS=-v 编译参数，开启 verbose 日志。
- GOGCFLAGS="-N -l" 编译参数，禁止编译优化和内联，减小可执行程序大小。 
```


### 异常处理：
1. 编译指定模块提示(`cd cmd/kube-apiserver && go build -v`)：
```shell
[root@localhost kube-apiserver]# go build .
go: updates to go.mod needed, disabled by -mod=vendor
	(Go version in go.mod is at least 1.14 and vendor directory exists.)
	to update it:
	go mod tidy
go: updates to go.mod needed, disabled by -mod=vendor
	(Go version in go.mod is at least 1.14 and vendor directory exists.)
	to update it:
	go mod tidy
```
解决办法：
```shell
go get -u
go mod tidy
go mod vendor
```
2. `make all` 或 `KUBE_BUILD_PLATFORMS=linux/amd64 make all GOFLAGS=-v GOGCFLAGS="-N -l"`提示异常：
```shell
...
	To ignore the vendor directory, use -mod=readonly or -mod=mod.
	To sync the vendor directory, run:
		go mod vendor
!!! [1028 15:40:37] Call tree:
!!! [1028 15:40:37]  1: /home/go_worker/src/k8s.io/kubernetes/hack/lib/golang.sh:616 kube::golang::build_some_binaries(...)
!!! [1028 15:40:37]  2: /home/go_worker/src/k8s.io/kubernetes/hack/lib/golang.sh:752 kube::golang::build_binaries_for_platform(...)
!!! [1028 15:40:37]  3: hack/make-rules/build.sh:27 kube::golang::build_binaries(...)
!!! [1028 15:40:37] Call tree:
!!! [1028 15:40:37]  1: hack/make-rules/build.sh:27 kube::golang::build_binaries(...)
!!! [1028 15:40:37] Call tree:
!!! [1028 15:40:37]  1: hack/make-rules/build.sh:27 kube::golang::build_binaries(...)
make[1]: *** [_output/bin/deepcopy-gen] 错误 1
make: *** [generated_files] 错误 2
```
解决办法：
此问题出现在1.13版本，可能是因为go mod得问题，建议使用Godeps

### 编译结果：
``` shell
[root@localhost kubernetes]# ll _output/
总用量 80
-rw-r--r-- 1 root root  3378 10月 28 16:00 AGGREGATOR_violations.report
-rw-r--r-- 1 root root  3378 10月 28 16:00 APIEXTENSIONS_violations.report
lrwxrwxrwx 1 root root    67 10月 28 16:09 bin -> /home/go_worker/src/k8s.io/kubernetes/_output/local/bin/linux/amd64
-rw-r--r-- 1 root root  3713 10月 28 16:00 CODEGEN_violations.report
-rw-r--r-- 1 root root 64197 10月 28 16:00 KUBE_violations.report
drwxr-xr-x 4 root root    27 10月 28 15:59 local
-rw-r--r-- 1 root root  3492 10月 28 16:00 SAMPLEAPISERVER_violations.report
```

### 清除：
`make clean`

### 运行:
```shell
  make clean && KUBE_BUILD_PLATFORMS=linux/amd64 make all GOFLAGS=-v GOGCFLAGS="-N -l"
```

## 模块学习

### apiserver

#### 主要功能：

#### 代码入口：
- `cmd/kube-apiserver/apiserver.go -> main()`: main() 函数入口位置,
- `cmd/kube-apiserver/app/server.go -> NewAPIServerCommand()`
-

#### 业务逻辑：

##### 延申：

#### 使用：


### kube-proxy

#### 主要功能：

#### 代码入口：

#### 业务逻辑：

##### 延申：

#### 使用：



### scheduler(调度器)

#### 主要功能：
​	Scheduler是一个跑在其他组件边上的独立程序，对接Apiserver寻找PodSpec.NodeName为空的Pod，然后用post的方式发送一个api调用，指定这些pod应该跑在哪个node上。

​	通俗地说，就是scheduler是相对独立的一个组件，主动访问api server，寻找等待调度的pod，然后通过一系列调度算法寻找哪个node适合跑这个pod，然后将这个pod和node的绑定关系发给api server，从而完成了调度的过程。
#### 代码入口：
- `cmd/kube-scheduler/scheduler.go`: main() 函数入口位置，在scheduler过程开始被调用前的一系列初始化工作。
- `pkg/scheduler/scheduler.go`: 调度框架的整体逻辑，在具体的调度算法之上的框架性的代码。
- `pkg/scheduler/core/generic_scheduler.go`: 具体的计算哪些node适合跑哪些pod的算法。

#### 业务逻辑：
```shell
对于一个给定的pod
+---------------------------------------------+
|             可用于调度的nodes如下：           |
|  +--------+     +--------+     +--------+   |
|  | node 1 |     | node 2 |     | node 3 |   |
|  +--------+     +--------+     +--------+   |
+----------------------+----------------------+
                       |
                       v
+----------------------+----------------------+
初步过滤: node 3 资源不足
+----------------------+----------------------+
                       |
                       v
+----------------------+----------------------+
|                 剩下的nodes:                 |
|     +--------+               +--------+     |
|     | node 1 |               | node 2 |     |
|     +--------+               +--------+     |
+----------------------+----------------------+
                       |
                       v
+----------------------+----------------------+
优先级算法计算结果:    node 1: 分数=2
                    node 2: 分数=5
+----------------------+----------------------+
                       |
                       v
            选择分值最高的节点 = node 2
```

Scheduler为每个pod寻找一个适合其运行的node，大体分成三步：

1. 通过一系列的“predicates”过滤掉不能运行pod的node，比如一个pod需要500M的内存，有些节点剩余内存只有100M了，就会被剔除；
2. 通过一系列的“priority functions”给剩下的node排一个等级，分出三六九等，寻找能够运行pod的若干node中最合适的一个node；
3. 得分最高的一个node，也就是被“priority functions”选中的node胜出了，获得了跑对应pod的资格。

##### 延申：

- Predicates是一些用于过滤不合适node的策略。

- Priorities是一些用于区分node排名（分数）的策略（作用在通过predicates过滤的node上）。

  Predicates 和 priorities 的代码分别在：

  - pkg/scheduler/algorithm/predicates/predicates.go
  - pkg/scheduler/algorithm/priorities.



#### 使用：

​	默认调度策略是通过`defaultPredicates()` 和 `defaultPriorities()函数`定义的，源码在 `pkg/scheduler/algorithmprovider/defaults/defaults.go`，可以通过命令行flag `--policy-config-file`来覆盖默认行为。所以可以通过配置文件的方式或者修改`pkg/scheduler/algorithm/predicates/predicates.go` /`pkg/scheduler/algorithm/priorities`，然后注册到`defaultPredicates()`/`defaultPriorities()`来实现。配置文件类似下面这个样子：

```yaml
{
"kind" : "Policy",
"apiVersion" : "v1",
"predicates" : [
    {"name" : "PodFitsHostPorts"},
    {"name" : "PodFitsResources"},
    {"name" : "NoDiskConflict"},
    {"name" : "NoVolumeZoneConflict"},
    {"name" : "MatchNodeSelector"},
    {"name" : "HostName"}
    ],
"priorities" : [
    {"name" : "LeastRequestedPriority", "weight" : 1},
    {"name" : "BalancedResourceAllocation", "weight" : 1},
    {"name" : "ServiceSpreadingPriority", "weight" : 1},
    {"name" : "EqualPriority", "weight" : 1}
    ],
"hardPodAffinitySymmetricWeight" : 10,
"alwaysCheckAllPredicates" : false
}
```

### controller(控制器)

#### 主要功能：

#### 代码入口：

#### 业务逻辑：

##### 延申：

#### 使用：


### kubelet

#### 主要功能：

#### 代码入口：

#### 业务逻辑：

##### 延申：

#### 使用：



### client-go

#### 主要功能：

#### 代码入口：

#### 业务逻辑：

##### 延申：

#### 使用：