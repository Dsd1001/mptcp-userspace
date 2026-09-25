package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mptcp-desktop/engine/multipath"
)

type options struct {
	root, config, source, sha, statusFile    string
	yes, purge, start, follow, showKey, edit bool
}
type terminal struct {
	reader *bufio.Reader
	out    io.Writer
}

func (t *terminal) line(label, defaultValue string) (string, error) {
	if defaultValue != "" {
		fmt.Fprintf(t.out, "%s [%s]：", label, defaultValue)
	} else {
		fmt.Fprintf(t.out, "%s：", label)
	}
	text, err := t.reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		text = defaultValue
	}
	return text, nil
}
func (t *terminal) confirm(label string) (bool, error) {
	v, e := t.line(label+"（输入 yes 确认）", "")
	return v == "yes", e
}
func (t *terminal) configure(c Config) (Config, error) {
	var err error
	fmt.Fprintln(t.out, "请使用独立于旧 Native MPTCP 和 SS backend 的新聚合端口。传输密钥不是 SS 密码。")
	if c.ListenTCP, err = t.line("TCP 聚合监听", c.ListenTCP); err != nil {
		return c, err
	}
	if c.BackendTCP, err = t.line("已有 SS TCP backend", c.BackendTCP); err != nil {
		return c, err
	}
	defaultUDP := "n"
	if c.UDPEnabled {
		defaultUDP = "y"
	}
	udp, err := t.line("启用独立 UDP 多路径？y/n", defaultUDP)
	if err != nil {
		return c, err
	}
	if udp != "y" && udp != "n" {
		return c, errors.New("请输入 y 或 n")
	}
	c.UDPEnabled = udp == "y"
	if c.UDPEnabled {
		if c.ListenUDP, err = t.line("UDP 聚合监听", c.ListenUDP); err != nil {
			return c, err
		}
		if c.BackendUDP, err = t.line("已有 SS UDP backend", c.BackendUDP); err != nil {
			return c, err
		}
	}
	key, err := t.line("传输密钥：回车保留；new 生成新密钥；或粘贴 64 位十六进制（输入可在终端显示）", "")
	if err != nil {
		return c, err
	}
	if key == "new" {
		c.TransportKey, err = multipath.NewKey()
		if err != nil {
			return c, err
		}
	} else if key != "" {
		c.TransportKey = key
	}
	sessions, err := t.line("最多客户端会话数", strconv.Itoa(c.MaxSessions))
	if err != nil {
		return c, err
	}
	c.MaxSessions, err = strconv.Atoi(sessions)
	if err != nil {
		return c, err
	}
	return c, c.validate()
}

func configPath(m *manager, o options, server bool) (string, error) {
	if o.config != "" {
		return filepath.Abs(o.config)
	}
	if server {
		if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
			return filepath.Join(dir, "config"), nil
		}
	}
	return m.path(configRel, false)
}
func loadSelected(m *manager, o options, server bool) (Config, error) {
	path, err := configPath(m, o, server)
	if err != nil {
		return Config{}, err
	}
	return readConfig(path)
}

