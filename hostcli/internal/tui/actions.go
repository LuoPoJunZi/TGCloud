package tui

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/entry"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

var telegramIDRegex = regexp.MustCompile(`^[1-9][0-9]{0,14}$`)

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

func (a *App) handleEntry(instance *inspect.Instance) (bool, error) {
	info, err := a.Backend.EntryInfo(instance)
	if err != nil {
		return false, err
	}
	if info.IsLAN {
		fmt.Fprintln(a.Out, FormatPageHeading("HTTPS 代理与证书", instance.Version))
		fmt.Fprintf(a.Out, "访问模式：内网 HTTP\n管理地址：http://%s:19798\n当前是内网 HTTP 模式，不申请证书或隐式切换为公网模式。\n", info.BindIP)
		return true, nil
	}
	fmt.Fprintln(a.Out, FormatPageHeading("HTTPS 代理与证书", instance.Version))
	fmt.Fprintln(a.Out, "只管理入口，不升级或重启 Bot／网关。")
	fmt.Fprintln(a.Out)
	fmt.Fprintln(a.Out, FormatNumberedMenu([]MenuItem{
		{1, "检查入口状态", "1"},
		{2, "配置修复入口", "2"},
		{3, "证书续期演练", "3"},
		{0, "返回上级", "0"},
	}))
	sel, err := a.choose("选择操作", []string{"0", "1", "2", "3"}, "1")
	if err != nil || sel == "0" {
		return false, err
	}
	switch sel {
	case "1":
		fmt.Fprintln(a.Out, "正在检查 HTTPS，请稍候……")
		return true, a.Backend.ShowEntryStatus(instance, a.Out)
	case "2":
		domain := info.Domain
		if domain == "" {
			domain, err = a.ask("管理页域名（A/AAAA 直指本机；Cloudflare 仅 DNS）", "")
			if err != nil {
				return false, err
			}
		}
		if !entry.ValidHTTPSDomain(domain) {
			return false, errors.New("域名格式不正确；请填写完整的 ASCII/Punycode FQDN")
		}
		email := info.Email
		if email == "" {
			email, err = a.ask("Let's Encrypt 邮箱（可留空）", "")
			if err != nil {
				return false, err
			}
		}
		confirmedNAT, err := a.ask("公网 NAT IPv4（非 NAT 环境直接回车留空）", "")
		if err != nil {
			return false, err
		}
		ok, err := a.confirm("仅配置/修复 " + domain + " 的 HTTPS？可能短暂中断共享管理入口，不升级或重启 Bot/网关")
		if err != nil || !ok {
			return false, err
		}
		if err := a.Backend.ConfigureHTTPS(instance, domain, email, confirmedNAT, info.Domain, a.Out); err != nil {
			return false, err
		}
		fmt.Fprintln(a.Out, "HTTPS 配置／修复已完成。")
		return true, nil
	case "3":
		if info.Domain == "" {
			return false, errors.New("尚未配置 HTTPS 域名，无法执行续期演练")
		}
		fmt.Fprintln(a.Out, "续期演练使用 ACME 测试流程，不代表正式证书已经续签。")
		ok, err := a.confirm("确认执行续期 dry-run？需要外网和 ACME，不替换正式证书")
		if err != nil || !ok {
			return false, err
		}
		if err := a.Backend.RenewHTTPSDryRun(instance, info.Domain, a.Out); err != nil {
			return false, err
		}
		fmt.Fprintln(a.Out, "续期 dry-run 通过；没有替换正式证书，也不代表长期自动续期已经验收。")
		return true, nil
	}
	return false, nil
}

func (a *App) handleCredentials(instance *inspect.Instance) error {
	if a.PrivateOut == nil {
		return errors.New("凭据只能在当前交互终端显示")
	}
	env, err := a.Backend.ReadEnv(instance)
	if err != nil {
		return err
	}
	creds, err := DecodeWebDAVCredentials(env)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Out, "网关地址："+creds.URL)
	fmt.Fprintln(a.Out, "保存目录："+creds.TargetPath)
	fmt.Fprintln(a.Out, "TGCloud：逐项确认后仅向当前私有终端显示已保存凭据；默认不显示。")
	for _, field := range []struct{ label, value string }{
		{"WebDAV 用户名", creds.Username},
		{"WebDAV 密码", creds.Password},
	} {
		ok, err := a.confirm("是否仅在当前私密终端显示" + field.label + "？请确保没有旁观/录屏")
		if err != nil {
			return err
		}
		if ok {
			fmt.Fprintln(a.PrivateOut, field.label+"："+field.value)
		} else {
			fmt.Fprintln(a.Out, field.label+"：已隐藏")
		}
	}
	return nil
}

