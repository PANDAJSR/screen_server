package input

import (
	"fmt"
	"runtime"
)

type darwinController struct{}

func newDarwinController() (Controller, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("not darwin")
	}
	return &darwinController{}, nil
}

func (c *darwinController) MoveMouse(x, y int) error {
	return fmt.Errorf("darwin input not fully implemented")
}

func (c *darwinController) PressMouse(btn MouseButton, x, y int) error {
	return fmt.Errorf("darwin input not fully implemented")
}

func (c *darwinController) ReleaseMouse(btn MouseButton, x, y int) error {
	return fmt.Errorf("darwin input not fully implemented")
}

func (c *darwinController) Scroll(dx, dy int) error {
	return fmt.Errorf("darwin input not fully implemented")
}

func (c *darwinController) PressKey(keyCode int) error {
	return fmt.Errorf("darwin input not fully implemented")
}

func (c *darwinController) ReleaseKey(keyCode int) error {
	return fmt.Errorf("darwin input not fully implemented")
}

func (c *darwinController) Close() error {
	return nil
}

func (c *darwinController) GetCursorPos() (x, y int, err error) {
	return 0, 0, fmt.Errorf("not implemented")
}

func (c *darwinController) GetScreenSize() (width, height int, err error) {
	return 0, 0, fmt.Errorf("not implemented")
}