package tui

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

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
	ShowEntryStatus(instance *inspect.Instance, out io.Writer) error
	ShowExternalWebDAV(instance *inspect.Instance, out io.Writer) error
	BotUsersRPC(instance *inspect.Instance, adminID int64, payload map[string]any) (UsersRecord, error)
	BotControlRPC(instance *inspect.Instance, adminID int64, payload map[string]any) (map[string]any, error)
	RunListenAccount(instance *inspect.Instance, action string, out io.Writer) error
	ListBackups(out io.Writer) error
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
		fmt.Fprintln(a.Out, FormatNumberedMenu(MainMenuItems)+"\n")
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
		fmt.Fprintln(a.Out, FormatNumberedMenu(items)+"\n")
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
		fmt.Fprintln(a.Out, FormatPageHeading("HTTPS 代理与证书", instance.Version))
		err = a.Backend.ShowEntryStatus(instance, a.Out)
		completed = true
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

func (a *App) runReadPage(action string, instance *inspect.Instance) error {
	refreshLabel := "重新检查"
	if action == "logs" {
		refreshLabel = "重新读取"
	}
	for {
		var err error
		switch action {
		case "status":
			fmt.Fprintln(a.Out, FormatPageHeading("查看运行状态", instance.Version))
			err = a.Backend.ShowStatus(instance, a.Out)
		case "logs":
			fmt.Fprintln(a.Out, FormatPageHeading("查看运行日志", instance.Version))
			err = a.Backend.ShowLogs(instance, a.Out)
		case "readiness":
			fmt.Fprintln(a.Out, FormatPageHeading("业务就绪检查", instance.Version))
			err = a.Backend.ShowReadiness(instance, a.Out)
		}
		if err != nil {
			fmt.Fprintln(a.Out, "检查未完成："+err.Error()+"；不标记为通过。")
		}
		fmt.Fprintln(a.Out, "\n"+FormatNumberedMenu([]MenuItem{{1, refreshLabel, "1"}, {0, "返回上级", "0"}}))
		sel, err := a.choose("选择操作", []string{"0", "1"}, "0")
		if err != nil || sel == "0" {
			return err
		}
		next, err := a.Backend.Discover()
		if err != nil || next == nil {
			fmt.Fprintln(a.Out, "当前实例已不可用，返回上级；不会自动安装或重建。")
			return nil
		}
		instance = next
	}
}

func (a *App) handleControl(instance *inspect.Instance) (bool, error) {
	fmt.Fprintln(a.Out, FormatPageHeading("服务启停管理", instance.Version))
	_ = a.Backend.ShowStatus(instance, a.Out)
	fmt.Fprintln(a.Out, "\n存储网关停止或重启会中断存储访问。\n"+FormatNumberedMenu([]MenuItem{
		{1, "启动 Bot", "1"}, {2, "停止 Bot", "2"}, {3, "重启 Bot", "3"},
		{4, "启动网关", "4"}, {5, "停止网关", "5"}, {6, "重启网关", "6"},
		{0, "返回上级", "0"},
	}))
	choice, err := a.choose("选择操作", []string{"0", "1", "2", "3", "4", "5", "6"}, "0")
	if err != nil || choice == "0" {
		return false, err
	}
	idx, _ := strconv.Atoi(choice)
	target := "bot"
	name := inspect.BotName
	if idx >= 4 {
		target = "gateway"
		name = inspect.GatewayName
	}
	verbs := []string{"start", "stop", "restart"}
	labels := []string{"启动", "停止", "重启"}
	verb := verbs[(idx-1)%3]
	label := labels[(idx-1)%3]
	ok, err := a.confirm(fmt.Sprintf("确认%s %s？进行中的任务或外部访问可能中断", label, name))
	if err != nil || !ok {
		return false, err
	}
	if err := a.Backend.ControlService(instance, verb, target, a.Out); err != nil {
		return false, err
	}
	fmt.Fprintln(a.Out, "容器操作已完成；健康状态请另选“查看运行状态”。")
	return true, nil
}

