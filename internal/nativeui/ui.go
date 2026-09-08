package nativeui

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"image/color"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	coreapp "socksrevivepc/internal/app"
	"socksrevivepc/internal/config"
	"socksrevivepc/internal/crash"
	"socksrevivepc/internal/updater"
	"socksrevivepc/internal/xraylink"
)

//go:embed assets/logo.png
var logoPNG []byte

type UI struct {
	core *coreapp.App
	app  fyne.App
	win  fyne.Window

	profiles []config.Profile
	current  config.Profile
	lastLog  int64
	logText  string

	profileList *widget.List
	status      *widget.Label
	subStatus   *widget.Label
	connectBtn  *widget.Button
	killSwitch  *widget.Check
	modeContent *fyne.Container
	logView     *widget.Label
	logScroller *container.Scroll

	name      *widget.Entry
	mode      *widget.Select
	sshHost   *widget.Entry
	sshPort   *widget.Entry
	sshUser   *widget.Entry
	sshPass   *widget.Entry
	keepAlive *widget.Entry
	handshake *widget.Entry

	proxyHost *widget.Entry
	proxyPort *widget.Entry

	payloadText  *widget.Entry
	payloadWait  *widget.Check
	payloadAny   *widget.Check
	payloadReply *widget.Entry
	payloadSplit *widget.Entry

	tlsHost     *widget.Entry
	tlsPort     *widget.Entry
	tlsSNI      *widget.Entry
	tlsInsecure *widget.Check

	dnsttEmbedded     *widget.Check
	dnsttResolverType *widget.Select
	dnsttResolverAddr *widget.Entry
	dnsttDomain       *widget.Entry
	dnsttPublicKey    *widget.Entry
	dnsttUTLS         *widget.Entry
	dnsttExe          *widget.Entry
	dnsttArgs         *widget.Entry
	dnsttLocalHost    *widget.Entry
	dnsttLocalPort    *widget.Entry

	xrayExe       *widget.Entry
	xrayArgs      *widget.Entry
	xrayConfig    *widget.Entry
	xraySocksHost *widget.Entry
	xraySocksPort *widget.Entry
	xrayJSON      *widget.Entry

	openvpnExe      *widget.Entry
	openvpnConfig   *widget.Entry
	openvpnUser     *widget.Entry
	openvpnPass     *widget.Entry
	openvpnAuthFile *widget.Entry
	openvpnVerb     *widget.Entry

	udpgwEnabled  *widget.Check
	udpgwProtocol *widget.Select
	udpgwHost     *widget.Entry
	udpgwPort     *widget.Entry

	routeMode        *widget.Select
	socksInfo        *widget.Label
	socksCopy        *widget.Button
	syncingRoute     bool
	proxifierExe     *widget.Entry
	proxifierLicName *widget.Entry
	proxifierLicKey  *widget.Entry

	localSocksHost *widget.Entry
	localSocksPort *widget.Entry

	dnsServers *widget.Entry

	reconnectEnabled *widget.Check
	reconnectDelay   *widget.Entry
	reconnectMax     *widget.Entry
	reconnectCheck   *widget.Entry

	tunEnabled       *widget.Check
	tunDevice        *widget.Entry
	tunIface         *widget.Entry
	tunMTU           *widget.Entry
	tunCIDR          *widget.Entry
	tunDNS           *widget.Entry
	tunRoute         *widget.Check
	tunIPv6Enabled   *widget.Check
	tunIPv6CIDR      *widget.Entry
	tunIPv6DNS       *widget.Entry
	tunBlockIPv6Leak *widget.Check
}

func Run(core *coreapp.App) {
	a := fyneapp.NewWithID("com.sakr.tun")
	a.Settings().SetTheme(theme.DarkTheme())
	logo := fyne.NewStaticResource("logo.png", logoPNG)
	a.SetIcon(logo)
	w := a.NewWindow("SAKR TUN")
	w.SetIcon(logo)
	w.Resize(fyne.NewSize(1220, 780))
	// Open the app using the full normal screen area (maximized, taskbar
	// visible). On Windows this is done through Win32 after the window exists;
	// on other platforms the default size above is kept.
	maximizeWindow(w.Title())

	u := &UI{core: core, app: a, win: w}
	u.build()
	u.loadProfiles()
	if u.core.Settings.KillSwitch {
		u.killSwitch.SetChecked(true)
		// Re-apply the block on startup (idempotent) so the internet stays off
		// outside the VPN even if the routes were restored while the app was
		// closed.
		startupProfile := u.current
		crash.Go(u.core.Root, func() {
			if err := u.core.Engine.SetKillSwitch(true, startupProfile); err != nil {
				fyne.Do(func() { dialog.ShowError(err, u.win) })
			}
		})
	}
	u.refreshStatus()
	u.startLogPoller()

	w.SetCloseIntercept(func() {
		u.core.Engine.Stop()
		w.Close()
	})
	// Do not call w.CenterOnScreen(): it makes Fyne query the monitor video
	// mode, and on disconnected RDP sessions there is no monitor, which
	// panics (nil video mode) and closes the app. The Win32 maximize below
	// already places the window on the normal screen area.
	w.ShowAndRun()
}

func (u *UI) build() {
	u.profileList = widget.NewList(
		func() int { return len(u.profiles) },
		func() fyne.CanvasObject {
			name := widget.NewLabel("Profile")
			name.TextStyle = fyne.TextStyle{Bold: true}
			name.Truncation = fyne.TextTruncateEllipsis
			mode := widget.NewLabel("Tunnel mode")
			mode.Truncation = fyne.TextTruncateEllipsis
			return container.NewVBox(name, mode)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < 0 || id >= len(u.profiles) {
				return
			}
			p := u.profiles[id]
			box := obj.(*fyne.Container)
			name := box.Objects[0].(*widget.Label)
			mode := box.Objects[1].(*widget.Label)
			prefix := ""
			if p.ID == u.current.ID {
				prefix = "● "
			}
			name.SetText(prefix + p.Name)
			mode.SetText(modeLabel(p.Mode))
		},
	)
	u.profileList.OnSelected = func(id widget.ListItemID) {
		if id < 0 || id >= len(u.profiles) {
			return
		}
		p, err := u.core.Store.Load(u.profiles[id].ID)
		if err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		u.setProfile(p)
	}

	u.status = widget.NewLabel("Disconnected")
	u.status.TextStyle = fyne.TextStyle{Bold: true}
	u.subStatus = widget.NewLabel("No active tunnel")
	u.subStatus.Truncation = fyne.TextTruncateEllipsis
	u.connectBtn = widget.NewButtonWithIcon("Connect", theme.MediaPlayIcon(), u.toggleConnection)

	side := fixedWidth(315, sidebarPanel(u.sidebar()))
	center := minSize(860, 620, container.NewBorder(u.topBar(), nil, nil, nil, u.profileEditor()))
	u.win.SetContent(container.NewBorder(nil, nil, side, nil, center))
}

func (u *UI) sidebar() fyne.CanvasObject {
	title := widget.NewLabel("SAKR TUN")
	title.TextStyle = fyne.TextStyle{Bold: true}
	subtitle := widget.NewLabel(fmt.Sprintf("v%s \u00b7 Offline profiles", coreapp.Version))
	subtitle.Importance = widget.LowImportance

	newBtn := widget.NewButtonWithIcon("New", theme.ContentAddIcon(), u.newProfile)
	deleteBtn := widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), u.deleteProfile)
	importBtn := widget.NewButtonWithIcon("Import", theme.FolderOpenIcon(), u.importProfile)
	exportBtn := widget.NewButtonWithIcon("Export", theme.DocumentSaveIcon(), u.exportProfile)
	linkBtn := widget.NewButtonWithIcon("Import link", theme.DocumentCreateIcon(), u.importLink)
	updateBtn := widget.NewButtonWithIcon("Update app", theme.ViewRefreshIcon(), u.updateApp)
	buttons := container.NewGridWithColumns(2, newBtn, deleteBtn, importBtn, exportBtn, linkBtn, updateBtn)

	emptyHint := widget.NewLabel("Create or import a .sakr profile to start.")
	emptyHint.Wrapping = fyne.TextWrapWord
	emptyHint.Importance = widget.LowImportance

	top := container.NewVBox(title, subtitle, buttons, widget.NewSeparator())
	body := container.NewBorder(top, container.NewVBox(widget.NewSeparator(), emptyHint), nil, nil, u.profileList)
	return container.NewPadded(body)
}

