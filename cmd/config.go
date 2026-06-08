package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"zey/internal/config"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "查看或设置 k8s 连接信息",
	}
	cmd.AddCommand(newConfigSetCmd(), newConfigViewCmd())
	return cmd
}

func newConfigSetCmd() *cobra.Command {
	var kubeconfig, ctxName, namespace string
	cmd := &cobra.Command{
		Use:   "set",
		Short: "保存连接信息到 ~/.zey/config.json",
		Example: "  zey config set --kubeconfig ~/.kube/config --context kind-kind --namespace dev",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			// cmd.Flags().Changed 判断用户这次是否真的传了这个 flag,
			// 只覆盖显式传入的字段,不会把没传的清空。
			if cmd.Flags().Changed("kubeconfig") {
				cfg.Kubeconfig = kubeconfig
			}
			if cmd.Flags().Changed("context") {
				cfg.Context = ctxName
			}
			if cmd.Flags().Changed("namespace") {
				cfg.Namespace = namespace
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			path, _ := config.Path()
			fmt.Printf("已保存到 %s\n", path)
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "kubeconfig 文件路径")
	cmd.Flags().StringVar(&ctxName, "context", "", "context 名称")
	cmd.Flags().StringVar(&namespace, "namespace", "", "默认命名空间")
	return cmd
}

func newConfigViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "view",
		Short: "显示当前保存的连接信息",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			path, _ := config.Path()
			fmt.Printf("配置文件: %s\n", path)
			fmt.Printf("  kubeconfig: %s\n", orDefault(cfg.Kubeconfig, "(默认 ~/.kube/config)"))
			fmt.Printf("  context:    %s\n", orDefault(cfg.Context, "(默认)"))
			fmt.Printf("  namespace:  %s\n", orDefault(cfg.Namespace, "default"))
			return nil
		},
	}
}

// orDefault:v 为空时返回 def。Go 没有三元运算符,这种小helper很常见。
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
