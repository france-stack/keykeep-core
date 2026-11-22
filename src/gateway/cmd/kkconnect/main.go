// Command kkconnect 是员工端的本地网关（KeyKeep client 模式）。
//
// 用法（员工在自己电脑上执行一次）：
//
//	kkconnect login --server http://192.168.1.20:8443 --account alice --password '…'
//	kkconnect serve            # 启动本地连接器，监听 127.0.0.1:3426
//	kkconnect link claude      # 把 Claude Code 配置指到本地连接器
//
// 老板的服务器不需要任何变化：员工仍然用控制台签发的 kk_ 虚拟 Key，
// 连接器只是把 Agent 的请求原样转给公司网关。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/keykeep/gateway/internal/agentcfg"
	"github.com/keykeep/gateway/internal/relay"
	"github.com/keykeep/gateway/internal/version"
	"golang.org/x/term"
)

// profile 是员工本机的连接配置，落在 ~/.keykeep/config.json（0600）。
type profile struct {
	Server   string `json:"server"`   // 控制面地址，如 http://192.168.1.20:8443
	Gateway  string `json:"gateway"`  // 数据面地址，如 http://192.168.1.20:8080
	APIKey   string `json:"apiKey"`   // kk_ 虚拟 Key
	Account  string `json:"account"`  // 员工账号，仅用于展示
	Listen   string `json:"listen"`   // 本地监听地址
	ClientID string `json:"clientId"` // 机器名，服务端排障用
}

func defaultListen() string { return "127.0.0.1:3426" }

