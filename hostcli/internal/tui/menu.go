package tui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

type MenuItem struct {
	Number int
	Label  string
	Action string
}

type SubmenuGroup struct {
	Key     string
	Title   string
	Entries []MenuItem
}

var MainMenuItems = []MenuItem{
	{1, "服务运行管理", "1"},
	{2, "访问入口管理", "2"},
	{3, "业务功能设置", "3"},
	{4, "健康检查诊断", "4"},
	{5, "实例维护备份", "5"},
	{0, "退出", "0"},
}

var SubmenuGroups = map[string]SubmenuGroup{
	"1": {
		Key:   "1",
		Title: "服务运行管理",
		Entries: []MenuItem{
			{1, "查看运行状态", "status"},
			{2, "查看运行日志", "logs"},
			{3, "服务启停管理", "control"},
		},
	},
	"2": {
		Key:   "2",
		Title: "访问入口管理",
		Entries: []MenuItem{
			{1, "HTTPS 代理与证书", "entry"},
			{2, "网关 WebDAV 凭据", "credentials"},
			{3, "外部 WebDAV 管理", "external_webdav"},
		},
	},
	"3": {
		Key:   "3",
		Title: "业务功能设置",
		Entries: []MenuItem{
			{1, "Bot 用户管理", "users"},
			{2, "监听转存管理", "listening"},
			{3, "文件分类设置", "classification"},
			{4, "异常通知设置", "notifications"},
		},
	},
	"4": {
		Key:   "4",
		Title: "健康检查诊断",
		Entries: []MenuItem{
			{1, "业务就绪检查", "readiness"},
			{2, "远端写入验收", "verify"},
			{3, "故障诊断报告", "diagnostics"},
		},
	},
	"5": {
		Key:   "5",
		Title: "实例维护备份",
		Entries: []MenuItem{
			{1, "实例安装升级", "upgrade"},
			{2, "数据备份管理", "backups"},
			{3, "卸载当前实例", "remove"},
		},
	},
}

type EntryInfo struct {
	IsLAN   bool
	BindIP  string
	Domain  string
	Email   string
}

type Backend interface {
	Discover() (*inspect.Instance, error)
	InstallDir() string
	ShowStatus(instance *inspect.Instance, out io.Writer) error
	ShowLogs(instance *inspect.Instance, out io.Writer) error
	ControlService(instance *inspect.Instance, verb, target string, out io.Writer) error
	ShowReadiness(instance *inspect.Instance, out io.Writer) error
	VerifyWebDAV(instance *inspect.Instance, out io.Writer) error
	CollectDiagnostics(instance *inspect.Instance) (string, error)
	SaveDiagnostics(report, dir string) (string, error)
	ReadEnv(instance *inspect.Instance) (map[string]string, error)
	EntryInfo(instance *inspect.Instance) (EntryInfo, error)
	ShowEntryStatus(instance *inspect.Instance, out io.Writer) error
	ConfigureHTTPS(instance *inspect.Instance, domain, email, confirmedNAT, expectedDomain string, out io.Writer) error
	RenewHTTPSDryRun(instance *inspect.Instance, domain string, out io.Writer) error
	ShowExternalWebDAV(instance *inspect.Instance, out io.Writer) error
	BotUsersRPC(instance *inspect.Instance, adminID int64, payload map[string]any) (UsersRecord, error)
	BotControlRPC(instance *inspect.Instance, adminID int64, payload map[string]any) (map[string]any, error)
	RunListenAccount(instance *inspect.Instance, action string, out io.Writer) error
	ListBackups(out io.Writer) error
	CreateInstanceSnapshot(instance *inspect.Instance, destDir string) (string, error)
	PreflightInstanceSnapshot(instance *inspect.Instance, archivePath, scratchDir string) (int, error)
	ProxyBackupAction(action string, keep int, planID string, out io.Writer) (string, error)
	UninstallPreview(purgeData, purgeBackups bool, out io.Writer) (string, error)
	UninstallExecute(purgeData, purgeBackups bool, planDigest string, out io.Writer) error
}

type UsersRecord struct {
	Schema   int          `json:"schema"`
	AdminID  int64        `json:"admin_id"`
	Revision string       `json:"revision"`
	Users    []FamilyUser `json:"users"`
}

type FamilyUser struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type App struct {
	Backend Backend
	In      *bufio.Scanner
	Out     io.Writer
	Version string
}

func NewApp(backend Backend, in io.Reader, out io.Writer, version string) *App {
	return &App{
		Backend: backend,
		In:      bufio.NewScanner(in),
		Out:     out,
		Version: version,
	}
}

func FormatNumberedMenu(items []MenuItem) string {
	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d. %s", item.Number, item.Label)
	}
	return b.String()
}

func FormatPageHeading(title, version string) string {
	if version != "" {
		return fmt.Sprintf("\n── %s（CloudDrive2 · v%s）──\n", title, version)
	}
	return fmt.Sprintf("\n── %s（CloudDrive2）──\n", title)
}

