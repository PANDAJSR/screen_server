//go:build !windows

package sysinfo

// Stubs for non-Windows platforms.

func EnumSessions() ([]SessionInfo, error) {
	return []SessionInfo{
		{ID: 0, Name: "console", State: "Active", UserName: ""},
	}, nil
}

func EnumDisplays() ([]DisplayInfo, error) {
	return []DisplayInfo{
		{Index: 0, Name: "Default", X: 0, Y: 0, Width: 1920, Height: 1080, Primary: true},
	}, nil
}

func EnumWindows_() ([]WindowInfo, error) {
	return nil, nil
}

func GetWindowRectByTitle(title string) (x, y, width, height int, found bool) {
	return 0, 0, 0, 0, false
}

func GetVirtualScreenSize() (width, height int) {
	return 1920, 1080
}

func GetVirtualScreenOrigin() (x, y int) {
	return 0, 0
}