func execute(ctx context.Context, command string, o options, t *terminal) error {
	m, err := newManager(o.root, t.out)
	if err != nil {
		return err
	}
	switch command {
	case "version":
		fmt.Fprintf(t.out, "mptcp-landing %s %s/%s MPX/3 experimental source=%s\n", multipath.Version, runtime.GOOS, runtime.GOARCH, multipath.SourceID)
		return nil
	case "help":
		help(t.out)
		return nil
	case "menu":
		return menu(ctx, o, t)
	case "server":
		c, e := loadSelected(m, o, true)
		if e != nil {
			return e
		}
		return runServer(ctx, c, o.statusFile, t.out)
	case "install":
		var c Config
		if o.config != "" {
			c, err = loadSelected(m, o, false)
		} else {
			path, _ := m.path(configRel, false)
			c, err = readConfig(path)
			if os.IsNotExist(err) || path == "" {
				c, err = defaultConfig()
			}
			if err == nil {
				c, err = t.configure(c)
			}
		}
		if err != nil {
			return err
		}
		if !o.yes {
			ok, e := t.confirm("安装独立 Landing 服务，并保留现有 Native/SS 服务不变？")
			if e != nil {
				return e
			}
			if !ok {
				return errors.New("已取消安装")
			}
		}
		source, e := os.Executable()
		if e != nil {
			return e
		}
		return m.install(c, source, o.start)
	case "config":
		c, e := loadSelected(m, o, false)
		if e != nil {
			return e
		}
		if o.source != "" {
			c, e = readConfig(o.source)
			if e != nil {
				return e
			}
			return m.setConfig(c)
		}
		if o.edit {
			c, e = t.configure(c)
			if e != nil {
				return e
			}
			ok, e := t.confirm("保存配置（运行中将重启，仅影响新版 Landing）？")
			if e != nil {
				return e
			}
			if !ok {
				return errors.New("已取消保存")
			}
			return m.setConfig(c)
		}
		if !o.showKey {
			c = c.redacted()
		}
		_, err = t.out.Write(configBytes(c))
		return err
	case "start", "stop", "restart":
		return m.service(command)
	case "upgrade":
		if o.source == "" {
			return errors.New("需要 --source 新二进制路径和 --sha256")
		}
		return m.upgrade(o.source, o.sha)
	case "rollback":
		return m.rollback()
	case "uninstall":
		return m.uninstall(o.yes, o.purge)
	case "status":
		record, e := m.manifest()
		if e != nil {
			return e
		}
		fmt.Fprintf(t.out, "已安装：%t；管理记录版本：%s；二进制 SHA-256：%s\n", record.Installed, record.Version, record.BinarySHA)
		c, e := loadSelected(m, o, false)
		if e == nil {
			fmt.Fprint(t.out, string(configBytes(c.redacted())))
		}
		if !record.Installed {
			return nil
		}
		if _, e = m.owned(); e != nil {
			return e
		}
		if e = m.system("status", "--no-pager", "--full", serviceName); e != nil {
			return e
		}
		if b, e := m.read(statusRel, 1<<20); e == nil {
			fmt.Fprintf(t.out, "最近状态快照（以 updated 时间为准）：\n%s\n", b)
		}
		return nil
	case "logs":
		if o.root != "" {
			fmt.Fprintln(t.out, "暂存模式没有运行服务，也没有 systemd 日志。")
			return nil
		}
		if runtime.GOOS != "linux" {
			return errors.New("journalctl 日志仅在 Linux/systemd 主机可用")
		}
		args := []string{"-u", serviceName, "--no-pager", "-n", "100"}
		if o.follow {
			args = append(args, "-f")
		}
		cmd := exec.CommandContext(ctx, "/usr/bin/journalctl", args...)
		cmd.Stdout = t.out
		cmd.Stderr = t.out
		err = cmd.Run()
		if ctx.Err() != nil {
			return nil
		}
		return err
	case "doctor":
		c, e := loadSelected(m, o, false)
		if e != nil {
			return e
		}
		return doctor(ctx, m, c, t.out)
	default:
		return fmt.Errorf("未知子命令 %q；运行 help 查看帮助", command)
	}
}