func (a *App) handleCredentials(instance *inspect.Instance) error {
	env, err := a.Backend.ReadEnv(instance)
	if err != nil {
		return err
	}
	creds, err := DecodeWebDAVCredentials(env)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Out, "网关地址："+creds.URL)
	fmt.Fprintln(a.Out, "用户名："+creds.Username)
	fmt.Fprintln(a.Out, "保存目录："+creds.TargetPath)
	ok, err := a.confirm("是否在当前私密终端临时显示 WebDAV 密码？（请确认无旁观/录屏）")
	if err != nil {
		return err
	}
	if ok {
		fmt.Fprintln(a.Out, "密码："+creds.Password)
	} else {
		fmt.Fprintln(a.Out, "密码：已隐藏")
	}
	return nil
}

type WebDAVCredentials struct {
	URL        string
	Username   string
	Password   string
	TargetPath string
}

func DecodeWebDAVCredentials(env map[string]string) (WebDAVCredentials, error) {
	decodeField := func(primary, legacy string) (string, error) {
		raw := env[primary]
		if raw == "" {
			raw = env[legacy]
		}
		if raw == "" {
			return "", nil
		}
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return "", errors.New("实例的 WebDAV 配置无法安全解析")
		}
		return string(decoded), nil
	}
	url, err := decodeField("WEBDAV_URL_B64", "CD2_WEBDAV_URL_B64")
	if err != nil {
		return WebDAVCredentials{}, err
	}
	user, err := decodeField("WEBDAV_USERNAME_B64", "CD2_WEBDAV_USERNAME_B64")
	if err != nil {
		return WebDAVCredentials{}, err
	}
	pass, err := decodeField("WEBDAV_PASSWORD_B64", "CD2_WEBDAV_PASSWORD_B64")
	if err != nil {
		return WebDAVCredentials{}, err
	}
	target, err := decodeField("WEBDAV_TARGET_PATH_B64", "CD2_TARGET_PATH_B64")
	if err != nil {
		return WebDAVCredentials{}, err
	}
	return WebDAVCredentials{URL: url, Username: user, Password: pass, TargetPath: target}, nil
}

func (a *App) readAdminID(instance *inspect.Instance) (int64, error) {
	env, err := a.Backend.ReadEnv(instance)
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(env["ALLOWED_USER_ID"]), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("无法解析实例管理员 ALLOWED_USER_ID")
	}
	return id, nil
}

var telegramIDRegex = regexp.MustCompile(`^[1-9][0-9]{0,14}$`)

func (a *App) handleUsers(instance *inspect.Instance) error {
	if instance.Bot.State != "running" {
		return errors.New("请先启动 Bot；不直接修改离线数据库")
	}
	adminID, err := a.readAdminID(instance)
	if err != nil {
		return err
	}
	for {
		rec, err := a.Backend.BotUsersRPC(instance, adminID, map[string]any{"action": "list"})
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Out, FormatPageHeading("Bot 用户管理", instance.Version))
		fmt.Fprintf(a.Out, "管理员 ID：%d\n", adminID)
		if len(rec.Users) == 0 {
			fmt.Fprintln(a.Out, "暂无家庭成员。")
		}
		for i, u := range rec.Users {
			state := "停用"
			if u.Enabled {
				state = "启用"
			}
			fmt.Fprintf(a.Out, "成员 %d：%s · %d · %s\n", i+1, u.Name, u.ID, state)
		}
		fmt.Fprintln(a.Out, "\n管理操作\n"+FormatNumberedMenu([]MenuItem{
			{1, "添加成员", "add"}, {2, "启用成员", "enable"},
			{3, "停用成员", "disable"}, {4, "移除授权", "remove"},
			{0, "返回上级", "0"},
		}))
		sel, err := a.choose("选择操作", []string{"0", "1", "2", "3", "4"}, "0")
		if err != nil || sel == "0" {
			return err
		}
		actionMap := map[string]string{"1": "add", "2": "enable", "3": "disable", "4": "remove"}
		action := actionMap[sel]
		var memberID int64
		var name string
		if action == "add" {
			rawID, err := a.ask("家人的 Telegram 用户 ID", "")
			if err != nil {
				return err
			}
			if !telegramIDRegex.MatchString(rawID) {
				fmt.Fprintln(a.Out, "请输入有效的 Telegram 数字用户 ID")
				continue
			}
			memberID, _ = strconv.ParseInt(rawID, 10, 64)
			name, err = a.ask("备注名称", "家庭成员")
			if err != nil {
				return err
			}
		} else {
			if len(rec.Users) == 0 {
				fmt.Fprintln(a.Out, "没有已登记的家人。")
				continue
			}
			validIdx := make([]string, 0, len(rec.Users)+1)
			for i := 0; i <= len(rec.Users); i++ {
				validIdx = append(validIdx, strconv.Itoa(i))
			}
			idxStr, err := a.choose("选择上方成员序号（0 返回）", validIdx, "0")
			if err != nil || idxStr == "0" {
				if err != nil {
					return err
				}
				continue
			}
			idx, _ := strconv.Atoi(idxStr)
			memberID = rec.Users[idx-1].ID
			name = rec.Users[idx-1].Name
		}
		if memberID == adminID {
			fmt.Fprintln(a.Out, "不能通过家庭成员管理修改管理员。")
			continue
		}
		ok, err := a.confirm("确认本次授权变更？")
		if err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		if _, err := a.Backend.BotUsersRPC(instance, adminID, map[string]any{
			"action": action, "id": memberID, "name": name, "revision": rec.Revision,
		}); err != nil {
			return err
		}
		fmt.Fprintln(a.Out, "家庭授权已更新；无需重启 Bot。")
	}
}

