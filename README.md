# zey

一个用 Go 写的轻量 Kubernetes 命令行工具,用来管理工作负载的环境变量和导入导出资源。
也是一个学习 Go 实战的示例项目(cobra + 官方 client-go)。

## 构建

```bash
cd zey
go mod tidy      # 下载依赖(client-go、cobra 等)
go build -o zey  # 编译出二进制
sudo ./zey install   # 可选:装到 /usr/bin,之后任意目录直接敲 zey
```

## 功能

| 命令 | 作用 |
|------|------|
| `zey config set/view` | 配置 / 查看 k8s 连接信息(kubeconfig、context、namespace) |
| `zey env get <type/name>` | 查看工作负载各容器的环境变量 |
| `zey env set <type/name> K=V K2-` | 立即修改环境变量(`K=V` 设置,`K-` 删除) |
| `zey env schedule <type/name> K=V --at/--after/--every` | 定时修改环境变量 |
| `zey export [-o file] [-A]` | 导出 Service / Deployment / ConfigMap(默认存档到 `~/.zey/exports/`) |
| `zey apply [-f file]` | 应用存档回集群(默认取 `~/.zey/exports/` 最新一份) |
| `zey nginxInstall` | 从源码编译安装 nginx(含 stub_status,systemd 托管) |
| `zey nginxExporterInit` | 一键安装 nginx-prometheus-exporter 并用 systemd 开机自启托管 |
| `zey install` | 把 zey 自身装到 PATH(默认 `/usr/bin`),之后任意目录可直接运行 zey |

工作负载类型支持 `deployment`(默认)、`statefulset`、`daemonset`,可用简写 `deploy/sts/ds`。

## 用法示例

```bash
# 1) 配置连接信息(只需一次)
zey config set --context kind-kind --namespace dev
zey config view

# 2) 查看环境变量
zey env get deploy/nginx

# 3) 立即改环境变量
zey env set deploy/nginx LOG_LEVEL=debug FEATURE_X=on OLD_VAR-

# 4) 定时改(三选一)
zey env schedule deploy/nginx MAINTENANCE=on --after 30m
zey env schedule deploy/nginx MAINTENANCE=on --at '2026-06-08 02:00'
zey env schedule deploy/nginx HEARTBEAT=tick --every 1h   # 周期任务,Ctrl-C 停止

# 5) 导出 / 应用(默认存档到 ~/.zey/exports/,自动带时间戳)
zey export                 # 存档当前命名空间,结束打印文件路径
zey apply                  # 应用最新一份存档(也可 -f 指定文件)

# 6) 本机运维(需 Linux + root):源码编译装 nginx + 装 exporter,都交给 systemd
sudo zey nginxInstall --source ./nginx-1.31.1.tar.gz   # 编译装 nginx,stub_status 监听 :8081
sudo zey nginxExporterInit                             # 装 exporter,默认采集 :8081
# 任意系统可加 --dry-run 预览全部步骤,不实际执行

# 全局 flag 可临时覆盖配置
zey env get deploy/nginx -n other-ns --context prod
```

## 代码结构

```
main.go                    程序入口
cmd/                       命令行层(cobra)
  root.go                  根命令 + 全局 flag + 构造客户端
  config.go env.go export.go apply.go nginxinstall.go nginxexporter.go install.go
internal/
  config/config.go         读写 ~/.zey/config.json
  k8s/
    client.go              连接集群
    env.go                 环境变量增删改查(含乐观锁重试)
    resources.go           导出/应用 svc、deploy、cm
  nginxexporter/
    install.go             安装 nginx exporter + 注册 systemd 服务
  nginxinstall/
    install.go             源码编译安装 nginx + 注册 systemd 服务
```