func (u *UI) topBar() fyne.CanvasObject {
	brand := widget.NewLabel("Tunnel Control")
	brand.TextStyle = fyne.TextStyle{Bold: true}
	statusBlock := container.NewVBox(u.status, u.subStatus)
	u.killSwitch = widget.NewCheck("Kill Switch", u.toggleKillSwitch)
	actions := container.NewHBox(
		u.killSwitch,
		widget.NewButtonWithIcon("Save", theme.DocumentSaveIcon(), u.saveProfile),
		u.connectBtn,
	)
	content := container.NewBorder(nil, nil, container.NewVBox(brand), actions, statusBlock)
	return softPanel(container.NewPadded(content))
}

func (u *UI) profileEditor() fyne.CanvasObject {
	u.name = widget.NewEntry()
	u.name.SetPlaceHolder("My server")
	u.mode = widget.NewSelect(modeLabels(), func(_ string) { u.updateModePanel() })

	u.sshHost = widget.NewEntry()
	u.sshHost.SetPlaceHolder("ssh.example.com")
	u.sshPort = widget.NewEntry()
	u.sshPort.SetPlaceHolder("22")
	u.sshUser = widget.NewEntry()
	u.sshUser.SetPlaceHolder("username")
	u.sshPass = widget.NewPasswordEntry()
	u.sshPass.SetPlaceHolder("password")
	u.keepAlive = widget.NewEntry()
	u.handshake = widget.NewEntry()

	u.proxyHost = widget.NewEntry()
	u.proxyHost.SetPlaceHolder("proxy1.com#proxy2.com#52.85.78.104")
	u.proxyPort = widget.NewEntry()
	u.proxyPort.SetPlaceHolder("80 or 443")

	u.payloadText = widget.NewMultiLineEntry()
	u.payloadText.SetMinRowsVisible(8)
	u.payloadText.Wrapping = fyne.TextWrapOff
	u.payloadText.SetPlaceHolder("CONNECT [host]:[port] HTTP/1.1[crlf]Host: [host][crlf][crlf]")
	u.payloadWait = widget.NewCheck("Wait for proxy response", nil)
	u.payloadAny = widget.NewCheck("Accept any HTTP status", nil)
	u.payloadReply = widget.NewEntry()
	u.payloadSplit = widget.NewEntry()

	u.tlsHost = widget.NewEntry()
	u.tlsHost.SetPlaceHolder("cdn.example.com")
	u.tlsPort = widget.NewEntry()
	u.tlsSNI = widget.NewEntry()
	u.tlsSNI.SetPlaceHolder("server name / SNI")
	u.tlsInsecure = widget.NewCheck("Allow insecure TLS / skip certificate verify", nil)

	u.dnsttEmbedded = widget.NewCheck("Use embedded DNSTT engine / no external EXE", nil)
	u.dnsttResolverType = widget.NewSelect([]string{"doh", "dot", "udp"}, nil)
	u.dnsttResolverAddr = widget.NewEntry()
	u.dnsttResolverAddr.SetPlaceHolder("DoH URL, DoT host:853, or UDP DNS host:53")
	u.dnsttDomain = widget.NewEntry()
	u.dnsttDomain.SetPlaceHolder("t.example.com")
	u.dnsttPublicKey = widget.NewEntry()
	u.dnsttPublicKey.SetPlaceHolder("server public key hex")
	u.dnsttUTLS = widget.NewEntry()
	u.dnsttExe = widget.NewEntry()
	u.dnsttArgs = widget.NewMultiLineEntry()
	u.dnsttArgs.SetMinRowsVisible(3)
	u.dnsttLocalHost = widget.NewEntry()
	u.dnsttLocalPort = widget.NewEntry()

	u.xrayExe = widget.NewEntry()
	u.xrayExe.SetPlaceHolder("tools/xray/xray.exe")
	u.xrayArgs = widget.NewEntry()
	u.xrayConfig = widget.NewEntry()
	u.xrayConfig.SetPlaceHolder("configs/xray.json")
	u.xraySocksHost = widget.NewEntry()
	u.xraySocksPort = widget.NewEntry()
	u.xrayJSON = widget.NewMultiLineEntry()
	u.xrayJSON.SetMinRowsVisible(14)
	u.xrayJSON.Wrapping = fyne.TextWrapOff
	u.xrayJSON.TextStyle = fyne.TextStyle{Monospace: true}
	u.xrayJSON.SetPlaceHolder(`{"log": {"loglevel": "warning"}, "inbounds": [...], "outbounds": [...]}`)

	u.openvpnExe = widget.NewEntry()
	u.openvpnExe.SetPlaceHolder("openvpn (leave empty to auto-detect)")
	u.openvpnConfig = widget.NewEntry()
	u.openvpnConfig.SetPlaceHolder("path to the .ovpn config file")
	u.openvpnUser = widget.NewEntry()
	u.openvpnPass = widget.NewPasswordEntry()
	u.openvpnAuthFile = widget.NewEntry()
	u.openvpnAuthFile.SetPlaceHolder("optional auth-user-pass file")
	u.openvpnVerb = widget.NewEntry()
	u.openvpnVerb.SetPlaceHolder("3")

	u.udpgwEnabled = widget.NewCheck("Enable UDPGW for real UDP in SSH modes", nil)
	u.udpgwProtocol = widget.NewSelect([]string{"badvpn", "legacy"}, nil)
	u.udpgwProtocol.SetSelected("badvpn")
	u.udpgwHost = widget.NewEntry()
	u.udpgwHost.SetPlaceHolder("127.0.0.1")
	u.udpgwPort = widget.NewEntry()
	u.udpgwPort.SetPlaceHolder("7400")

	u.localSocksHost = widget.NewEntry()
	u.localSocksPort = widget.NewEntry()

	// Route mode: the user picks between a full-device TUN (the system routes
	// everything through the VPN), Proxifier (the local SOCKS proxy is pushed
	// into Proxifier, which forces apps through it) or a plain local SOCKS
	// proxy (apps must be configured to use the SOCKS address manually).
	u.routeMode = widget.NewSelect([]string{routeModeTUN, routeModeProxifier, routeModeProxy}, func(s string) {
		if u.syncingRoute {
			return
		}
		u.syncingRoute = true
		if u.tunEnabled != nil {
			u.tunEnabled.SetChecked(s == routeModeTUN)
		}
		u.syncingRoute = false
		u.updateSocksInfo()
	})
	u.routeMode.SetSelected(routeModeTUN)
	u.socksInfo = widget.NewLabel("")
	u.socksCopy = widget.NewButtonWithIcon("Copy SOCKS address", theme.ContentCopyIcon(), func() {
		addr := u.socksInfo.Text
		if addr == "" {
			return
		}
		u.win.Clipboard().SetContent(addr)
		u.socksCopy.SetText("Copied!")
		time.AfterFunc(1500*time.Millisecond, func() {
			fyne.Do(func() { u.socksCopy.SetText("Copy SOCKS address") })
		})
	})
	u.proxifierExe = widget.NewEntry()
	u.proxifierExe.SetPlaceHolder("empty = auto-detect Proxifier.exe (Program Files / PATH)")
	u.proxifierLicName = widget.NewEntry()
	u.proxifierLicName.SetPlaceHolder("license owner name")
	u.proxifierLicKey = widget.NewEntry()
	u.proxifierLicKey.SetPlaceHolder("registration key (registered automatically)")

	u.dnsServers = widget.NewEntry()
	u.dnsServers.SetPlaceHolder("empty = default (1.1.1.1, 8.8.8.8 / TUN DNS)")

	u.reconnectEnabled = widget.NewCheck("Auto reconnect if the tunnel is lost", nil)
	u.reconnectDelay = widget.NewEntry()
	u.reconnectDelay.SetPlaceHolder("3")
	u.reconnectMax = widget.NewEntry()
	u.reconnectMax.SetPlaceHolder("0 = forever")
	u.reconnectCheck = widget.NewEntry()
	u.reconnectCheck.SetPlaceHolder("10")

	u.tunEnabled = widget.NewCheck("Enable full-device TUN mode", func(on bool) {
		if u.syncingRoute {
			return
		}
		u.syncingRoute = true
		if u.routeMode != nil {
			if on {
				u.routeMode.SetSelected(routeModeTUN)
			} else {
				u.routeMode.SetSelected(routeModeProxy)
			}
		}
		u.syncingRoute = false
		u.updateSocksInfo()
	})
	u.tunDevice = widget.NewEntry()
	u.tunDevice.SetPlaceHolder("Windows: wintun | Linux: tun://socksrevive0")
	u.tunIface = widget.NewEntry()
	u.tunIface.SetPlaceHolder("Windows: wintun | Linux: socksrevive0")
	u.tunMTU = widget.NewEntry()
	u.tunCIDR = widget.NewEntry()
	u.tunCIDR.SetPlaceHolder("198.18.0.1/15")
	u.tunDNS = widget.NewEntry()
	u.tunDNS.SetPlaceHolder("1.1.1.1, 8.8.8.8")
	u.tunRoute = widget.NewCheck("Route all IPv4 traffic", nil)
	u.tunIPv6Enabled = widget.NewCheck("Enable IPv6 through tunnel", nil)
	u.tunIPv6CIDR = widget.NewEntry()
	u.tunIPv6CIDR.SetPlaceHolder("fd00:534f:434b::1/64")
	u.tunIPv6DNS = widget.NewEntry()
	u.tunIPv6DNS.SetPlaceHolder("2606:4700:4700::1111, 2001:4860:4860::8888")
	u.tunBlockIPv6Leak = widget.NewCheck("Block IPv6 leaks when IPv6 tunnel is off", nil)

	u.logView = widget.NewLabel("No logs yet.")
	u.logView.Wrapping = fyne.TextWrapWord
	u.logView.TextStyle = fyne.TextStyle{Monospace: true}
	u.logScroller = container.NewVScroll(u.logView)
	u.logScroller.SetMinSize(fyne.NewSize(760, 430))

	u.modeContent = container.NewVBox()
	u.updateModePanel()
	return container.NewPadded(u.modeContent)
}