func (a *App) handleListening(instance *inspect.Instance) error {
	if instance.Bot.State != "running" {
		return errors.New("请先启动 Bot")
	}
	for {
		fmt.Fprintln(a.Out, FormatPageHeading("监听转存管理", instance.Version))
		fmt.Fprintln(a.Out, "登录只保存个人账号授权；在 Bot 监听管理中启用。停用保留来源、任务和 Session。")
		fmt.Fprintln(a.Out, FormatNumberedMenu([]MenuItem{
			{1, "登录监听账号", "login"}, {2, "查看监听状态", "status"},
			{3, "停用监听功能", "disable"}, {0, "返回上级", "0"},
		}))
		sel, err := a.choose("选择操作", []string{"0", "1", "2", "3"}, "0")
		if err != nil || sel == "0" {
			return err
		}
		act := map[string]string{"1": "login", "2": "status", "3": "disable"}[sel]
		if err := a.Backend.RunListenAccount(instance, act, a.Out); err != nil {
			fmt.Fprintln(a.Out, "监听操作未完成："+err.Error())
		}
		if err := a.returnToParent(); err != nil {
			return err
		}
	}
}

func (a *App) handleClassification(instance *inspect.Instance) error {
	if instance.Bot.State != "running" {
		return errors.New("请先启动支持新协议的 Bot")
	}
	adminID, err := a.readAdminID(instance)
	if err != nil {
		return err
	}
	modes := []struct{ key, label string }{
		{"off", "不作自动分类"},
		{"type", "仅按类型分类"},
		{"year", "仅按年份分类"},
		{"month", "仅按年月分类"},
		{"type_month", "按类型和年月"},
	}
	for {
		res, err := a.Backend.BotControlRPC(instance, adminID, map[string]any{"action": "settings-read"})
		if err != nil {
			return err
		}
		settings, _ := res["settings"].(map[string]any)
		currMode, _ := settings["classification"].(string)
		rev, _ := settings["revision"].(string)
		currLabel := currMode
		for _, m := range modes {
			if m.key == currMode {
				currLabel = m.label
			}
		}
		fmt.Fprintln(a.Out, FormatPageHeading("文件分类设置", instance.Version))
		fmt.Fprintln(a.Out, "当前规则："+currLabel)
		fmt.Fprintln(a.Out, "分类目录位于现有 Bot 保存目录下；只影响新任务，不移动历史文件。\n")
		items := make([]MenuItem, 0, len(modes)+1)
		for i, m := range modes {
			items = append(items, MenuItem{i + 1, m.label, m.key})
		}
		items = append(items, MenuItem{0, "返回上级", "0"})
		fmt.Fprintln(a.Out, FormatNumberedMenu(items))
		sel, err := a.choose("选择规则", []string{"0", "1", "2", "3", "4", "5"}, "0")
		if err != nil || sel == "0" {
			return err
		}
		idx, _ := strconv.Atoi(sel)
		ok, err := a.confirm("确认保存？不重启 Bot；分类不整理历史文件")
		if err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		if _, err := a.Backend.BotControlRPC(instance, adminID, map[string]any{
			"action": "settings-write", "revision": rev,
			"updates": map[string]any{"classification": modes[idx-1].key},
		}); err != nil {
			return err
		}
		fmt.Fprintln(a.Out, "设置已保存。")
	}
}