func (a *App) handleExternalWebDAV(instance *inspect.Instance) (bool, error) {
	fmt.Fprintln(a.Out, FormatPageHeading("外部 WebDAV 管理", instance.Version))
	if err := a.Backend.ShowExternalWebDAV(instance, a.Out); err != nil {
		return false, err
	}
	fmt.Fprintln(a.Out, "\n"+FormatNumberedMenu([]MenuItem{
		{1, "打开完整外部 WebDAV 管理菜单", "1"},
		{0, "返回上级", "0"},
	}))
	sel, err := a.choose("选择操作", []string{"0", "1"}, "1")
	if err != nil || sel == "0" {
		return false, err
	}
	if err := a.Backend.ManageExternalWebDAV(instance, a.Out); err != nil {
		return false, err
	}
	return true, nil
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
			{5, "查看配额", "quota-view"}, {6, "设置配额", "quota-set"},
			{7, "取消限额", "quota-clear"}, {0, "返回上级", "0"},
		}))
		sel, err := a.choose("选择操作", []string{"0", "1", "2", "3", "4", "5", "6", "7"}, "0")
		if err != nil || sel == "0" {
			return err
		}
		actionMap := map[string]string{
			"1": "add", "2": "enable", "3": "disable", "4": "remove",
			"5": "quota-view", "6": "quota-set", "7": "quota-clear",
		}
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
		if strings.HasPrefix(action, "quota-") {
			if err := a.handleMemberQuota(instance, adminID, rec, memberID, name, strings.TrimPrefix(action, "quota-")); err != nil {
				return err
			}
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

func asInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case float64:
		if v < 0 {
			return 0, false
		}
		return int64(v), true
	case int64:
		return v, v >= 0
	case int:
		return int64(v), v >= 0
	default:
		return 0, false
	}
}