func main() {
	log.SetFlags(0)
	log.SetPrefix("kkconnect ")

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "login":
		err = cmdLogin(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "link":
		err = cmdLink(os.Args[2:])
	case "unlink":
		err = cmdUnlink(os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	case "config":
		err = cmdConfig(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("kkconnect %s\n", version.String())
	case "help", "--help", "-h":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Printf("错误：%v", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`kkconnect —— KeyKeep 员工端本地网关

  kkconnect login --server URL [--gateway URL] --key kk_… [--account 名字]
        保存服务器地址与虚拟 Key（~/.keykeep/config.json，权限 0600）
  kkconnect serve [--listen 127.0.0.1:3426]
        启动本地连接器；Agent 连它，它转发到公司网关
  kkconnect link <claude|codex|gemini|opencode|qwen> [--model 模型名]
        把对应 Agent 的配置指向本地连接器（原值自动备份，可 unlink 还原）
  kkconnect unlink <claude|codex|gemini|opencode|qwen>
        按备份还原 Agent 配置
  kkconnect status
        查看服务器连通性与当前绑定
  kkconnect config --gateway URL [--server URL]
        只改地址，不需要重新粘贴 Key（换服务器地址时用这个）

员工只做三件事：login 一次、serve 常驻、link 一次。老板的服务器不用改。
`)
}

// ---------------------------------------------------------------- login

func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	server := fs.String("server", "", "控制面地址，如 http://192.168.1.20:8443")
	gateway := fs.String("gateway", "", "数据面地址，如 http://192.168.1.20:8080（默认按 server 主机 + 8080 推断）")
	key := fs.String("key", "", "员工虚拟 Key（kk_…，由老板在控制台签发）")
	account := fs.String("account", "", "员工账号（可选，仅展示）")
	// 默认值留空：这样「没传」和「传了默认值」可以区分。
	// 以前默认值直接是 127.0.0.1:3426，于是只想换个服务器地址的人，
	// 会连带把自己改过的本地端口重置掉。
	listen := fs.String("listen", "", "本地连接器监听地址（默认沿用上次的值，或 127.0.0.1:3426）")
	_ = fs.Parse(args)

	previous, _ := loadProfileOptional()

	if *key == "" {
		// 允许交互粘贴，避免 Key 留在 shell 历史里。
		fmt.Print("粘贴员工虚拟 Key（kk_…，输入不回显）：")
		line, err := readPassword()
		fmt.Println()
		if err != nil {
			return err
		}
		*key = line
	}
	*key = strings.TrimSpace(*key)
	if !strings.HasPrefix(*key, "kk_") {
		return errors.New("虚拟 Key 必须以 kk_ 开头；请找老板在控制台「API 密钥」页签发")
	}
	if *server == "" {
		return errors.New("必须给 --server：老板控制台的地址，例如 http://192.168.1.20:8443")
	}
	serverURL, err := parseRoot(*server)
	if err != nil {
		return fmt.Errorf("--server 非法：%w", err)
	}
	gatewayURL := *gateway
	if gatewayURL == "" {
		u := *serverURL
		u.Host = net.JoinHostPort(u.Hostname(), "8080")
		gatewayURL = u.String()
	}
	gw, err := parseRoot(gatewayURL)
	if err != nil {
		return fmt.Errorf("--gateway 非法：%w", err)
	}

	listenAddr := strings.TrimSpace(*listen)
	if listenAddr == "" {
		listenAddr = previous.Listen
	}
	if listenAddr == "" {
		listenAddr = defaultListen()
	}
	accountName := *account
	if accountName == "" {
		accountName = previous.Account
	}

	p := &profile{
		Server:   serverURL.String(),
		Gateway:  gw.String(),
		APIKey:   *key,
		Account:  accountName,
		Listen:   listenAddr,
		ClientID: hostname(),
	}
	if err := saveProfile(p); err != nil {
		return err
	}
	fmt.Printf("已保存：服务器 %s，网关 %s，Key %s\n", p.Server, p.Gateway, maskKey(p.APIKey))
	if !probe(gw.String() + "/healthz") {
		// 只警告不阻断：离线安装时先写配置、后接通网络是正常流程。
		fmt.Printf("提示：暂时连不上 %s/v1；确认地址与网络后再运行 kkconnect serve。\n", p.Gateway)
	}
	fmt.Println("下一步：kkconnect serve（保持运行），然后另开终端 kkconnect link claude")
	return nil
}

// cmdConfig 只改地址，不碰 Key。
//
// 为什么需要它：老板换机器/换 IP 时，员工以前只能重跑 login，而 login 要重新
// 粘贴 Key。一次运维动作被放大成「找老板要 Key」，这在十几号人的场景里很痛。
func cmdConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	gateway := fs.String("gateway", "", "数据面地址，如 http://192.168.1.20:8080")
	server := fs.String("server", "", "控制面地址，如 http://192.168.1.20:8443")
	listen := fs.String("listen", "", "本地连接器监听地址")
	_ = fs.Parse(args)

	if *gateway == "" && *server == "" && *listen == "" {
		return errors.New("至少给一项：--gateway、--server 或 --listen")
	}

	p, err := loadProfile()
	if err != nil {
		return err
	}

	if *server != "" {
		serverURL, err := parseRoot(*server)
		if err != nil {
			return fmt.Errorf("--server 非法：%w", err)
		}
		p.Server = serverURL.String()
	}
	if *gateway != "" {
		gw, err := parseRoot(*gateway)
		if err != nil {
			return fmt.Errorf("--gateway 非法：%w", err)
		}
		p.Gateway = gw.String()
	}
	if *listen != "" {
		p.Listen = *listen
	}
	if err := saveProfile(p); err != nil {
		return err
	}

	fmt.Printf("已更新：服务器 %s，网关 %s，本地监听 %s\n", p.Server, p.Gateway, p.Listen)
	if !probe(p.Gateway + "/healthz") {
		fmt.Printf("提示：暂时连不上 %s/v1；确认地址与网络。\n", p.Gateway)
	}
	if probe(p.Server + "/login") {
		fmt.Println("控制台可达。")
	}
	return nil
}

// ---------------------------------------------------------------- serve

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "", "本地监听地址（默认沿用 login 时的值）")
	allowExpose := fs.Bool("allow-expose", false,
		"确认把连接器绑定到非回环地址（连接器内嵌员工 Key，会暴露给同网段设备）")
	_ = fs.Parse(args)

	p, err := loadProfile()
	if err != nil {
		return err
	}
	if *listen != "" {
		p.Listen = *listen
	}
	if p.Listen == "" {
		p.Listen = defaultListen()
	}
	// 防呆：连接器带真实员工 Key 且不做来源认证，绑定非回环地址必须显式确认。
	// 空主机（":3426"）也按非回环拒绝——Go 会把它绑到所有网卡。
	if !relay.IsLoopbackHost(p.Listen) && !*allowExpose {
		return errors.New(
			"监听地址不是回环地址：" + p.Listen + " 会把内嵌的员工 Key 暴露给局域网。\n" +
				"用 --listen 127.0.0.1:3426 只监听本机；确需局域网访问再加 --allow-expose。")
	}
	upstream, err := url.Parse(p.Gateway)
	if err != nil {
		return err
	}

	handler := relay.NewHandler(relay.Config{
		Upstream: upstream,
		APIKey:   p.APIKey,
		Version:  version.String(),
		Listen:   p.Listen,
		ClientID: p.ClientID,
	})
	server := &http.Server{
		Addr:              p.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// SSE 长连接不能设写超时；空闲超时放宽到 5 分钟。
		IdleTimeout: 5 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	base := "http://" + p.Listen
	if strings.HasPrefix(p.Listen, ":") {
		base = "http://127.0.0.1" + p.Listen
	}
	fmt.Printf("KeyKeep 连接器已启动 · %s\n", base)
	fmt.Printf("  Anthropic Base URL  %s\n", base)
	fmt.Printf("  OpenAI Base URL     %s/v1\n", base)
	fmt.Printf("  对应网关            %s\n", p.Gateway)
	fmt.Printf("  状态页              %s/\n", base)
	fmt.Println("保持本窗口运行；Ctrl+C 停止。")

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("监听失败：%v", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	return nil
}

// ---------------------------------------------------------------- link / unlink

func cmdLink(args []string) error {
	// Go 的 flag 遇到第一个位置参数就停止解析；把子命令挑出来，其余交给 FlagSet，
	// 这样 `link claude --model x` 与 `link --model x claude` 都能用。
	id, rest := splitPositional(args)
	if id == "" {
		return errors.New("用法：kkconnect link <claude|codex|gemini|opencode|qwen> [--model 模型名]")
	}
	fs := flag.NewFlagSet("link", flag.ExitOnError)
	model := fs.String("model", "", "默认模型名（不填就保留 Agent 原来的模型设置）")
	_ = fs.Parse(rest)
	p, err := loadProfile()
	if err != nil {
		return err
	}
	agent, ok := agentcfg.Find(id)
	if !ok {
		return fmt.Errorf("不支持的客户端 %q（可选：claude、codex、gemini、opencode、qwen）", id)
	}
	files, err := agent.Files()
	if err != nil {
		return err
	}
	base := "http://" + p.Listen
	if strings.HasPrefix(p.Listen, ":") {
		base = "http://127.0.0.1" + p.Listen
	}
	state, err := agentcfg.LoadState()
	if err != nil {
		return err
	}
	// 复用已有快照：第二次 link 不能再把「自己上次写的值」记成用户原值。
	link := state.Linked[agent.ID]
	if err := agent.Link(files, agentcfg.Local{
		AnthropicBase: base,
		OpenAIBase:    base + "/v1",
		Key:           relay.PlaceholderKey,
		Model:         *model,
	}, &link); err != nil {
		return err
	}
	state.Linked[agent.ID] = link
	if err := agentcfg.SaveState(state); err != nil {
		return err
	}
	fmt.Printf("已把 %s 指向本地连接器（%s）\n", agent.Name, strings.Join(sortedValues(files), "、"))
	fmt.Println("重开一个该客户端的会话即可生效。要还原：kkconnect unlink " + agent.ID)
	return nil
}

func cmdUnlink(args []string) error {
	if len(args) < 1 {
		return errors.New("用法：kkconnect unlink <claude|codex|gemini|opencode|qwen>")
	}
	agent, ok := agentcfg.Find(args[0])
	if !ok {
		return fmt.Errorf("不支持的客户端 %q", args[0])
	}
	state, err := agentcfg.LoadState()
	if err != nil {
		return err
	}
	link, ok := state.Linked[agent.ID]
	if !ok {
		return fmt.Errorf("%s 没有被 kkconnect 改过", agent.Name)
	}
	files, err := agent.Files()
	if err != nil {
		return err
	}
	if err := agent.Unlink(files, &link); err != nil {
		return err
	}
	delete(state.Linked, agent.ID)
	if err := agentcfg.SaveState(state); err != nil {
		return err
	}
	fmt.Printf("已还原 %s 的配置\n", agent.Name)
	return nil
}

// splitPositional 取第一个非标志参数（子命令），返回它和剩余参数。
// 带值的标志（--model x）要连同值一起留在剩余参数里，不能把 x 当成子命令。
func splitPositional(args []string) (string, []string) {
	rest := make([]string, 0, len(args))
	id := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			rest = append(rest, arg)
			// --model=x 自带值；--model x 的 x 是值，不能当子命令。
			if !strings.Contains(arg, "=") && i+1 < len(args) {
				rest = append(rest, args[i+1])
				i++
			}
			continue
		}
		if id == "" {
			id = arg
			continue
		}
		rest = append(rest, arg)
	}
	return id, rest
}