func (u *UI) updateModePanel() {
	if u.modeContent == nil || u.mode == nil {
		return
	}
	// Preserve the currently selected tab across panel rebuilds (for example
	// when connecting from the Logs tab, the form reload must not kick the
	// user back to the Main tab).
	selectedTitle := ""
	if len(u.modeContent.Objects) > 0 {
		if tabs, ok := u.modeContent.Objects[0].(*container.AppTabs); ok && tabs.Selected() != nil {
			selectedTitle = tabs.Selected().Text
		}
	}
	mode := modeValue(u.mode.Selected)
	if u.routeMode != nil {
		// OpenVPN routes through its own adapter, so the TUN/proxy choice
		// does not apply there.
		if mode == config.ModeOpenVPN {
			u.routeMode.Disable()
		} else {
			u.routeMode.Enable()
		}
		u.updateSocksInfo()
	}
	tabs := container.NewAppTabs(
		tabPage("Main", u.mainSection()),
	)
	switch mode {
	case config.ModeDirect:
		tabs.Append(tabPage("SSH", u.sshSection(true)))
	case config.ModePayload:
		tabs.Append(tabPage("SSH", u.sshSection(true)))
		tabs.Append(tabPage("Proxy", u.proxySection()))
		tabs.Append(tabPage("Payload", u.payloadSection()))
	case config.ModeSSL:
		tabs.Append(tabPage("SSH", u.sshSection(true)))
		tabs.Append(tabPage("SSL/SNI", u.tlsSection()))
	case config.ModePayloadSSL:
		tabs.Append(tabPage("SSH", u.sshSection(true)))
		tabs.Append(tabPage("SSL/SNI", u.tlsSection()))
		tabs.Append(tabPage("Payload", u.payloadSection()))
	case config.ModeDNSTT:
		tabs.Append(tabPage("DNSTT", u.dnsttSection()))
		tabs.Append(tabPage("SSH", u.sshSection(false)))
	case config.ModeXray:
		tabs.Append(tabPage("Xray", u.xraySection()))
	case config.ModeOpenVPN:
		tabs.Append(tabPage("OpenVPN", u.openvpnSection()))
	default:
		tabs.Append(tabPage("SSH", u.sshSection(true)))
	}
	if mode != config.ModeXray && mode != config.ModeOpenVPN {
		tabs.Append(tabPage("UDPGW", u.udpgwSection()))
	}
	if mode != config.ModeOpenVPN {
		tabs.Append(tabPage("TUN", u.tunSection()))
	}
	tabs.Append(tabPage("Logs", u.logsSection()))
	tabs.SetTabLocation(container.TabLocationTop)
	if selectedTitle != "" {
		for _, item := range tabs.Items {
			if item.Text == selectedTitle {
				tabs.Select(item)
				break
			}
		}
	}
	u.modeContent.Objects = []fyne.CanvasObject{tabs}
	u.modeContent.Refresh()
}

func (u *UI) mainSection() fyne.CanvasObject {
	return section("Profile", "Choose the tunnel mode first. Only the tabs needed for that mode are shown.", widget.NewForm(
		widget.NewFormItem("Profile name", u.name),
		widget.NewFormItem("Tunnel mode", u.mode),
		widget.NewFormItem("Route mode", u.routeMode),
		widget.NewFormItem("SOCKS proxy", container.NewHBox(u.socksInfo, u.socksCopy)),
		widget.NewFormItem("Proxifier executable", u.proxifierExe),
		widget.NewFormItem("Proxifier license name", u.proxifierLicName),
		widget.NewFormItem("Proxifier license key", u.proxifierLicKey),
		widget.NewFormItem("", u.routeModeTip()),
		widget.NewFormItem("Local SOCKS host", u.localSocksHost),
		widget.NewFormItem("Local SOCKS port", u.localSocksPort),
		widget.NewFormItem("Custom DNS servers", u.dnsServers),
		widget.NewFormItem("Reconnect", u.reconnectEnabled),
		widget.NewFormItem("Reconnect delay seconds", u.reconnectDelay),
		widget.NewFormItem("Reconnect max retries", u.reconnectMax),
		widget.NewFormItem("Reconnect check seconds", u.reconnectCheck),
	))
}

func (u *UI) sshSection(includeHost bool) fyne.CanvasObject {
	items := []*widget.FormItem{}
	if includeHost {
		items = append(items,
			widget.NewFormItem("SSH host", u.sshHost),
			widget.NewFormItem("SSH port", u.sshPort),
		)
	}
	items = append(items,
		widget.NewFormItem("Username", u.sshUser),
		widget.NewFormItem("Password", u.sshPass),
		widget.NewFormItem("Keep alive seconds", u.keepAlive),
		widget.NewFormItem("Handshake timeout ms", u.handshake),
	)
	subtitle := "Credentials used after the transport is ready."
	if includeHost {
		subtitle = "SSH server and login credentials."
	}
	return section("SSH", subtitle, widget.NewForm(items...))
}

func (u *UI) proxySection() fyne.CanvasObject {
	return section("Optional proxy / rotate", "Leave empty to connect directly. For rotation, separate hosts with #, comma, semicolon, or new lines. The app tries each proxy until payload + SSH succeeds.", widget.NewForm(
		widget.NewFormItem("Proxy host(s)", u.proxyHost),
		widget.NewFormItem("Proxy port", u.proxyPort),
	))
}

func (u *UI) payloadSection() fyne.CanvasObject {
	settings := widget.NewForm(
		widget.NewFormItem("Response timeout ms", u.payloadReply),
		widget.NewFormItem("Split delay ms", u.payloadSplit),
	)
	content := container.NewVBox(u.payloadText, u.payloadWait, u.payloadAny, settings)
	return section("HTTP payload", "Supports [host], [port], [crlf], [lf], [rotate=a;b;c] or [rotate=a#b#c], [split] and [instant_split].", content)
}

func (u *UI) tlsSection() fyne.CanvasObject {
	return section("SSL/SNI", "TLS front host and SNI used before SSH or payload injection.", widget.NewForm(
		widget.NewFormItem("TLS/front host", u.tlsHost),
		widget.NewFormItem("TLS port", u.tlsPort),
		widget.NewFormItem("SNI/server name", u.tlsSNI),
		widget.NewFormItem("Security", u.tlsInsecure),
	))
}