func (a *App) ask(prompt, defaultVal string) (string, error) {
	if defaultVal != "" {
		fmt.Fprintf(a.Out, "%s [%s]: ", prompt, defaultVal)
	} else {
		fmt.Fprintf(a.Out, "%s: ", prompt)
	}
	if !a.In.Scan() {
		if err := a.In.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	value := strings.TrimSpace(a.In.Text())
	if value == "" {
		return defaultVal, nil
	}
	return value, nil
}

func (a *App) choose(prompt string, valid []string, defaultVal string) (string, error) {
	for {
		val, err := a.ask(prompt, defaultVal)
		if err != nil {
			return "", err
		}
		if slices.Contains(valid, val) {
			return val, nil
		}
		fmt.Fprintln(a.Out, "请选择菜单中的序号")
	}
}

func (a *App) confirm(prompt string) (bool, error) {
	val, err := a.ask(prompt+" [y/N]", "n")
	if err != nil {
		return false, err
	}
	lower := strings.ToLower(val)
	return lower == "y" || lower == "yes", nil
}

func (a *App) returnToParent() error {
	fmt.Fprintln(a.Out, "\n"+FormatNumberedMenu([]MenuItem{{0, "返回上级", "0"}}))
	_, err := a.choose("选择操作", []string{"0"}, "0")
	return err
}

func (a *App) Run() int {
	for {
		instance, err := a.Backend.Discover()
		if err != nil {
			fmt.Fprintln(a.Out, "本地管理未启动："+err.Error())
			return 1
		}
		installState := "未安装"
		botState := "未运行"
		version := a.Version
		if instance != nil {
			installState = "已安装"
			if instance.Bot.State == "running" {
				botState = "运行中"
			}
			if instance.Version != "" {
				version = instance.Version
			}
		}
		fmt.Fprintf(a.Out, "\n── TGCloud 本地管理（Go 原生宿主模式）──\n\n安装状态：%s\nBot 状态：%s\n安装目录：%s\n管理工具（当前进程）：v%s\n\n",
			installState, botState, a.Backend.InstallDir(), version)
		if instance == nil {
			fmt.Fprintln(a.Out, "TGCloud 尚未安装。请在完整源码目录运行 sudo bash install.sh（内网使用 sudo bash install.sh --lan）。")
			return 0
		}
		fmt.Fprint(a.Out, FormatNumberedMenu(MainMenuItems)+"\n\n")
		choice, err := a.choose("选择操作", []string{"0", "1", "2", "3", "4", "5"}, "0")
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0
			}
			fmt.Fprintln(a.Out, "操作中断："+err.Error())
			return 130
		}
		if choice == "0" {
			return 0
		}
		exitCode, done := a.runGroup(choice, instance)
		if done {
			return exitCode
		}
	}
}

func (a *App) runGroup(groupKey string, instance *inspect.Instance) (int, bool) {
	group, ok := SubmenuGroups[groupKey]
	if !ok {
		return 0, false
	}
	items := append(slices.Clone(group.Entries), MenuItem{0, "返回上级", "0"})
	valid := make([]string, 0, len(items))
	actionByNum := make(map[string]string, len(group.Entries))
	for _, item := range items {
		k := strconv.Itoa(item.Number)
		valid = append(valid, k)
		if item.Number != 0 {
			actionByNum[k] = item.Action
		}
	}
	for {
		fmt.Fprintln(a.Out, FormatPageHeading(group.Title, instance.Version))
		fmt.Fprint(a.Out, FormatNumberedMenu(items)+"\n\n")
		selected, err := a.choose("选择操作", valid, "0")
		if err != nil {
			return 130, true
		}
		if selected == "0" {
			return 0, false
		}
		code, exitNow := a.dispatchAction(actionByNum[selected], instance)
		if exitNow {
			return code, true
		}
	}
}

func (a *App) dispatchAction(action string, instance *inspect.Instance) (int, bool) {
	var err error
	completed := false
	switch action {
	case "status", "logs", "readiness":
		err = a.runReadPage(action, instance)
	case "control":
		completed, err = a.handleControl(instance)
	case "entry":
		completed, err = a.handleEntry(instance)
	case "credentials":
		fmt.Fprintln(a.Out, FormatPageHeading("网关 WebDAV 凭据", instance.Version))
		err = a.handleCredentials(instance)
		completed = true
	case "external_webdav":
		fmt.Fprintln(a.Out, FormatPageHeading("外部 WebDAV 管理", instance.Version))
		err = a.Backend.ShowExternalWebDAV(instance, a.Out)
		completed = true
	case "users":
		err = a.handleUsers(instance)
	case "listening":
		err = a.handleListening(instance)
	case "classification":
		err = a.handleClassification(instance)
	case "notifications":
		err = a.handleNotifications(instance)
	case "verify":
		completed, err = a.handleVerify(instance)
	case "diagnostics":
		err = a.handleDiagnostics(instance)
		completed = true
	case "upgrade":
		fmt.Fprintln(a.Out, FormatPageHeading("实例安装升级", instance.Version))
		fmt.Fprintln(a.Out, "请在完整 TGCloud 源码目录运行 sudo bash install.sh（内网使用 sudo bash install.sh --lan）以校验源码并执行事务化升级。")
		completed = true
	case "backups":
		completed, err = a.handleBackups(instance)
	case "remove":
		removed, rmErr := a.handleRemove(instance)
		if rmErr != nil {
			fmt.Fprintln(a.Out, "操作未完成："+rmErr.Error())
			return 1, true
		}
		if removed {
			return 0, true
		}
		return 0, false
	default:
		err = errors.New("不支持的菜单操作")
	}
	if err != nil {
		if errors.Is(err, io.EOF) {
			return 0, true
		}
		fmt.Fprintln(a.Out, "操作未完成："+err.Error())
		if a.returnToParent() != nil {
			return 0, true
		}
		return 0, false
	}
	if completed {
		if a.returnToParent() != nil {
			return 0, true
		}
	}
	return 0, false
}
