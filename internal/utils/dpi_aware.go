package utils

import "local/internal/svc/internal/utils/winproc"

func init() {
    winproc.SetProcessDpiAware.Call()
}
