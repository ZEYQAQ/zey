package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"zey/internal/k8s"
)

func newEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "查看 / 立即修改 / 定时修改 工作负载的环境变量",
	}
	cmd.AddCommand(newEnvGetCmd(), newEnvSetCmd(), newEnvScheduleCmd())
	return cmd
}

// parseRef 解析 "deploy/myapp" 这种引用;不带 "/" 时默认是 deployment。
// 返回多个值是 Go 的惯用法(这里返回 kind, name, err)。
func parseRef(s string) (kind, name string, err error) {
	if strings.Contains(s, "/") {
		parts := strings.SplitN(s, "/", 2)
		if parts[0] == "" || parts[1] == "" {
			return "", "", fmt.Errorf("无效的资源引用 %q,应形如 deploy/myapp", s)
		}
		return parts[0], parts[1], nil
	}
	return "deployment", s, nil
}

// parseEnvArgs 把命令行里的 KEY=VALUE / KEY- 拆成「要设置的」和「要删除的」。
// 这跟 kubectl set env 的写法一致。
func parseEnvArgs(args []string) (set map[string]string, unset []string, err error) {
	set = make(map[string]string)
	for _, a := range args {
		switch {
		case strings.Contains(a, "="):
			kv := strings.SplitN(a, "=", 2)
			if kv[0] == "" {
				return nil, nil, fmt.Errorf("无效的环境变量 %q", a)
			}
			set[kv[0]] = kv[1]
		case strings.HasSuffix(a, "-"):
			key := strings.TrimSuffix(a, "-")
			if key == "" {
				return nil, nil, fmt.Errorf("无效的删除项 %q", a)
			}
			unset = append(unset, key)
		default:
			return nil, nil, fmt.Errorf("无法识别的参数 %q(KEY=VALUE 设置,KEY- 删除)", a)
		}
	}
	return set, unset, nil
}

func newEnvGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get <type/name>",
		Short:   "查看工作负载各容器的环境变量",
		Example: "  zey env get deploy/nginx\n  zey env get nginx        # 不写类型默认 deployment",
		Args:    cobra.ExactArgs(1), // 必须正好 1 个参数
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, name, err := parseRef(args[0])
			if err != nil {
				return err
			}
			client, err := newClient()
			if err != nil {
				return err
			}
			containers, err := client.GetEnv(context.Background(), kind, name)
			if err != nil {
				return err
			}
			fmt.Printf("命名空间 %s 中 %s/%s 的环境变量:\n", client.Namespace(), kind, name)
			for _, ce := range containers {
				fmt.Printf("\n  容器 [%s]:\n", ce.Container)
				if len(ce.Vars) == 0 {
					fmt.Println("    (无)")
					continue
				}
				for _, v := range ce.Vars {
					if v.ValueFrom != nil {
						fmt.Printf("    %s=<引用 valueFrom>\n", v.Name)
					} else {
						fmt.Printf("    %s=%s\n", v.Name, v.Value)
					}
				}
			}
			return nil
		},
	}
}

func newEnvSetCmd() *cobra.Command {
	var container string
	cmd := &cobra.Command{
		Use:     "set <type/name> KEY=VALUE [KEY2=VALUE2 ...] [KEY3-]",
		Short:   "立即修改环境变量(KEY=VALUE 设置,KEY- 删除)",
		Example: "  zey env set deploy/nginx LOG_LEVEL=debug FEATURE_X=on OLD_VAR-",
		Args:    cobra.MinimumNArgs(2), // 至少:资源引用 + 1 个改动
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, name, err := parseRef(args[0])
			if err != nil {
				return err
			}
			set, unset, err := parseEnvArgs(args[1:])
			if err != nil {
				return err
			}
			client, err := newClient()
			if err != nil {
				return err
			}
			return doSetEnv(context.Background(), client, kind, name, container, set, unset)
		},
	}
	cmd.Flags().StringVarP(&container, "container", "c", "", "只改指定容器(默认改所有容器)")
	return cmd
}