func (a *App) handleNotifications(instance *inspect.Instance) error {
	if instance.Bot.State != "running" {
		return errors.New("请先启动支持新协议的 Bot")
	}
	adminID, err := a.readAdminID(instance)
	if err != nil {
		return err
	}
	for {
		res, err := a.Backend.BotControlRPC(instance, adminID, map[string]any{"action": "settings-read"})
		if err != nil {
			return err
		}
		settings, _ := res["settings"].(map[string]any)
		notif, _ := settings["notifications"].(bool)
		recov, _ := settings["recovery_notifications"].(bool)
		rev, _ := settings["revision"].(string)
		stateText := func(b bool) string {
			if b {
				return "开启"
			}
			return "关闭"
		}
		toggleLabel := func(b bool, name string) string {
			if b {
				return "关闭" + name
			}
			return "开启" + name
		}
		fmt.Fprintln(a.Out, FormatPageHeading("异常通知设置", instance.Version))
		fmt.Fprintf(a.Out, "异常通知：%s\n恢复通知：%s\n\n", stateText(notif), stateText(recov))
		fmt.Fprintln(a.Out, FormatNumberedMenu([]MenuItem{
			{1, toggleLabel(notif, "异常通知"), "1"},
			{2, toggleLabel(recov, "恢复通知"), "2"},
			{3, "修改磁盘阈值", "3"},
			{0, "返回上级", "0"},
		}))
		sel, err := a.choose("选择操作", []string{"0", "1", "2", "3"}, "0")
		if err != nil || sel == "0" {
			return err
		}
		updates := map[string]any{}
		switch sel {
		case "1":
			updates["notifications"] = !notif
		case "2":
			updates["recovery_notifications"] = !recov
		case "3":
			gbStr, err := a.ask("提醒阈值（单位 GB；0 跟随原磁盘安全线）", "0")
			if err != nil {
				return err
			}
			gb, err := strconv.Atoi(gbStr)
			if err != nil || gb < 0 || gb > 100000 {
				fmt.Fprintln(a.Out, "请输入 0～100000 的整数")
				continue
			}
			updates["disk_threshold_bytes"] = int64(gb) * 1024 * 1024 * 1024
		}
		ok, err := a.confirm("确认保存？不重启 Bot；通知只发给管理员")
		if err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		if _, err := a.Backend.BotControlRPC(instance, adminID, map[string]any{
			"action": "settings-write", "revision": rev, "updates": updates,
		}); err != nil {
			return err
		}
		fmt.Fprintln(a.Out, "设置已保存。")
	}
}

func (a *App) handleVerify(instance *inspect.Instance) (bool, error) {
	fmt.Fprintln(a.Out, FormatPageHeading("WebDAV 验收", instance.Version))
	fmt.Fprintln(a.Out, "验收将创建并清理随机测试文件；不删除已有文件，大小复验不等于云盘官方最终入库。")
	if instance.Bot.State != "running" {
		return false, errors.New("Bot 未运行，请先启动后验收")
	}
	ok, err := a.confirm("运行 WebDAV 写入验收？仅创建并清理随机测试文件")
	if err != nil || !ok {
		return false, err
	}
	if err := a.Backend.VerifyWebDAV(instance, a.Out); err != nil {
		return false, err
	}
	fmt.Fprintln(a.Out, "WebDAV 写入与远端大小复验通过；云盘官方端最终完成仍需自行确认。")
	return true, nil
}