func (u *UI) dnsttSection() fyne.CanvasObject {
	resolver := widget.NewForm(
		widget.NewFormItem("Embedded engine", u.dnsttEmbedded),
		widget.NewFormItem("Resolver type", u.dnsttResolverType),
		widget.NewFormItem("Resolver", u.dnsttResolverAddr),
		widget.NewFormItem("Tunnel domain", u.dnsttDomain),
		widget.NewFormItem("Server public key", u.dnsttPublicKey),
		widget.NewFormItem("uTLS profile", u.dnsttUTLS),
		widget.NewFormItem("Local SSH host", u.dnsttLocalHost),
		widget.NewFormItem("Local SSH port", u.dnsttLocalPort),
	)
	fallback := widget.NewAccordion(widget.NewAccordionItem("External DNSTT fallback", widget.NewForm(
		widget.NewFormItem("Executable", u.dnsttExe),
		widget.NewFormItem("Arguments", u.dnsttArgs),
	)))
	return section("DNSTT", "Embedded client is used by default. External executable is only for legacy fallback.", container.NewVBox(resolver, fallback))
}

func (u *UI) xraySection() fyne.CanvasObject {
	templateBtn := widget.NewButton("Template", func() {
		u.xrayJSON.SetText(xrayTemplateJSON)
	})
	loadBtn := widget.NewButton("Load from file", func() {
		path := strings.TrimSpace(u.xrayConfig.Text)
		if path == "" {
			dialog.ShowInformation("Xray", "Type the config file path in the field above first.", u.win)
			return
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(u.core.Root, path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		u.xrayJSON.SetText(string(b))
	})
	settings := widget.NewForm(
		widget.NewFormItem("Executable", u.xrayExe),
		widget.NewFormItem("Arguments", u.xrayArgs),
		widget.NewFormItem("Config file", u.xrayConfig),
		widget.NewFormItem("Xray SOCKS host", u.xraySocksHost),
		widget.NewFormItem("Xray SOCKS port", u.xraySocksPort),
	)
	jsonLabel := widget.NewLabel("Manual JSON configuration (create it here; when not empty it overrides the config file above):")
	jsonLabel.Wrapping = fyne.TextWrapWord
	content := container.NewVBox(
		settings,
		widget.NewSeparator(),
		jsonLabel,
		u.xrayJSON,
		container.NewHBox(templateBtn, loadBtn, widget.NewButton("Update Xray", u.updateXray)),
	)
	return section("Xray", "Place xray.exe in tools/xray or browse to your own executable path. Create the configuration manually in JSON format, or point to an existing config file.", content)
}

// xrayTemplateJSON is the default VLESS over TCP+TLS template offered by the
// manual editor's Template button.
const xrayTemplateJSON = `{
  "log": { "loglevel": "warning" },
  "inbounds": [
    {
      "tag": "socks-in",
      "port": 10808,
      "listen": "127.0.0.1",
      "protocol": "socks",
      "settings": { "udp": true }
    }
  ],
  "outbounds": [
    {
      "tag": "proxy",
      "protocol": "vless",
      "settings": {
        "vnext": [
          {
            "address": "CHANGE_ME_HOST",
            "port": 443,
            "users": [ { "id": "CHANGE_ME_UUID", "encryption": "none" } ]
          }
        ]
      },
      "streamSettings": {
        "network": "tcp",
        "security": "tls",
        "tlsSettings": { "serverName": "CHANGE_ME_SNI" }
      }
    },
    { "tag": "direct", "protocol": "freedom" }
  ]
}`

func (u *UI) openvpnSection() fyne.CanvasObject {
	browse := widget.NewButtonWithIcon("Browse", theme.FolderOpenIcon(), func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, u.win)
				return
			}
			if r == nil {
				return
			}
			defer r.Close()
			path := r.URI().Path()
			// Fyne file URIs on Windows sometimes carry a leading slash
			// ("/C:/...").
			if len(path) > 2 && path[0] == '/' && path[2] == ':' {
				path = path[1:]
			}
			u.openvpnConfig.SetText(path)
		}, u.win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".ovpn", ".conf"}))
		fd.Show()
	})
	configRow := container.NewBorder(nil, nil, nil, browse, u.openvpnConfig)
	return section("OpenVPN", "The OpenVPN process creates and manages its own adapter and routes. Leave the executable empty to auto-detect it (app folder, PATH, then the standard Program Files install). Username/password are optional and only used when the .ovpn requests auth-user-pass.", widget.NewForm(
		widget.NewFormItem("Executable", u.openvpnExe),
		widget.NewFormItem("Config file (.ovpn)", configRow),
		widget.NewFormItem("Username", u.openvpnUser),
		widget.NewFormItem("Password", u.openvpnPass),
		widget.NewFormItem("Auth file (optional)", u.openvpnAuthFile),
		widget.NewFormItem("Verbosity (0-11)", u.openvpnVerb),
		widget.NewFormItem("Update", widget.NewButton("Update OpenVPN", u.updateOpenVPN)),
	))
}

func (u *UI) udpgwSection() fyne.CanvasObject {
	return section("UDPGW / UDP support", "Optional UDP gateway for SSH-based modes. Use badvpn for Android-compatible badvpn-udpgw servers and IPv6 UDP. Use legacy only for the old experimental PC frame.", widget.NewForm(
		widget.NewFormItem("Enabled", u.udpgwEnabled),
		widget.NewFormItem("Protocol", u.udpgwProtocol),
		widget.NewFormItem("Remote UDPGW host", u.udpgwHost),
		widget.NewFormItem("Remote UDPGW port", u.udpgwPort),
	))
}

func (u *UI) tunSection() fyne.CanvasObject {
	return section("TUN / full device mode", "Optional OpenVPN-style routing through the active SOCKS tunnel. IPv6 is optional; enable it only when your server/tunnel has IPv6 egress. Keep leak blocking on if you do not want Windows to expose IPv6 outside the VPN.", widget.NewForm(
		widget.NewFormItem("Enabled", u.tunEnabled),
		widget.NewFormItem("Device", u.tunDevice),
		widget.NewFormItem("Interface name", u.tunIface),
		widget.NewFormItem("MTU", u.tunMTU),
		widget.NewFormItem("IPv4 CIDR", u.tunCIDR),
		widget.NewFormItem("IPv4 DNS", u.tunDNS),
		widget.NewFormItem("IPv4 routing", u.tunRoute),
		widget.NewFormItem("IPv6 support", u.tunIPv6Enabled),
		widget.NewFormItem("IPv6 CIDR", u.tunIPv6CIDR),
		widget.NewFormItem("IPv6 DNS", u.tunIPv6DNS),
		widget.NewFormItem("IPv6 leak protection", u.tunBlockIPv6Leak),
	))
}

func (u *UI) logsSection() fyne.CanvasObject {
	clearBtn := widget.NewButtonWithIcon("Clear logs", theme.DeleteIcon(), u.clearLogs)
	top := container.NewBorder(nil, nil, nil, clearBtn, widget.NewLabel(""))
	content := container.NewBorder(top, nil, nil, nil, u.logScroller)
	return section("Logs", "Runtime messages from SSH, DNSTT, Xray, TUN and routing.", content)
}

// clearLogs empties the Logs tab, the in-memory engine log and the runtime
// and crash log files on disk.
func (u *UI) clearLogs() {
	u.core.Engine.ClearLogs()
	u.logText = ""
	u.logView.SetText("No logs yet.")
	if u.logScroller != nil {
		u.logScroller.ScrollToTop()
	}
	logDir := filepath.Join(u.core.Root, "logs")
	for _, name := range []string{"runtime.log", "crash.log"} {
		_ = os.Truncate(filepath.Join(logDir, name), 0)
	}
}

func (u *UI) newProfile() {
	p := config.Profile{
		Name:      "New Profile",
		Mode:      config.ModeDirect,
		Payload:   config.PayloadConfig{Text: "CONNECT [host]:[port] HTTP/1.1[crlf]Host: [host][crlf]User-Agent: SAKRTUN[crlf][crlf]", WaitForResponse: true, AcceptAnyStatus: true},
		TLS:       config.TLSConfig{Port: 443},
		DNSTT:     config.DNSTTConfig{UseEmbedded: true, ResolverType: "doh"},
		UDPGW:     config.UDPGWConfig{Host: "127.0.0.1", Port: 7400, Protocol: "badvpn"},
		Reconnect: config.ReconnectConfig{Enabled: true, DelaySeconds: 3, MaxRetries: 0, CheckIntervalSeconds: 10},
		Tun:       config.TunConfig{RouteAll: true},
	}
	config.ApplyDefaults(&p)
	u.setProfile(p)
}

