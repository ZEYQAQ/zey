// Package cmd 定义所有命令行子命令,基于 spf13/cobra(kubectl、helm 用的就是它)。
package cmd

import (
	"github.com/spf13/cobra"

	"zey/internal/config"
	"zey/internal/k8s"
)

// 这些是「全局 flag」对应的变量,放在包级别供各子命令读取。
var (
	flagNamespace  string
	flagKubeconfig string
	flagContext    string
)

// Execute 是 cmd 包对外的入口,被 main 调用。
func Execute() error {
	return newRootCmd().Execute()
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "zey",
		Short: "zey —— 一个轻量的 Kubernetes 工作负载与资源管理工具",
		Long: `zey 是一个用 Go 写的 Kubernetes 助手。

能力:
  • env get/set/schedule  查看 / 立即修改 / 定时修改 工作负载环境变量
  • config                配置 k8s 连接信息(kubeconfig / context / namespace)
  • export                导出命名空间下的 Service / Deployment / ConfigMap
  • apply                 把导出的文件重新应用回集群
  • nginxExporterInit     一键安装 nginx-prometheus-exporter 并用 systemd 托管
  • install               把 zey 自身装到 PATH(/usr/bin),之后可直接运行 zey`,
		SilenceUsage:  true, // 出错时不要把一大坨用法说明也打出来
		SilenceErrors: true, // 错误统一交给 main 打印
	}

	// PersistentFlags = 对本命令及所有子命令都生效的全局 flag。
	root.PersistentFlags().StringVarP(&flagNamespace, "namespace", "n", "", "目标命名空间(覆盖配置)")
	root.PersistentFlags().StringVar(&flagKubeconfig, "kubeconfig", "", "kubeconfig 路径(覆盖配置)")
	root.PersistentFlags().StringVar(&flagContext, "context", "", "kubeconfig context(覆盖配置)")

	root.AddCommand(
		newConfigCmd(),
		newEnvCmd(),
		newExportCmd(),
		newApplyCmd(),
		newNginxExporterInitCmd(),
		newInstallCmd(),
	)
	return root
}

// newClient 合并「保存的配置」与「本次命令行临时 flag」,构造一个 k8s 客户端。
// 几乎每个子命令开头都会调它。
func newClient() (*k8s.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	// 命令行 flag 优先级高于配置文件
	if flagKubeconfig != "" {
		cfg.Kubeconfig = flagKubeconfig
	}
	if flagContext != "" {
		cfg.Context = flagContext
	}
	return k8s.NewClient(cfg, flagNamespace)
}