func doctor(ctx context.Context, m *manager, c Config, out io.Writer) error {
	if err := c.validate(); err != nil {
		return err
	}
	fmt.Fprintf(out, "版本 %s；主机 %s/%s；配置有效；传输密钥已隐藏。\n", multipath.Version, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintln(out, "运行协议为应用层 MPX/3；客户端需支持 MPX/3；建议两端使用相同版本。无需启用内核 MPTCP，不修改防火墙、路由或 SS 配置。")
	var failures []error
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	backend, err := multipath.PlainDial(probe, c.BackendTCP)
	cancel()
	if err != nil {
		fmt.Fprintf(out, "TCP backend 不可连接：%v\n", err)
		failures = append(failures, err)
	} else {
		backend.Close()
		fmt.Fprintln(out, "TCP backend 接受连接；尚未验证 SS 密码、算法或应用请求。")
	}
	if c.UDPEnabled {
		fmt.Fprintln(out, "UDP backend 的可达性不能由 UDP dial 证明；需要通过实际 SS UDP 请求验收。")
	}
	if m.active() {
		fmt.Fprintln(out, "受管服务为 active；为避免端口冲突，未再次绑定监听端口。")
	} else {
		l, e := multipath.PlainListen(ctx, c.ListenTCP)
		if e != nil {
			fmt.Fprintf(out, "TCP 监听端口不可绑定：%v\n", e)
			failures = append(failures, e)
		} else {
			l.Close()
			fmt.Fprintln(out, "TCP 监听端口可绑定（检查后立即释放）。")
		}
		if c.UDPEnabled {
			a, e := net.ResolveUDPAddr("udp", c.ListenUDP)
			if e == nil {
				var u *net.UDPConn
				u, e = net.ListenUDP("udp", a)
				if e == nil {
					u.Close()
				}
			}
			if e != nil {
				fmt.Fprintf(out, "UDP 监听端口不可绑定：%v\n", e)
				failures = append(failures, e)
			} else {
				fmt.Fprintln(out, "UDP 监听端口可绑定（检查后立即释放）。")
			}
		}
	}
	if m.staged {
		fmt.Fprintln(out, "当前 --root 为暂存测试，未验证 Debian/systemd 安装运行。")
	}
	return errors.Join(failures...)
}

func menu(ctx context.Context, o options, t *terminal) error {
	for {
		fmt.Fprintf(t.out, "\n=== MPTCP Landing %s · 应用层多路径（实验版）===\n", multipath.Version)
		fmt.Fprintln(t.out, "1 安装/初始化   2 编辑配置   3 环境与端口检查\n4 启动          5 停止       6 重启\n7 状态          8 最近日志   9 实时日志\n10 升级         11 回滚      12 卸载\n13 显示连接密钥（敏感）      0 退出")
		choice, err := t.line("选择", "")
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if choice == "0" {
			return nil
		}
		next := o
		command := ""
		switch choice {
		case "1":
			command = "install"
			next.start = true
		case "2":
			command = "config"
			next.edit = true
		case "3":
			command = "doctor"
		case "4":
			command = "start"
		case "5":
			command = "stop"
		case "6":
			command = "restart"
		case "7":
			command = "status"
		case "8":
			command = "logs"
		case "9":
			command = "logs"
			next.follow = true
		case "10":
			command = "upgrade"
			next.source, err = t.line("新版二进制文件路径", "")
			if err == nil {
				next.sha, err = t.line("来自可信交付清单的 SHA-256", "")
			}
		case "11":
			ok, e := t.confirm("回滚新版 Landing 的二进制及配置（会中断现有会话）？")
			err = e
			if ok {
				command = "rollback"
			}
		case "12":
			ok, e := t.confirm("停止并卸载新版 Landing（不影响旧 Native/SS）？")
			err = e
			if ok {
				command = "uninstall"
				next.yes = true
				next.purge, err = t.confirm("同时删除新版 Landing 私有配置和密钥？")
			}
		case "13":
			ok, e := t.confirm("在当前终端显示完整传输密钥？不要截图或共享日志。")
			err = e
			if ok {
				command = "config"
				next.showKey = true
			}
		default:
			fmt.Fprintln(t.out, "无效选项。")
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if command != "" {
			err = execute(ctx, command, next, t)
			if err == io.EOF {
				return nil
			}
			if err != nil {
				fmt.Fprintf(t.out, "操作未完成：%v\n", err)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		default:
		}
	}
}
func help(out io.Writer) {
	fmt.Fprintln(out, `mptcp-landing：无参数进入中文交互菜单。
命令：install config start stop restart status logs doctor upgrade rollback uninstall server version help

安装：sudo ./mptcp-landing（选择 1）；或 install --config 私有配置.json --yes --start
配置：config（默认隐藏密钥）；config --edit；config --source 私有配置.json
显示密钥：config --show-key（敏感，仅本人终端）
升级：upgrade --source ./新版二进制 --sha256 可信的64位SHA256
回滚：rollback（上一二进制及其配置）
卸载：uninstall --yes；加 --purge 才删除配置和密钥
日志：logs；logs --follow
前台：server --config 私有配置.json [--status-file 状态文件.json]
暂存：上述管理命令加 --root /绝对暂存目录；不会调用主机 systemd，不代表 Debian 已验收。
配置文件必须为 0600/0400；不使用外部安装脚本，不触碰 Native MPTCP、SS、路由或防火墙。`)
}
func runCLI(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	command := "menu"
	if len(args) > 0 && args[0] == "--version" {
		command = "version"
		args = args[1:]
	} else if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	var o options
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(out)
	flags.StringVar(&o.root, "root", "", "absolute staging root (no systemd)")
	flags.StringVar(&o.config, "config", "", "private configuration path")
	flags.StringVar(&o.source, "source", "", "upgrade binary or config source")
	flags.StringVar(&o.sha, "sha256", "", "trusted upgrade checksum")
	flags.StringVar(&o.statusFile, "status-file", "", "server runtime status file")
	flags.BoolVar(&o.yes, "yes", false, "confirm install/uninstall")
	flags.BoolVar(&o.purge, "purge", false, "delete owned private config on uninstall")
	flags.BoolVar(&o.start, "start", false, "start service during installation")
	flags.BoolVar(&o.follow, "follow", false, "follow journal")
	flags.BoolVar(&o.showKey, "show-key", false, "explicitly show sensitive key")
	flags.BoolVar(&o.edit, "edit", false, "edit configuration interactively")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("不接受额外的位置参数")
	}
	return execute(ctx, command, o, &terminal{reader: bufio.NewReaderSize(in, 64<<10), out: out})
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := runCLI(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "错误：%v\n", err)
		os.Exit(1)
	}
}