func (u *UI) loadProfiles() {
	profiles, err := u.core.Store.List()
	if err != nil {
		dialog.ShowError(err, u.win)
		return
	}
	u.profiles = profiles
	u.profileList.Refresh()
	if u.current.ID == "" && len(profiles) > 0 {
		p, err := u.core.Store.Load(profiles[0].ID)
		if err == nil {
			u.setProfile(p)
		}
	}
	if len(profiles) == 0 {
		u.newProfile()
	}
}

func (u *UI) setProfile(p config.Profile) {
	config.ApplyDefaults(&p)
	u.current = p
	u.name.SetText(p.Name)
	u.mode.SetSelected(modeLabel(p.Mode))
	u.sshHost.SetText(p.SSH.Host)
	u.sshPort.SetText(itoa(p.SSH.Port))
	u.sshUser.SetText(p.SSH.Username)
	u.sshPass.SetText(p.SSH.Password)
	u.keepAlive.SetText(itoa(p.SSH.KeepAliveSeconds))
	u.handshake.SetText(itoa(p.SSH.HandshakeTimeoutMs))
	u.proxyHost.SetText(p.Proxy.Host)
	u.proxyPort.SetText(itoa(p.Proxy.Port))
	u.payloadText.SetText(p.Payload.Text)
	u.payloadWait.SetChecked(p.Payload.WaitForResponse)
	u.payloadAny.SetChecked(p.Payload.AcceptAnyStatus)
	u.payloadReply.SetText(itoa(p.Payload.ResponseTimeoutMs))
	u.payloadSplit.SetText(itoa(p.Payload.SplitDelayMs))
	u.tlsHost.SetText(p.TLS.Host)
	u.tlsPort.SetText(itoa(p.TLS.Port))
	u.tlsSNI.SetText(p.TLS.ServerName)
	u.tlsInsecure.SetChecked(p.TLS.InsecureSkipVerify)
	u.dnsttEmbedded.SetChecked(p.DNSTT.UseEmbedded)
	u.dnsttResolverType.SetSelected(p.DNSTT.ResolverType)
	u.dnsttResolverAddr.SetText(p.DNSTT.ResolverAddress)
	u.dnsttDomain.SetText(p.DNSTT.Domain)
	u.dnsttPublicKey.SetText(p.DNSTT.PublicKey)
	u.dnsttUTLS.SetText(p.DNSTT.UTLSDistribution)
	u.dnsttExe.SetText(p.DNSTT.Executable)
	u.dnsttArgs.SetText(strings.Join(p.DNSTT.Args, " "))
	u.dnsttLocalHost.SetText(p.DNSTT.LocalSSHHost)
	u.dnsttLocalPort.SetText(itoa(p.DNSTT.LocalSSHPort))
	u.xrayExe.SetText(p.Xray.Executable)
	u.xrayArgs.SetText(strings.Join(p.Xray.Args, " "))
	u.xrayConfig.SetText(p.Xray.ConfigPath)
	u.xraySocksHost.SetText(p.Xray.LocalSocksHost)
	u.xraySocksPort.SetText(itoa(p.Xray.LocalSocksPort))
	u.xrayJSON.SetText(p.Xray.ConfigJSON)
	u.openvpnExe.SetText(p.OpenVPN.Executable)
	u.openvpnConfig.SetText(p.OpenVPN.ConfigPath)
	u.openvpnUser.SetText(p.OpenVPN.Username)
	u.openvpnPass.SetText(p.OpenVPN.Password)
	u.openvpnAuthFile.SetText(p.OpenVPN.AuthFile)
	u.openvpnVerb.SetText(itoa(p.OpenVPN.Verbosity))
	u.udpgwEnabled.SetChecked(p.UDPGW.Enabled)
	u.udpgwProtocol.SetSelected(p.UDPGW.Protocol)
	u.udpgwHost.SetText(p.UDPGW.Host)
	u.udpgwPort.SetText(itoa(p.UDPGW.Port))
	u.localSocksHost.SetText(p.Local.SocksHost)
	u.localSocksPort.SetText(itoa(p.Local.SocksPort))
	u.dnsServers.SetText(strings.Join(p.DNS.Servers, ", "))
	u.reconnectEnabled.SetChecked(p.Reconnect.Enabled)
	u.reconnectDelay.SetText(itoa(p.Reconnect.DelaySeconds))
	u.reconnectMax.SetText(itoa(p.Reconnect.MaxRetries))
	u.reconnectCheck.SetText(itoa(p.Reconnect.CheckIntervalSeconds))
	u.tunEnabled.SetChecked(p.Tun.Enabled)
	u.tunDevice.SetText(p.Tun.Device)
	u.tunIface.SetText(p.Tun.InterfaceName)
	u.tunMTU.SetText(itoa(p.Tun.MTU))
	u.tunCIDR.SetText(p.Tun.CIDR)
	u.tunDNS.SetText(strings.Join(p.Tun.DNS, ", "))
	u.tunRoute.SetChecked(p.Tun.RouteAll)
	u.tunIPv6Enabled.SetChecked(p.Tun.IPv6Enabled)
	u.tunIPv6CIDR.SetText(p.Tun.IPv6CIDR)
	u.tunIPv6DNS.SetText(strings.Join(p.Tun.IPv6DNS, ", "))
	u.tunBlockIPv6Leak.SetChecked(!p.Tun.AllowIPv6Leak)
	u.proxifierExe.SetText(p.Proxifier.ExePath)
	u.proxifierLicName.SetText(p.Proxifier.LicenseName)
	u.proxifierLicKey.SetText(p.Proxifier.LicenseKey)
	if u.routeMode != nil {
		switch routeModeOfUI(p) {
		case "proxifier":
			u.routeMode.SetSelected(routeModeProxifier)
		case "proxy":
			u.routeMode.SetSelected(routeModeProxy)
		default:
			u.routeMode.SetSelected(routeModeTUN)
		}
	}
	u.updateSocksInfo()
	u.updateModePanel()
	u.profileList.Refresh()
}

