//go:build android

package platform

type RDPKeyboardCapture struct{}

func NewRDPKeyboardCapture(emit func(RDPKeyEvent), _ ...func(func())) (*RDPKeyboardCapture, error) {
	return &RDPKeyboardCapture{}, nil
}
func (*RDPKeyboardCapture) SetCapture(id string, hwnd uintptr) bool         { return false }
func (*RDPKeyboardCapture) SetShortcutCapture(id string, hwnd uintptr) bool { return false }
func (*RDPKeyboardCapture) Release(id string)                               {}
func (*RDPKeyboardCapture) Close()                                          {}