func newEnvScheduleCmd() *cobra.Command {
	var container, at, after, every string
	cmd := &cobra.Command{
		Use:   "schedule <type/name> KEY=VALUE [...]",
		Short: "定时修改环境变量",
		Long: `定时修改环境变量。三选一指定触发方式:
  --after  多久后执行一次,如 30s / 10m / 2h
  --at     在某绝对时间执行一次,如 '2026-06-08 15:04' 或 '15:04'
  --every  每隔多久重复执行,如 1h(前台常驻,Ctrl-C 停止)`,
		Example: "  zey env schedule deploy/nginx MAINTENANCE=on --at '2026-06-08 02:00'\n  zey env schedule deploy/nginx CACHE=off --after 30m",
		Args:    cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, name, err := parseRef(args[0])
			if err != nil {
				return err
			}
			set, unset, err := parseEnvArgs(args[1:])
			if err != nil {
				return err
			}
			client, err := newClient()
			if err != nil {
				return err
			}

			// signal.NotifyContext:收到 Ctrl-C / SIGTERM 时,ctx 会被取消。
			// 这是 Go 里实现「优雅退出」的标准写法。
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			run := func() {
				if err := doSetEnv(ctx, client, kind, name, container, set, unset); err != nil {
					fmt.Fprintf(os.Stderr, "执行失败: %v\n", err)
				}
			}

			// 模式一:周期重复(--every)
			if every != "" {
				d, err := time.ParseDuration(every)
				if err != nil {
					return fmt.Errorf("无效的 --every: %w", err)
				}
				fmt.Printf("已启动周期任务:每隔 %s 执行一次,Ctrl-C 停止\n", d)
				ticker := time.NewTicker(d)
				defer ticker.Stop()
				for { // 死循环 + select,典型的事件循环
					select {
					case <-ctx.Done(): // 收到退出信号
						fmt.Println("\n已停止")
						return nil
					case <-ticker.C: // 每个周期触发一次
						run()
					}
				}
			}

			// 模式二:一次性(--after / --at),先算出触发时间。
			var fireAt time.Time
			switch {
			case after != "":
				d, err := time.ParseDuration(after)
				if err != nil {
					return fmt.Errorf("无效的 --after: %w", err)
				}
				fireAt = time.Now().Add(d)
			case at != "":
				fireAt, err = parseScheduleTime(at)
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("请指定 --after、--at 或 --every 之一")
			}

			wait := time.Until(fireAt)
			if wait < 0 {
				return fmt.Errorf("触发时间 %s 已经过去了", fireAt.Format("2006-01-02 15:04:05"))
			}
			fmt.Printf("将在 %s(约 %s 后)修改 %s/%s,Ctrl-C 取消\n",
				fireAt.Format("2006-01-02 15:04:05"), wait.Truncate(time.Second), kind, name)

			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				fmt.Println("\n已取消")
				return nil
			case <-timer.C:
				run()
				return nil
			}
		},
	}
	cmd.Flags().StringVarP(&container, "container", "c", "", "只改指定容器(默认改所有容器)")
	cmd.Flags().StringVar(&at, "at", "", "在某绝对时间执行一次")
	cmd.Flags().StringVar(&after, "after", "", "在多久后执行一次")
	cmd.Flags().StringVar(&every, "every", "", "每隔多久重复执行")
	return cmd
}

// doSetEnv 真正调用 SetEnv 并打印结果,被 set 和 schedule 共用。
func doSetEnv(ctx context.Context, client *k8s.Client, kind, name, container string, set map[string]string, unset []string) error {
	changed, err := client.SetEnv(ctx, kind, name, container, set, unset)
	if err != nil {
		return err
	}
	if len(changed) == 0 {
		fmt.Println("没有需要改动的环境变量(可能容器名不匹配)")
		return nil
	}
	fmt.Printf("%s 已更新 %s/%s:\n", time.Now().Format("15:04:05"), kind, name)
	for _, c := range changed {
		fmt.Printf("  %s\n", c)
	}
	return nil
}

// parseScheduleTime 支持几种常见时间格式。
// 注意 Go 的时间格式不用 yyyy-MM-dd,而是用「参考时间」2006-01-02 15:04:05 来表示!
func parseScheduleTime(s string) (time.Time, error) {
	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		time.RFC3339,
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			return t, nil
		}
	}
	// 只给了 "15:04" 就当成今天
	if t, err := time.ParseInLocation("15:04", s, time.Local); err == nil {
		now := time.Now()
		return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, time.Local), nil
	}
	return time.Time{}, fmt.Errorf("无法解析时间 %q(支持 '2006-01-02 15:04'、'15:04'、RFC3339)", s)
}