func (u *UI) readProfileFromForm() (config.Profile, error) {
	p := u.current
	p.Name = strings.TrimSpace(u.name.Text)
	p.Mode = modeValue(u.mode.Selected)
	p.SSH.Host = strings.TrimSpace(u.sshHost.Text)
	p.SSH.Port = mustInt(u.sshPort.Text, 22)
	p.SSH.Username = strings.TrimSpace(u.sshUser.Text)
	p.SSH.Password = u.sshPass.Text
	p.SSH.KeepAliveSeconds = mustInt(u.keepAlive.Text, 20)
	p.SSH.HandshakeTimeoutMs = mustInt(u.handshake.Text, 15000)
	p.Proxy.Host = strings.TrimSpace(u.proxyHost.Text)
	p.Proxy.Port = mustInt(u.proxyPort.Text, 0)
	p.Payload.Text = u.payloadText.Text
	p.Payload.WaitForResponse = u.payloadWait.Checked
	p.Payload.AcceptAnyStatus = u.payloadAny.Checked
	p.Payload.ResponseTimeoutMs = mustInt(u.payloadReply.Text, 8000)
	p.Payload.SplitDelayMs = mustInt(u.payloadSplit.Text, 120)
	p.TLS.Enabled = p.Mode == config.ModeSSL || p.Mode == config.ModePayloadSSL || u.tlsHost.Text != "" || u.tlsSNI.Text != ""
	p.TLS.Host = strings.TrimSpace(u.tlsHost.Text)
	p.TLS.Port = mustInt(u.tlsPort.Text, 443)
	p.TLS.ServerName = strings.TrimSpace(u.tlsSNI.Text)
	p.TLS.InsecureSkipVerify = u.tlsInsecure.Checked
	p.DNSTT.Enabled = p.Mode == config.ModeDNSTT
	p.DNSTT.UseEmbedded = u.dnsttEmbedded.Checked
	p.DNSTT.ResolverType = strings.TrimSpace(u.dnsttResolverType.Selected)
	p.DNSTT.ResolverAddress = strings.TrimSpace(u.dnsttResolverAddr.Text)
	p.DNSTT.Domain = strings.TrimSpace(u.dnsttDomain.Text)
	p.DNSTT.PublicKey = strings.TrimSpace(u.dnsttPublicKey.Text)
	p.DNSTT.UTLSDistribution = strings.TrimSpace(u.dnsttUTLS.Text)
	p.DNSTT.Executable = strings.TrimSpace(u.dnsttExe.Text)
	p.DNSTT.Args = splitArgs(u.dnsttArgs.Text)
	p.DNSTT.LocalSSHHost = strings.TrimSpace(u.dnsttLocalHost.Text)
	p.DNSTT.LocalSSHPort = mustInt(u.dnsttLocalPort.Text, 2222)
	p.Xray.Executable = strings.TrimSpace(u.xrayExe.Text)
	p.Xray.Args = splitArgs(u.xrayArgs.Text)
	p.Xray.ConfigPath = strings.TrimSpace(u.xrayConfig.Text)
	p.Xray.LocalSocksHost = strings.TrimSpace(u.xraySocksHost.Text)
	p.Xray.LocalSocksPort = mustInt(u.xraySocksPort.Text, 10808)
	p.Xray.ConfigJSON = u.xrayJSON.Text
	p.OpenVPN.Executable = strings.TrimSpace(u.openvpnExe.Text)
	p.OpenVPN.ConfigPath = strings.TrimSpace(u.openvpnConfig.Text)
	p.OpenVPN.Username = strings.TrimSpace(u.openvpnUser.Text)
	p.OpenVPN.Password = u.openvpnPass.Text
	p.OpenVPN.AuthFile = strings.TrimSpace(u.openvpnAuthFile.Text)
	p.OpenVPN.Verbosity = mustInt(u.openvpnVerb.Text, 3)
	p.UDPGW.Enabled = u.udpgwEnabled.Checked
	p.UDPGW.Protocol = strings.TrimSpace(u.udpgwProtocol.Selected)
	p.UDPGW.Host = strings.TrimSpace(u.udpgwHost.Text)
	p.UDPGW.Port = mustInt(u.udpgwPort.Text, 7400)
	p.Local.SocksHost = strings.TrimSpace(u.localSocksHost.Text)
	p.Local.SocksPort = mustInt(u.localSocksPort.Text, 10809)
	p.DNS.Servers = splitCSV(u.dnsServers.Text)
	p.Reconnect.Enabled = u.reconnectEnabled.Checked
	p.Reconnect.DelaySeconds = mustInt(u.reconnectDelay.Text, 3)
	p.Reconnect.MaxRetries = mustInt(u.reconnectMax.Text, 0)
	p.Reconnect.CheckIntervalSeconds = mustInt(u.reconnectCheck.Text, 10)
	p.Tun.Enabled = u.tunEnabled.Checked
	p.Tun.RouteMode = u.routeModeFromUI()
	p.Proxifier.ExePath = strings.TrimSpace(u.proxifierExe.Text)
	p.Proxifier.LicenseName = strings.TrimSpace(u.proxifierLicName.Text)
	p.Proxifier.LicenseKey = strings.TrimSpace(u.proxifierLicKey.Text)
	p.Tun.Device = strings.TrimSpace(u.tunDevice.Text)
	p.Tun.InterfaceName = strings.TrimSpace(u.tunIface.Text)
	p.Tun.MTU = mustInt(u.tunMTU.Text, 1500)
	p.Tun.CIDR = strings.TrimSpace(u.tunCIDR.Text)
	p.Tun.DNS = splitCSV(u.tunDNS.Text)
	p.Tun.RouteAll = u.tunRoute.Checked
	p.Tun.IPv6Enabled = u.tunIPv6Enabled.Checked
	p.Tun.IPv6CIDR = strings.TrimSpace(u.tunIPv6CIDR.Text)
	p.Tun.IPv6DNS = splitCSV(u.tunIPv6DNS.Text)
	p.Tun.AllowIPv6Leak = !u.tunBlockIPv6Leak.Checked
	config.ApplyDefaults(&p)
	return p, config.Validate(p)
}

func (u *UI) saveProfile() {
	p, err := u.readProfileFromForm()
	if err != nil {
		dialog.ShowError(err, u.win)
		return
	}
	saved, err := u.core.Store.Save(p)
	if err != nil {
		dialog.ShowError(err, u.win)
		return
	}
	u.current = saved
	u.loadProfiles()
	u.setProfile(saved)
	dialog.ShowInformation("Saved", "Profile saved as .sakr", u.win)
}

func (u *UI) deleteProfile() {
	if u.current.ID == "" {
		return
	}
	dialog.ShowConfirm("Delete profile", "Delete this offline profile?", func(ok bool) {
		if !ok {
			return
		}
		if err := u.core.Store.Delete(u.current.ID); err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		u.current = config.Profile{}
		u.loadProfiles()
	}, u.win)
}

// ---- tool updates and install -----------------------------------------------

func (u *UI) updateXray() {
	crash.Go(u.core.Root, func() {
		installed := updater.InstalledXrayVersion(u.core.Root)
		latest, err := updater.LatestXrayVersion()
		fyne.Do(func() {
			if err != nil {
				dialog.ShowError(fmt.Errorf("check for updates failed: %v", err), u.win)
				return
			}
			u.confirmToolUpdate("Xray", installed, latest, func(log updater.Logger) (string, error) {
				return updater.UpdateXray(u.core.Root, log)
			})
		})
	})
}

func (u *UI) updateOpenVPN() {
	crash.Go(u.core.Root, func() {
		installed := updater.InstalledOpenVPNVersion(u.core.Root)
		latest, err := updater.LatestOpenVPNVersion()
		fyne.Do(func() {
			if err != nil {
				dialog.ShowError(fmt.Errorf("check for updates failed: %v", err), u.win)
				return
			}
			u.confirmToolUpdate("OpenVPN", installed, latest, func(log updater.Logger) (string, error) {
				return updater.UpdateOpenVPN(u.core.Root, log)
			})
		})
	})
}

// confirmToolUpdate shows the installed/latest versions and, when the user
// confirms, runs the update in the background with an infinite progress
// dialog. Progress messages go to the Logs tab.
func (u *UI) confirmToolUpdate(tool, installed, latest string, run func(updater.Logger) (string, error)) {
	if installed == "" {
		installed = "unknown"
	}
	msg := fmt.Sprintf("Installed: %s\nLatest: %s", installed, latest)
	if installed == latest {
		dialog.ShowInformation("Update "+tool, msg+"\n\nYou are already up to date.", u.win)
		return
	}
	dialog.ShowConfirm("Update "+tool, msg+"\n\nDisconnect first, then download and install the update?", func(ok bool) {
		if !ok {
			return
		}
		prog := dialog.NewProgressInfinite("Updating "+tool, "Downloading and installing...", u.win)
		prog.Show()
		crash.Go(u.core.Root, func() {
			ver, err := run(func(level, format string, args ...any) {
				u.core.Engine.AddLog(level, format, args...)
			})
			fyne.Do(func() {
				prog.Hide()
				if err != nil {
					dialog.ShowError(err, u.win)
					return
				}
				dialog.ShowInformation("Update "+tool, tool+" updated to version "+ver, u.win)
			})
		})
	}, u.win)
}

// updateApp checks the faizalsalato/sakrtun GitHub releases and, when a newer
// version exists, downloads and installs it into the app folder.
func (u *UI) updateApp() {
	crash.Go(u.core.Root, func() {
		latest, err := updater.LatestAppVersion()
		fyne.Do(func() {
			if err != nil {
				dialog.ShowError(fmt.Errorf("check for updates failed: %v", err), u.win)
				return
			}
			current := coreapp.Version
			if current == latest {
				dialog.ShowInformation("Update app", fmt.Sprintf("You are running the latest version (%s).", current), u.win)
				return
			}
			dialog.ShowConfirm("Update app",
				fmt.Sprintf("Current: %s\nLatest: %s\n\nDownload and install the update? The app must restart afterwards.", current, latest),
				func(ok bool) {
					if !ok {
						return
					}
					prog := dialog.NewProgressInfinite("Updating app", "Downloading and installing...", u.win)
					prog.Show()
					crash.Go(u.core.Root, func() {
						ver, err := updater.UpdateApp(u.core.Root, coreapp.Version, func(level, format string, args ...any) {
							u.core.Engine.AddLog(level, format, args...)
						})
						fyne.Do(func() {
							prog.Hide()
							if err != nil {
								dialog.ShowError(err, u.win)
								return
							}
							dialog.ShowConfirm("Update app", fmt.Sprintf("Updated to version %s. Restart now?", ver), func(restart bool) {
								if restart {
									u.restartApp()
								}
							}, u.win)
						})
					})
				}, u.win)
		})
	})
}

