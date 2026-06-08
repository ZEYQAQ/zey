package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"zey/internal/config"
)

func newExportCmd() *cobra.Command {
	var output string
	var allNamespaces bool
	cmd := &cobra.Command{
		Use:   "export",
		Short: "导出 Service / Deployment / ConfigMap 到文件存档",
		Long: `把命名空间下的 Service / Deployment / ConfigMap 导出成 YAML 存档文件。

默认自动写入存档目录 ~/.zey/exports/,文件名带命名空间和时间戳,便于留档;
导出完成后打印存档文件的完整路径。`,
		Example: "  zey export                 # 自动存档到 ~/.zey/exports/(默认)\n  zey export -o backup.yaml  # 指定文件\n  zey export -A              # 所有命名空间\n  zey export -o -            # 仍输出到控制台(给管道用)",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClient()
			if err != nil {
				return err
			}

			// 先导出到内存缓冲。这样万一连集群失败,不会留下半截空文件。
			var buf bytes.Buffer
			counts, err := client.Export(context.Background(), &buf, allNamespaces)
			if err != nil {
				return err
			}

			// 转义出口:-o - 仍然输出到控制台(方便接管道)
			if output == "-" {
				os.Stdout.Write(buf.Bytes())
				fmt.Fprintf(os.Stderr, "已导出 Service=%d Deployment=%d ConfigMap=%d\n",
					counts.Services, counts.Deployments, counts.ConfigMaps)
				return nil
			}

			// 决定存档路径:没指定 -o 就自动生成到 ~/.zey/exports/
			path := output
			if path == "" {
				path, err = defaultExportPath(client.Namespace(), allNamespaces)
				if err != nil {
					return err
				}
			}

			if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
				return fmt.Errorf("写入存档文件失败: %w", err)
			}

			abs, _ := filepath.Abs(path)
			fmt.Printf("已导出 Service=%d Deployment=%d ConfigMap=%d\n",
				counts.Services, counts.Deployments, counts.ConfigMaps)
			fmt.Printf("存档文件: %s\n", abs) // 最后打印存档文件路径
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "输出文件;留空=自动存档到 ~/.zey/exports/;'-'=输出到控制台")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "导出所有命名空间")
	return cmd
}

// defaultExportPath 生成默认存档路径:~/.zey/exports/zey-export-<ns>-<时间戳>.yaml
func defaultExportPath(namespace string, allNamespaces bool) (string, error) {
	dir, err := config.ExportsDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建存档目录失败: %w", err)
	}
	nsPart := namespace
	if allNamespaces {
		nsPart = "all"
	}
	// 时间戳用 Go 的参考时间格式:20060102-150405 排序友好
	name := fmt.Sprintf("zey-export-%s-%s.yaml", nsPart, time.Now().Format("20060102-150405"))
	return filepath.Join(dir, name), nil
}
