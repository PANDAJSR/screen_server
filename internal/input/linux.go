package input

import (
	"fmt"
	"runtime"
)

type linuxController struct{}

func newLinuxController() (Controller, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("not linux")
	}
	return &linuxController{}, nil
}

func (c *linuxController) MoveMouse(x, y int) error {
	return fmt.Errorf("linux input not fully implemented: requires X11 or evdev")
}

func (c *linuxController) PressMouse(btn MouseButton, x, y int) error {
	return fmt.Errorf("linux input not fully implemented")
}

func (c *linuxController) ReleaseMouse(btn MouseButton, x, y int) error {
	return fmt.Errorf("linux input not fully implemented")
}

func (c *linuxController) Scroll(dx, dy int) error {
	return fmt.Errorf("linux input not fully implemented")
}

func (c *linuxController) PressKey(keyCode int) error {
	return fmt.Errorf("linux input not fully implemented")
}

func (c *linuxController) ReleaseKey(keyCode int) error {
	return fmt.Errorf("linux input not fully implemented")
}

func (c *linuxController) GetKeyState() ([]int, error) {
	return nil, fmt.Errorf("not implemented")
}

func (c *linuxController) ReleaseAllKeys() error {
	return fmt.Errorf("not implemented")
}

func (c *linuxController) Close() error {
	return nil
}

func (c *linuxController) GetCursorPos() (x, y int, err error) {
	return 0, 0, fmt.Errorf("not implemented")
}

func (c *linuxController) GetCursorInfo() (*CursorInfo, error) {
	return nil, fmt.Errorf("not implemented")
}

func (c *linuxController) SetCursorPos(x, y int) error {
	return fmt.Errorf("not implemented")
}

func (c *linuxController) GetScreenSize() (width, height int, err error) {
	return 0, 0, fmt.Errorf("not implemented")
}