func (a *App) handleMemberQuota(instance *inspect.Instance, adminID int64, rec UsersRecord, memberID int64, name, mode string) error {
	resp, err := a.Backend.BotControlRPC(instance, adminID, map[string]any{"action": "quota-read", "user_id": memberID})
	if err != nil {
		return err
	}
	usage, ok := resp["usage"].(map[string]any)
	if !ok {
		return errors.New("配额协议无效")
	}
	dailyBytes, ok1 := asInt64(usage["daily_bytes"])
	maxQueued, ok2 := asInt64(usage["max_queued"])
	maxActive, ok3 := asInt64(usage["max_active"])
	completedBytes, ok4 := asInt64(usage["completed_bytes"])
	reservedBytes, ok5 := asInt64(usage["reserved_bytes"])
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
		return errors.New("配额协议无效")
	}
	formatLimit := func(v int64, gb bool) string {
		if v == 0 {
			return "不限"
		}
		if gb {
			return fmt.Sprintf("%g GB", float64(v)/(1024*1024*1024))
		}
		return strconv.FormatInt(v, 10)
	}
	fmt.Fprintf(a.Out, "\n── 成员配额（%s · %d）──\n每日额度：%s\n最大排队：%s\n最大同时传输：%s\n今日已完成：%.2f GB\n未完成预留：%.2f GB\n",
		name, memberID,
		formatLimit(dailyBytes, true),
		formatLimit(maxQueued, false),
		formatLimit(maxActive, false),
		float64(completedBytes)/(1024*1024*1024),
		float64(reservedBytes)/(1024*1024*1024),
	)
	if mode == "view" {
		return nil
	}
	values := map[string]int64{"daily_bytes": 0, "max_queued": 0, "max_active": 0}
	if mode == "set" {
		askInt := func(prompt string) (int64, error) {
			raw, err := a.ask(prompt, "0")
			if err != nil {
				return 0, err
			}
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n < 0 || n > 100000 {
				return 0, errors.New("请输入 0～100000 的整数；0 表示不限制")
			}
			return n, nil
		}
		gb, err := askInt("每日 GB（0 不限）")
		if err != nil {
			return err
		}
		q, err := askInt("最大排队（0 不限）")
		if err != nil {
			return err
		}
		act, err := askInt("最大同时传输（0 不限）")
		if err != nil {
			return err
		}
		values["daily_bytes"] = gb * 1024 * 1024 * 1024
		values["max_queued"] = q
		values["max_active"] = act
	}
	confirmed, err := a.confirm("确认配额变更？不取消正在执行的任务，不删除历史记录或文件")
	if err != nil || !confirmed {
		return err
	}
	if _, err := a.Backend.BotControlRPC(instance, adminID, map[string]any{
		"action":   "quota-write",
		"user_id":  memberID,
		"revision": rec.Revision,
		"expected": map[string]int64{"daily_bytes": dailyBytes, "max_queued": maxQueued, "max_active": maxActive},
		"values":   values,
	}); err != nil {
		return err
	}
	fmt.Fprintln(a.Out, "配额已更新；家人发送 /quota 查看自己的用量。")
	return nil
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
		fmt.Fprintln(a.Out, "分类目录位于现有 Bot 保存目录下；只影响新任务，不移动历史文件。")
		fmt.Fprintln(a.Out)
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
	fmt.Fprintln(a.Out, "实例备份\n"+FormatNumberedMenu([]MenuItem{
		{1, "查看历史备份", "1"}, {2, "创建实例快照", "2"}, {3, "校验实例快照", "3"},
	})+"\n\n共享代理\n"+FormatNumberedMenu([]MenuItem{
		{4, "创建代理备份", "4"}, {5, "查看代理备份", "5"},
		{6, "校验代理备份", "6"}, {7, "清理代理备份", "7"},
		{0, "返回上级", "0"},
	}))
	sel, err := a.choose("选择操作", []string{"0", "1", "2", "3", "4", "5", "6", "7"}, "0")
	if err != nil || sel == "0" {
		return false, err
	}
	switch sel {
	case "1":
		return true, a.Backend.ListBackups(a.Out)
	case "2":
		destDir, err := a.ask("私密备份目录", "/opt/tgcloud-snapshots/clouddrive2")
		if err != nil {
			return false, err
		}
		fmt.Fprintln(a.Out, "备份配置、业务数据库、Session 和网关配置；不备份云端、downloads、日志、共享代理或附加功能状态。")
		ok, err := a.confirm("确认短暂停止所选 Bot 与网关创建快照？会暂时中断该网关的管理和外部 WebDAV 读取")
		if err != nil || !ok {
			return false, err
		}
		archive, err := a.Backend.CreateInstanceSnapshot(instance, destDir)
		if err != nil {
			return false, err
		}
		fmt.Fprintln(a.Out, "实例快照已核验："+archive+"；原服务已恢复运行状态，含敏感信息，请私密离机保存。")
		return true, nil
	case "3":
		scratchDir, err := a.ask("私密隔离校验目录", "/opt/tgcloud-snapshots/clouddrive2")
		if err != nil {
			return false, err
		}
		archivePath, err := a.ask("实例快照归档绝对路径", "")
		if err != nil {
			return false, err
		}
		ok, err := a.confirm("确认在上述私密目录隔离解包并检查？不覆盖运行数据，不启动第二个 Bot")
		if err != nil || !ok {
			return false, err
		}
		count, err := a.Backend.PreflightInstanceSnapshot(instance, archivePath, scratchDir)
		if err != nil {
			return false, err
		}
		fmt.Fprintf(a.Out, "实例快照结构／数据库预检通过：%d 文件；不代表运行恢复或全部网关授权已验收。\n", count)
		return true, nil
	case "4":
		ok, err := a.confirm("确认在 VPS 私密目录创建共享代理备份？含证书私钥，不要公开上传")
		if err != nil || !ok {
			return false, err
		}
		_, err = a.Backend.ProxyBackupAction("create", 5, "", a.Out)
		return true, err
	case "5":
		_, err := a.Backend.ProxyBackupAction("list", 5, "", a.Out)
		return true, err
	case "6":
		ok, err := a.confirm("确认在隔离私密目录校验最新共享代理备份？不会覆盖运行数据")
		if err != nil || !ok {
			return false, err
		}
		_, err = a.Backend.ProxyBackupAction("verify", 5, "", a.Out)
		return true, err
	case "7":
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
