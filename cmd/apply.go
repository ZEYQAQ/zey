package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newApplyCmd() *cobra.Command {
	var filename string
	cmd := &cobra.Command{
		Use:     "apply -f <file>",
		Short:   "把 YAML 里的资源创建/更新到集群",
		Example: "  zey apply -f backup.yaml\n  cat backup.yaml | zey apply -f -",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClient()
			if err != nil {
				return err
			}

			// filename 为 "-" 或空时从标准输入读,否则打开文件。
			r := os.Stdin
			if filename != "" && filename != "-" {
				f, err := os.Open(filename)
				if err != nil {
					return fmt.Errorf("打开文件失败: %w", err)
				}
				defer f.Close()
				r = f
			}

			results, err := client.Apply(context.Background(), r)
			if err != nil {
				return err
			}
			if len(results) == 0 {
				fmt.Println("没有可应用的资源")
				return nil
			}
			fmt.Println("应用结果:")
			for _, res := range results {
				fmt.Printf("  %s/%s %s\n", res.Kind, res.Name, res.Action)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&filename, "filename", "f", "-", "要应用的 YAML 文件,'-' 表示标准输入")
	return cmd
}