// sortedValues 把逻辑名→路径的 map 按路径排序输出，保证输出稳定。
func sortedValues(files map[string]string) []string {
	out := make([]string, 0, len(files))
	for _, path := range files {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------- status

func cmdStatus(args []string) error {
	p, err := loadProfile()
	if err != nil {
		return err
	}
	state, err := agentcfg.LoadState()
	if err != nil {
		return err
	}
	base := "http://" + p.Listen
	fmt.Printf("服务器      %s\n", p.Server)
	fmt.Printf("网关        %s\n", p.Gateway)
	fmt.Printf("我的 Key    %s\n", maskKey(p.APIKey))
	fmt.Printf("本地连接器  %s\n", base)

	reachable := probe(base + "/healthz")
	fmt.Printf("连接器状态  %s\n", yesNo(reachable))
	upstream := probe(p.Gateway + "/healthz")
	fmt.Printf("网关连通性  %s\n", yesNo(upstream))
	if len(state.Linked) == 0 {
		fmt.Println("已接入客户端  （无）")
	} else {
		for id, link := range state.Linked {
			fmt.Printf("已接入客户端  %s → %s\n", id, strings.Join(link.Paths(), "、"))
		}
	}
	return nil
}

func probe(target string) bool {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(target)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func yesNo(ok bool) string {
	if ok {
		return "正常"
	}
	return "不可达"
}

// ---------------------------------------------------------------- profile IO

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".keykeep", "config.json"), nil
}

func loadProfile() (*profile, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("还没有登录过：先运行 kkconnect login --server … --key kk_…")
	}
	if err != nil {
		return nil, err
	}
	var p profile
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("配置损坏（%s）：%w", path, err)
	}
	if p.APIKey == "" || p.Gateway == "" {
		return nil, errors.New("配置不完整：重新运行 kkconnect login")
	}
	return &p, nil
}

