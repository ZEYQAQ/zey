package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newExportCmd() *cobra.Command {
	var output string
	var allNamespaces bool
	cmd := &cobra.Command{
		Use:     "export",
		Short:   "导出 Service / Deployment / ConfigMap 到 YAML",
		Example: "  zey export -o backup.yaml\n  zey export -A          # 所有命名空间\n  zey export             # 打到标准输出",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClient()
			if err != nil {
				return err
			}

			// output 为 "-" 或空时写到标准输出,否则写文件。
			w := os.Stdout
			if output != "" && output != "-" {
				f, err := os.Create(output)
				if err != nil {
					return fmt.Errorf("创建输出文件失败: %w", err)
				}
				defer f.Close() // 函数返回前自动关文件,无论从哪条路径 return
				w = f
			}

			counts, err := client.Export(context.Background(), w, allNamespaces)
			if err != nil {
				return err
			}

			// 统计信息打到 stderr,避免污染 stdout 里的 YAML(方便管道使用)。
			fmt.Fprintf(os.Stderr, "已导出 Service=%d Deployment=%d ConfigMap=%d\n",
				counts.Services, counts.Deployments, counts.ConfigMaps)
			if w != os.Stdout {
				fmt.Fprintf(os.Stderr, "写入文件: %s\n", output)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "-", "输出文件,'-' 表示标准输出")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "导出所有命名空间")
	return cmd
}