// restartApp launches a new instance of the installed executable and quits
// the current one.
func (u *UI) restartApp() {
	exeName := "SAKRTUN.exe"
	if runtime.GOOS != "windows" {
		exeName = "sakrtun"
	}
	exe := filepath.Join(u.core.Root, exeName)
	cmd := exec.Command(exe)
	cmd.Dir = u.core.Root
	if err := cmd.Start(); err != nil {
		dialog.ShowError(err, u.win)
		return
	}
	u.core.Engine.Stop()
	u.app.Quit()
}

func (u *UI) toggleConnection() {
	s := u.core.Engine.Status()
	if s.Running || s.Connecting {
		u.disconnect()
		return
	}
	u.connect()
}

// toggleKillSwitch reacts to the advanced Kill Switch toggle. Enabling it asks
// for confirmation because it cuts the internet until the VPN connects.
func (u *UI) toggleKillSwitch(checked bool) {
	if !checked {
		u.applyKillSwitch(false)
		return
	}
	dialog.ShowConfirm("Kill Switch",
		"Activate the kill switch?\n\nThe internet will be blocked whenever the VPN is not connected. Only the VPN servers of the current profile stay reachable so the tunnel can reconnect.",
		func(ok bool) {
			if !ok {
				u.killSwitch.SetChecked(false)
				return
			}
			u.applyKillSwitch(true)
		}, u.win)
}

// applyKillSwitch forwards the toggle to the engine and persists the state in
// the global app settings. The current form profile is passed along so the
// bypass hosts stay in sync with what the user is about to connect.
func (u *UI) applyKillSwitch(enabled bool) {
	p := u.current
	if enabled {
		if fp, err := u.readProfileFromForm(); err == nil {
			p = fp
		}
	}
	u.killSwitch.Disable()
	crash.Go(u.core.Root, func() {
		err := u.core.Engine.SetKillSwitch(enabled, p)
		fyne.Do(func() {
			u.killSwitch.Enable()
			u.killSwitch.SetChecked(enabled)
			if err != nil {
				dialog.ShowError(err, u.win)
			} else {
				u.core.Settings.KillSwitch = enabled
				if saveErr := u.core.SaveSettings(); saveErr != nil {
					dialog.ShowError(saveErr, u.win)
				}
			}
			u.refreshStatus()
		})
	})
}

func (u *UI) connect() {
	p, err := u.readProfileFromForm()
	if err != nil {
		dialog.ShowError(err, u.win)
		return
	}
	saved, err := u.core.Store.Save(p)
	if err != nil {
		dialog.ShowError(err, u.win)
		return
	}
	u.current = saved
	u.loadProfiles()
	u.setProfile(saved)
	u.status.SetText("Connecting...")
	u.subStatus.SetText("Starting tunnel. Logs will keep updating while proxy rotation is tested.")
	u.connectBtn.SetText("Cancel")
	u.connectBtn.SetIcon(theme.MediaStopIcon())
	crash.Go(u.core.Root, func() {
		err := u.core.Engine.Start(saved)
		fyne.Do(func() {
			if err != nil && !errors.Is(err, context.Canceled) {
				dialog.ShowError(err, u.win)
			}
			u.refreshStatus()
		})
	})
}

func (u *UI) disconnect() {
	u.status.SetText("Disconnecting...")
	u.subStatus.SetText("Stopping tunnel and restoring routes.")
	u.connectBtn.Disable()
	crash.Go(u.core.Root, func() {
		u.core.Engine.Stop()
		fyne.Do(func() {
			u.connectBtn.Enable()
			u.refreshStatus()
		})
	})
}

func (u *UI) importLink() {
	entry := widget.NewMultiLineEntry()
	entry.SetPlaceHolder("vless://...\ntrojan://...\nss://...\nvmess://...")
	entry.SetMinRowsVisible(10)
	entry.Wrapping = fyne.TextWrapOff
	entry.TextStyle = fyne.TextStyle{Monospace: true}
	dialog.ShowCustomConfirm("Import share link(s)", "Import", "Cancel", entry, func(ok bool) {
		if !ok {
			return
		}
		var imported, failed []string
		var lastID string
		for _, line := range strings.Split(entry.Text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			cfg, err := xraylink.Parse(line)
			if err != nil {
				failed = append(failed, err.Error())
				continue
			}
			p := config.Profile{Name: cfg.Name, Mode: config.ModeXray, Xray: config.XrayConfig{ConfigJSON: cfg.JSON}}
			config.ApplyDefaults(&p)
			// Imported links are expected to route the whole system: enable the
			// TUN adapter with route-all by default, otherwise the Xray core
			// only exposes a local SOCKS proxy and the user's apps have no
			// internet even though the app reports "Connected".
			p.Tun.Enabled = true
			p.Tun.RouteAll = true
			saved, err := u.core.Store.Save(p)
			if err != nil {
				failed = append(failed, cfg.Name+": "+err.Error())
				continue
			}
			imported = append(imported, saved.Name)
			lastID = saved.ID
		}
		u.loadProfiles()
		if lastID != "" {
			for _, p := range u.profiles {
				if p.ID == lastID {
					u.setProfile(p)
					break
				}
			}
		}
		msg := fmt.Sprintf("%d profile(s) imported:\n\n%s", len(imported), strings.Join(imported, "\n"))
		if len(failed) > 0 {
			msg += "\n\nFailed:\n" + strings.Join(failed, "\n")
		}
		dialog.ShowInformation("Import links", msg, u.win)
	}, u.win)
}

func (u *UI) importProfile() {
	fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		if r == nil {
			return
		}
		defer r.Close()
		b, err := io.ReadAll(r)
		if err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		p, err := config.DecodeProfileFile(b)
		if err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		p.ID = ""
		saved, err := u.core.Store.Save(p)
		if err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		u.loadProfiles()
		u.setProfile(saved)
	}, u.win)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{config.ProfileExtension}))
	fd.Show()
}

func (u *UI) exportProfile() {
	p, err := u.readProfileFromForm()
	if err != nil {
		dialog.ShowError(err, u.win)
		return
	}
	p, err = u.core.Store.Save(p)
	if err != nil {
		dialog.ShowError(err, u.win)
		return
	}
	data, err := config.EncodeProfileFile(p)
	if err != nil {
		dialog.ShowError(err, u.win)
		return
	}
	fd := dialog.NewFileSave(func(w fyne.URIWriteCloser, err error) {
		if err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		if w == nil {
			return
		}
		defer w.Close()
		if _, err := w.Write(data); err != nil {
			dialog.ShowError(err, u.win)
			return
		}
	}, u.win)
	fd.SetFileName(safeFileName(p.Name) + config.ProfileExtension)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{config.ProfileExtension}))
	fd.Show()
}

func (u *UI) refreshStatus() {
	s := u.core.Engine.Status()
	if s.Running {
		u.status.SetText("Connected")
		sub := fmt.Sprintf("Mode: %s  |  SOCKS: %s  |  TUN: %v", s.Mode, s.SocksAddr, s.Tun)
		if s.Traffic == "failed" {
			u.status.SetText("Connected (no traffic!)")
			sub += "  |  no traffic passing - server may be offline"
		}
		u.subStatus.SetText(sub)
		if u.connectBtn != nil {
			u.connectBtn.Enable()
			u.connectBtn.SetText("Disconnect")
			u.connectBtn.SetIcon(theme.MediaStopIcon())
		}
	} else if s.Connecting {
		u.status.SetText("Connecting...")
		if s.SocksAddr != "" {
			u.subStatus.SetText(fmt.Sprintf("Mode: %s  |  SOCKS starting: %s  |  TUN: %v", s.Mode, s.SocksAddr, s.Tun))
		} else {
			u.subStatus.SetText(fmt.Sprintf("Mode: %s  |  trying connection attempts...", s.Mode))
		}
		if u.connectBtn != nil {
			u.connectBtn.Enable()
			u.connectBtn.SetText("Cancel")
			u.connectBtn.SetIcon(theme.MediaStopIcon())
		}
	} else {
		u.status.SetText("Disconnected")
		if s.KillSwitch {
			u.subStatus.SetText("Kill switch active: internet is blocked until the VPN connects")
		} else {
			u.subStatus.SetText("No active tunnel")
		}
		if u.connectBtn != nil {
			u.connectBtn.Enable()
			u.connectBtn.SetText("Connect")
			u.connectBtn.SetIcon(theme.MediaPlayIcon())
		}
	}
}

