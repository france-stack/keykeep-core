//go:build windows

package desktop

// MenuID 是托盘菜单项的命令标识。
type MenuID uint32

const (
	MenuOpen            MenuID = 1000
	MenuStatus          MenuID = 1001
	MenuRestart         MenuID = 1002
	MenuCheckUpdate     MenuID = 1003
	MenuToggleAutoStart MenuID = 1004
	MenuOpenLogs        MenuID = 1005
	MenuOpenDataDir     MenuID = 1006
	MenuQuit            MenuID = 1007
	// 局域网服务端相关（ADR-012）。从 1008 起编号，
	// 必须保持在 wParam 低 16 位以内（见 trayWindowProc 的 & 0xffff）。
	MenuToggleLan      MenuID = 1008
	MenuToggleFirewall MenuID = 1009
	MenuAlwaysOn       MenuID = 1010
	// MenuOpenInBrowser 是 ADR-013 的显式浏览器入口：
	// 默认已经走原生窗口，但「我想用浏览器看」必须是一个点得到的菜单项。
	MenuOpenInBrowser MenuID = 1011
)