// loadProfileOptional 与 loadProfile 的区别：文件不存在时返回零值而不报错。
//
// 用于 login 想沿用上次的 listen/account 的场景——首次登录时本来就没有旧配置，
// 那不是错误。
func loadProfileOptional() (profile, error) {
	path, err := configPath()
	if err != nil {
		return profile{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return profile{}, nil
	}
	if err != nil {
		return profile{}, err
	}
	var p profile
	if err := json.Unmarshal(raw, &p); err != nil {
		// 旧配置损坏时不让 login 失败：login 本来就会整份重写，
		// 而这里的唯一用途是取 listen/account 的旧值。
		return profile{}, nil
	}
	return p, nil
}

func saveProfile(p *profile) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	// 原子写：先落临时文件再改名。
	// 以前直接 WriteFile 覆盖，写到一半断电会留下截断的 JSON，
	// 下次启动报「配置损坏」，员工除了重新 login 没有别的办法。
	tmp, err := os.CreateTemp(filepath.Dir(path), "config-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	// 断电/崩溃防护：先把数据压盘再改名，否则 rename 可能指向半截文件。
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	// CreateTemp 建的是 0600；Windows 上权限位不生效，改名后再压一次。
	if err := tmp.Close(); err != nil {
		return err
	}
	// Go 的 os.Rename 在 Windows 上走 MoveFileEx(REPLACE_EXISTING)，可直接覆盖，
	// 不需要先删——先删反而制造「配置不存在」的窗口，改名失败时旧配置也没了。
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func parseRoot(raw string) (*url.URL, error) {
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Host == "" {
		return nil, errors.New("缺少主机名")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u, nil
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

func maskKey(key string) string {
	if len(key) <= 10 {
		return "kk_…"
	}
	return key[:7] + "…" + key[len(key)-6:]
}

func readPassword() (string, error) {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		raw, err := io.ReadAll(bufio.NewReader(os.Stdin))
		return strings.TrimSpace(string(raw)), err
	}
	// 终端下用 x/term 关闭回显（Windows/Unix 均支持，退出时恢复终端状态）；
	// 管道输入保持原样，供脚本化 provisioning 使用。
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}
