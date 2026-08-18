package tui

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// errNoClipImage means the clipboard holds no image — not a failure, just
// nothing for the image path to do; the caller falls back to text.
var errNoClipImage = errors.New("no image on the clipboard")

// clipboardImage reads an image off the OS clipboard into a temporary PNG and
// returns its path. It exists because a terminal paste can never deliver one:
// bracketed paste carries text only, so an image sitting on the clipboard
// reaches the app by asking the OS directly, not by waiting for the terminal.
func clipboardImage() (string, error) {
	f, err := os.CreateTemp("", "alpaca-clipboard-*.png")
	if err != nil {
		return "", err
	}
	f.Close()
	path := f.Name()

	if err := readClipImageInto(path); err != nil {
		os.Remove(path)
		return "", err
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		os.Remove(path)
		return "", errNoClipImage
	}
	return path, nil
}

func readClipImageInto(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return darwinClipImage(path)
	case "windows":
		return windowsClipImage(path)
	default:
		return linuxClipImage(path)
	}
}

// darwinClipImage asks AppleScript for the clipboard as PNG data — the class
// screenshots and browser-copied images arrive under. Anything else makes the
// coercion error, which reads as "no image here".
func darwinClipImage(path string) error {
	script := fmt.Sprintf(`set fh to open for access POSIX file %q with write permission
set eof fh to 0
write (the clipboard as «class PNGf») to fh
close access fh`, path)
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		return errNoClipImage
	}
	return nil
}

// linuxClipImage reads through wl-paste on Wayland and xclip on X11. Missing
// tools are reported by name: "no image" would send someone hunting a
// clipboard problem when the fix is one package install.
func linuxClipImage(path string) error {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if _, err := exec.LookPath("wl-paste"); err != nil {
			return errors.New("image paste needs wl-clipboard (provides wl-paste)")
		}
		return clipBytesToFile(path, "wl-paste", "--type", "image/png")
	}
	if _, err := exec.LookPath("xclip"); err != nil {
		return errors.New("image paste needs xclip")
	}
	return clipBytesToFile(path, "xclip", "-selection", "clipboard", "-t", "image/png", "-o")
}

// clipBytesToFile runs a clipboard reader and keeps its stdout when it is
// actually a PNG — both tools print an error message to stdout when the
// clipboard holds no image of the requested type.
func clipBytesToFile(path, name string, args ...string) error {
	out, err := exec.Command(name, args...).Output()
	if err != nil || !bytes.HasPrefix(out, []byte("\x89PNG")) {
		return errNoClipImage
	}
	return os.WriteFile(path, out, 0o600)
}

// windowsClipImage goes through PowerShell's clipboard API.
func windowsClipImage(path string) error {
	ps := fmt.Sprintf(
		`Add-Type -AssemblyName System.Windows.Forms,System.Drawing; `+
			`$i=[System.Windows.Forms.Clipboard]::GetImage(); `+
			`if($i -eq $null){exit 1}; `+
			`$i.Save(%q,[System.Drawing.Imaging.ImageFormat]::Png)`, path)
	if err := exec.Command("powershell", "-NoProfile", "-Command", ps).Run(); err != nil {
		return errNoClipImage
	}
	return nil
}

// openInViewer hands a file to the platform's opener — the full-resolution
// counterpart to the half-block preview in the popup.
func openInViewer(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}