func (a *App) handleDiagnostics(instance *inspect.Instance) error {
	fmt.Fprintln(a.Out, FormatPageHeading("故障诊断报告", instance.Version))
	fmt.Fprintln(a.Out, "正在生成只读诊断报告，请稍候……")
	report, err := a.Backend.CollectDiagnostics(instance)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Out, report)
	ok, err := a.confirm("保存已脱敏报告到本机私密目录？不会自动上传")
	if err != nil || !ok {
		return err
	}
	dir, err := a.ask("报告目录（绝对路径）", "/opt/tgcloud-reports")
	if err != nil {
		return err
	}
	saved, err := a.Backend.SaveDiagnostics(report, dir)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Out, "报告已保存："+saved)
	return nil
}

func (a *App) handleBackups(instance *inspect.Instance) (bool, error) {
	fmt.Fprintln(a.Out, FormatPageHeading("数据备份管理", instance.Version))
	fmt.Fprintln(a.Out, "实例备份\n"+FormatNumberedMenu([]MenuItem{{1, "查看历史备份", "1"}})+
		"\n\n共享代理\n"+FormatNumberedMenu([]MenuItem{
		{2, "创建代理备份", "2"}, {3, "查看代理备份", "3"},
		{4, "校验代理备份", "4"}, {5, "清理代理备份", "5"},
		{0, "返回上级", "0"},
	}))
	sel, err := a.choose("选择操作", []string{"0", "1", "2", "3", "4", "5"}, "0")
	if err != nil || sel == "0" {
		return false, err
	}
	switch sel {
	case "1":
		return true, a.Backend.ListBackups(a.Out)
	case "2":
		ok, err := a.confirm("确认在 VPS 私密目录创建共享代理备份？含证书私钥，不要公开上传")
		if err != nil || !ok {
			return false, err
		}
		_, err = a.Backend.ProxyBackupAction("create", 5, "", a.Out)
		return true, err
	case "3":
		_, err := a.Backend.ProxyBackupAction("list", 5, "", a.Out)
		return true, err
	case "4":
		ok, err := a.confirm("确认在隔离私密目录校验最新共享代理备份？不会覆盖运行数据")
		if err != nil || !ok {
			return false, err
		}
		_, err = a.Backend.ProxyBackupAction("verify", 5, "", a.Out)
		return true, err
	case "5":
		planID, err := a.Backend.ProxyBackupAction("prune-preview", 5, "", a.Out)
		if err != nil || planID == "" {
			return true, err
		}
		ok, err := a.confirm("确认仅删除上述代理备份？保留最近 5 份有效备份；删除不可撤销")
		if err != nil || !ok {
			return false, err
		}
		_, err = a.Backend.ProxyBackupAction("prune", 5, planID, a.Out)
		return true, err
	}
	return false, nil
}

func (a *App) handleRemove(instance *inspect.Instance) (bool, error) {
	fmt.Fprintln(a.Out, FormatPageHeading("卸载当前实例", instance.Version))
	if a.Backend.InstallDir() != inspect.InstallDirectory {
		return false, errors.New("现有卸载器只支持标准目录；自定义目录不自动卸载，避免清理错误实例")
	}
	planDigest, err := a.Backend.UninstallPreview(true, true, a.Out)
	if err != nil {
		return false, err
	}
	ok, err := a.confirm("是否执行以上 TGCloud 资源卸载？")
	if err != nil || !ok {
		fmt.Fprintln(a.Out, "TGCLOUD_UNINSTALL_RESULT=CANCELLED；未执行卸载")
		return false, err
	}
	fmt.Fprintln(a.Out, "危险：删除 .env、Session、SQLite、下载、日志、云网关授权配置及历史备份后不能自动恢复。")
	token, err := a.ask("请输入 DELETE ALL clouddrive2 确认删除上述本地数据", "")
	if err != nil {
		return false, err
	}
	if token != "DELETE ALL clouddrive2" {
		fmt.Fprintln(a.Out, "TGCLOUD_UNINSTALL_RESULT=CANCELLED；删除确认不匹配，未执行卸载")
		return false, nil
	}
	if err := a.Backend.UninstallExecute(true, true, planDigest, a.Out); err != nil {
		return false, err
	}
	return true, nil
}