func (u *UI) startLogPoller() {
	crash.Go(u.core.Root, func() {
		ticker := time.NewTicker(900 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			logs := u.core.Engine.LogsSince(u.lastLog)
			if len(logs) == 0 {
				fyne.Do(u.refreshStatus)
				continue
			}
			var b strings.Builder
			old := u.logText
			if old != "" {
				b.WriteString(old)
				if !strings.HasSuffix(old, "\n") {
					b.WriteByte('\n')
				}
			}
			for _, l := range logs {
				u.lastLog = l.ID
				b.WriteString("[")
				b.WriteString(l.Time)
				b.WriteString("] ")
				b.WriteString(strings.ToUpper(l.Level))
				b.WriteString("  ")
				b.WriteString(l.Message)
				b.WriteByte('\n')
			}
			text := b.String()
			if len(text) > 30000 {
				text = text[len(text)-30000:]
			}
			u.logText = text
			fyne.Do(func() {
				u.logView.SetText(text)
				if u.logScroller != nil {
					u.logScroller.ScrollToBottom()
				}
				u.refreshStatus()
			})
		}
	})
}

func sidebarPanel(obj fyne.CanvasObject) fyne.CanvasObject {
	bg := canvas.NewRectangle(color.NRGBA{R: 16, G: 17, B: 21, A: 255})
	return container.NewStack(bg, obj)
}

func softPanel(obj fyne.CanvasObject) fyne.CanvasObject {
	bg := canvas.NewRectangle(color.NRGBA{R: 24, G: 25, B: 31, A: 255})
	return container.NewStack(bg, obj)
}

func section(title, subtitle string, content fyne.CanvasObject) fyne.CanvasObject {
	titleLabel := widget.NewLabel(title)
	titleLabel.TextStyle = fyne.TextStyle{Bold: true}
	items := []fyne.CanvasObject{titleLabel}
	if strings.TrimSpace(subtitle) != "" {
		sub := widget.NewLabel(subtitle)
		sub.Wrapping = fyne.TextWrapWord
		sub.Importance = widget.LowImportance
		items = append(items, sub)
	}
	items = append(items, widget.NewSeparator(), content)
	return softPanel(container.NewPadded(container.NewVBox(items...)))
}

func helpCard(title, text string) fyne.CanvasObject {
	label := widget.NewLabel(text)
	label.Wrapping = fyne.TextWrapWord
	label.Importance = widget.LowImportance
	return section(title, "", label)
}

func tabPage(title string, content fyne.CanvasObject) *container.TabItem {
	scroll := container.NewVScroll(container.NewPadded(content))
	scroll.SetMinSize(fyne.NewSize(820, 560))
	return container.NewTabItem(title, scroll)
}

type fixedWidthLayout struct {
	width float32
}

func (l *fixedWidthLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, obj := range objects {
		obj.Move(fyne.NewPos(0, 0))
		obj.Resize(fyne.NewSize(l.width, size.Height))
	}
}

func (l *fixedWidthLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	height := float32(0)
	for _, obj := range objects {
		min := obj.MinSize()
		if min.Height > height {
			height = min.Height
		}
	}
	return fyne.NewSize(l.width, height)
}

func fixedWidth(width float32, obj fyne.CanvasObject) fyne.CanvasObject {
	return container.New(&fixedWidthLayout{width: width}, obj)
}

type minSizeLayout struct {
	width  float32
	height float32
}

func (l *minSizeLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, obj := range objects {
		obj.Move(fyne.NewPos(0, 0))
		obj.Resize(size)
	}
}

func (l *minSizeLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	width := l.width
	height := l.height
	for _, obj := range objects {
		min := obj.MinSize()
		if min.Width > width {
			width = min.Width
		}
		if min.Height > height {
			height = min.Height
		}
	}
	return fyne.NewSize(width, height)
}

func minSize(width, height float32, obj fyne.CanvasObject) fyne.CanvasObject {
	return container.New(&minSizeLayout{width: width, height: height}, obj)
}

func modeLabels() []string {
	return []string{"Direct SSH", "Payload SSH", "SSL/TLS SSH", "Payload + SSL", "DNSTT + SSH", "Xray Core", "OpenVPN"}
}

const (
	// routeModeTUN routes the whole system through the VPN with the TUN
	// adapter. routeModeProxifier forces apps through the local SOCKS proxy
	// with Proxifier. routeModeProxy only exposes the local SOCKS proxy; apps
	// must be configured to use that SOCKS address manually.
	routeModeTUN       = "TUN (route all)"
	routeModeProxifier = "Proxifier (force apps)"
	routeModeProxy     = "Proxy only (SOCKS)"
)

// routeModeTip explains the leak caveats of the proxy modes so users know why
// some sites may still see the machine's real IP (WebRTC, non-proxied apps).
func (u *UI) routeModeTip() fyne.CanvasObject {
	tip := widget.NewLabel("Tip: some sites can still see your real IP in the proxy/Proxifier modes (WebRTC uses direct UDP). Use TUN (route all) for full leak protection.")
	tip.Wrapping = fyne.TextWrapWord
	tip.TextStyle = fyne.TextStyle{Italic: true}
	return tip
}

// routeModeFromUI returns the route mode value currently selected in the
// Main tab ("tun", "proxifier" or "proxy").
func (u *UI) routeModeFromUI() string {
	switch u.routeMode.Selected {
	case routeModeProxifier:
		return "proxifier"
	case routeModeProxy:
		return "proxy"
	default:
		return "tun"
	}
}

// routeModeOfUI converts the profile routing fields into the route mode
// shown in the Main tab. Old profiles carry only Tun.Enabled, so derive it.
func routeModeOfUI(p config.Profile) string {
	switch strings.TrimSpace(p.Tun.RouteMode) {
	case "tun":
		return "tun"
	case "proxy":
		return "proxy"
	case "proxifier":
		return "proxifier"
	}
	if p.Tun.Enabled {
		return "tun"
	}
	return "proxy"
}

// updateSocksInfo refreshes the "SOCKS proxy" label in the Main tab with the
// address that local apps can use when the route mode is "Proxy only".
func (u *UI) updateSocksInfo() {
	if u.socksInfo == nil {
		return
	}
	mode := config.ModeDirect
	if u.mode != nil && u.mode.Selected != "" {
		mode = modeValue(u.mode.Selected)
	}
	switch mode {
	case config.ModeOpenVPN:
		u.socksInfo.SetText("(OpenVPN routes through its own adapter)")
	case config.ModeXray:
		host := strings.TrimSpace(u.xraySocksHost.Text)
		port := strings.TrimSpace(u.xraySocksPort.Text)
		if host == "" {
			host = "127.0.0.1"
		}
		if port == "" {
			port = "10808"
		}
		u.socksInfo.SetText(host + ":" + port)
	default:
		host := strings.TrimSpace(u.localSocksHost.Text)
		port := strings.TrimSpace(u.localSocksPort.Text)
		if host == "" {
			host = "127.0.0.1"
		}
		if port == "" {
			port = "10809"
		}
		u.socksInfo.SetText(host + ":" + port)
	}
}

func modeLabel(m config.Mode) string {
	switch m {
	case config.ModePayload:
		return "Payload SSH"
	case config.ModeSSL:
		return "SSL/TLS SSH"
	case config.ModePayloadSSL:
		return "Payload + SSL"
	case config.ModeDNSTT:
		return "DNSTT + SSH"
	case config.ModeXray:
		return "Xray Core"
	case config.ModeOpenVPN:
		return "OpenVPN"
	default:
		return "Direct SSH"
	}
}

func modeValue(label string) config.Mode {
	switch label {
	case "Payload SSH":
		return config.ModePayload
	case "SSL/TLS SSH":
		return config.ModeSSL
	case "Payload + SSL":
		return config.ModePayloadSSL
	case "DNSTT + SSH":
		return config.ModeDNSTT
	case "Xray Core":
		return config.ModeXray
	case "OpenVPN":
		return config.ModeOpenVPN
	default:
		return config.ModeDirect
	}
}

func itoa(v int) string {
	if v == 0 {
		return ""
	}
	return strconv.Itoa(v)
}

func mustInt(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func splitArgs(s string) []string {
	return strings.Fields(strings.TrimSpace(s))
}

func splitCSV(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == ';' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func safeFileName(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "profile"
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-", "?", "-", "\"", "-", "<", "-", ">", "-", "|", "-")
	s = replacer.Replace(s)
	s = strings.ReplaceAll(s, " ", "-")
	return s
}
