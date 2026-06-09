package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"
)

func newInstallCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "把 zey 自身安装到 PATH(默认 /usr/bin),之后任意目录可直接运行 zey",
		Long: `把当前正在运行的 zey 可执行文件复制到目标目录(默认 /usr/bin),
让它进入系统 PATH,之后在任意目录都能直接敲 zey,不用再写 ./zey。

需要对目标目录有写权限(/usr/bin 通常需要 root,即 sudo)。`,
		Example: "  sudo zey install\n  sudo zey install --dir /usr/local/bin",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return selfInstall(dir)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "/usr/bin", "安装目标目录(需在 PATH 中)")
	return cmd
}

// selfInstall 把当前运行的二进制复制到 dir/zey。
func selfInstall(dir string) error {
	src, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取当前可执行文件路径失败: %w", err)
	}

	name := "zey"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dest := filepath.Join(dir, name)

	if isSameFile(src, dest) {
		fmt.Printf("zey 已经在 %s,无需重复安装\n", dest)
		return nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目标目录失败: %w", err)
	}

	// 先写临时文件再原子 rename,避免覆盖正在运行的二进制时报 "text file busy"
	tmp := dest + ".tmp"
	if err := copyExecutable(src, tmp); err != nil {
		os.Remove(tmp)
		if os.IsPermission(err) {
			return fmt.Errorf("没有写入 %s 的权限,请用 sudo 运行:sudo zey install", dir)
		}
		return fmt.Errorf("复制失败: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("安装失败: %w", err)
	}

	fmt.Printf("已安装: %s\n", dest)
	fmt.Println("现在可以在任意目录直接运行: zey")
	return nil
}

// isSameFile 判断 a、b 是否是同一个文件(按 inode 比较,能正确处理符号链接、不同写法)。
// dest 不存在时返回 false(即首次安装)。
func isSameFile(a, b string) bool {
	fa, ea := os.Stat(a)
	fb, eb := os.Stat(b)
	if ea != nil || eb != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

// copyExecutable 复制文件并设为可执行 0755。
func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, 0o755) // 防止 umask 抹掉执行位
}
