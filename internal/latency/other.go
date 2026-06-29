//go:build !windows

package latency

// newOtherController returns nil on non-Windows platforms; the hub treats a nil
// controller as "feature unavailable".
func newOtherController() (Controller, error) {
	return nil, errUnsupported
}

func NewController() (Controller, error) {
	return newOtherController()
}