func RenderDiagnostics(instance *inspect.Instance, dockerVer, composeVer, botSnapshotJSON string, logRotation map[string]string, entrySummary string) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# TGCloud 故障诊断报告")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "仅只读检查；不包含原始日志、文件名、私人路径或凭据。")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "生成时间（UTC）："+time.Now().UTC().Format("2006-01-02 15:04:05"))
	fmt.Fprintln(&b, "实例：CloudDrive2")
	fmt.Fprintln(&b, "Docker："+fallbackText(dockerVer, "未检查"))
	fmt.Fprintln(&b, "Compose："+fallbackText(composeVer, "未检查"))
	fmt.Fprintf(&b, "Bot 容器：%s / %s\n", fallbackText(instance.Bot.State, "未检查"), fallbackText(instance.Bot.Health, "none"))
	fmt.Fprintf(&b, "网关容器：%s / %s\n", fallbackText(instance.Gateway.State, "未检查"), fallbackText(instance.Gateway.Health, "none"))

	var snap struct {
		Schema        int               `json:"schema"`
		Edition       string            `json:"edition"`
		Version       string            `json:"version"`
		Python        string            `json:"python"`
		Configuration string            `json:"configuration"`
		Checks        map[string]string `json:"checks"`
		Failures      map[string]int64  `json:"failures"`
	}
	if botSnapshotJSON != "" && json.Unmarshal([]byte(botSnapshotJSON), &snap) == nil &&
		snap.Schema == 1 && snap.Edition == "clouddrive2" {
		fmt.Fprintln(&b, "实际 Bot 版本："+snap.Version)
		fmt.Fprintln(&b, "Bot Python："+snap.Python)
		cfgLabel := "不完整"
		if snap.Configuration == "present" {
			cfgLabel = "存在（不代表凭据有效）"
		}
		fmt.Fprintln(&b, "配置必填项："+cfgLabel)
		for _, item := range []struct{ key, label string }{
			{"heartbeat", "Bot 心跳"}, {"telegram", "Telegram 连接"},
			{"destination", "WebDAV 目标"}, {"queue", "队列调度"}, {"capacity", "下载容量"},
		} {
			fmt.Fprintf(&b, "%s：%s\n", item.label, fallbackText(snap.Checks[item.key], "未核实"))
		}
		fmt.Fprintln(&b, "\n## 最近7天当前失败任务分类（非原始日志）")
		if len(snap.Failures) == 0 {
			fmt.Fprintln(&b, "当前无此范围内的失败任务；不代表从未发生错误。")
		} else {
			keys := make([]string, 0, len(snap.Failures))
			for k := range snap.Failures {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				fmt.Fprintf(&b, "%s：%d\n", k, snap.Failures[k])
			}
		}
	} else {
		fmt.Fprintln(&b, "Bot 诊断快照：未检查（未运行、旧版本或响应不受支持）")
	}
	fmt.Fprintln(&b, "\n## 容器日志轮转（实际配置）")
	logStateLabels := map[string]string{
		"ok":      "已生效（每份不超过10m，最多3份）",
		"pending": "未采用项目轮转限额（旧容器需确认更新）",
		"custom":  "已有限额，但与项目设置不同；请核对",
		"other":   "使用其他日志驱动；需核对其保留策略",
		"unknown": "未核实（未使用、不可读或归属未确认）",
	}
	for _, label := range []string{"Bot", "网关", "共享 Nginx", "Certbot"} {
		st := logStateLabels[logRotation[label]]
		if st == "" {
			st = logStateLabels["unknown"]
		}
		fmt.Fprintf(&b, "%s：%s\n", label, st)
	}
	fmt.Fprintln(&b, "\n管理入口："+fallbackText(entrySummary, "未核实"))
	fmt.Fprintln(&b, "未执行 WebDAV 写入、服务重启或软件安装。")
	return b.String()
}

func fallbackText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
