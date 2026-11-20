package slate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// WindowsTerminalTheme returns the colours and font that Windows Terminal's
// settings file gives the profile this process runs in. A host uses it when
// the terminal cannot be asked, as in a Herdr pane, which answers no colour
// query. ok is false when the settings name no colours that can be read.
//
// The profile is the one in WT_PROFILE_ID, or the default profile. Colours
// come from the profile's colour scheme, and only from schemes the settings
// file defines: the built-in schemes are not in it.
func WindowsTerminalTheme() (t Theme, ok bool) {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return Theme{}, false
	}
	for _, pkg := range []string{"Microsoft.WindowsTerminal_8wekyb3d8bbwe", "Microsoft.WindowsTerminalPreview_8wekyb3d8bbwe"} {
		data, err := os.ReadFile(filepath.Join(local, "Packages", pkg, "LocalState", "settings.json"))
		if err != nil {
			continue
		}
		if t, ok := windowsTerminalTheme(data, os.Getenv("WT_PROFILE_ID")); ok {
			return t, true
		}
	}
	return Theme{}, false
}

// wtProfile holds the settings a profile, or the defaults for all profiles,
// can carry.
type wtProfile struct {
	GUID        string          `json:"guid"`
	ColorScheme json.RawMessage `json:"colorScheme"` // a name, or {"dark": name, "light": name}
	Background  string          `json:"background"`
	Foreground  string          `json:"foreground"`
	Font        struct {
		Face string `json:"face"`
	} `json:"font"`
}

func windowsTerminalTheme(data []byte, profileID string) (Theme, bool) {
	var settings struct {
		DefaultProfile string `json:"defaultProfile"`
		Profiles       struct {
			Defaults wtProfile   `json:"defaults"`
			List     []wtProfile `json:"list"`
		} `json:"profiles"`
		Schemes []struct {
			Name                string `json:"name"`
			Background          string `json:"background"`
			Foreground          string `json:"foreground"`
			SelectionBackground string `json:"selectionBackground"`
		} `json:"schemes"`
	}
	// A program that rewrites the file, such as PowerShell, may start it
	// with a byte-order mark.
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if json.Unmarshal(plainJSON(data), &settings) != nil {
		return Theme{}, false
	}
	if profileID == "" {
		profileID = settings.DefaultProfile
	}
	// A profile's own setting wins over the defaults.
	layers := []wtProfile{settings.Profiles.Defaults}
	for _, p := range settings.Profiles.List {
		if strings.EqualFold(p.GUID, profileID) {
			layers = append(layers, p)
		}
	}
	var t Theme
	for _, p := range layers {
		if name := schemeName(p.ColorScheme); name != "" {
			for _, s := range settings.Schemes {
				if s.Name == name {
					t.Background, t.Foreground, t.Selection = s.Background, s.Foreground, s.SelectionBackground
				}
			}
		}
		if p.Background != "" {
			t.Background = p.Background
		}
		if p.Foreground != "" {
			t.Foreground = p.Foreground
		}
		if p.Font.Face != "" {
			// Code is set in the terminal's own font.
			t.MonoFont = `"` + p.Font.Face + `", "Cascadia Code", Consolas, monospace`
		}
	}
	return t, t.Background != "" || t.Foreground != "" || t.MonoFont != ""
}

func schemeName(raw json.RawMessage) string {
	var name string
	if json.Unmarshal(raw, &name) == nil {
		return name
	}
	var pair struct{ Dark, Light string }
	json.Unmarshal(raw, &pair)
	if pair.Dark != "" {
		return pair.Dark
	}
	return pair.Light
}

// plainJSON removes the comments and trailing commas that Windows Terminal
// allows in its settings file.
func plainJSON(data []byte) []byte {
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case c == '"':
			start := i
			for i++; i < len(data) && data[i] != '"'; i++ {
				if data[i] == '\\' {
					i++
				}
			}
			out = append(out, data[start:min(i+1, len(data))]...)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			for i += 2; i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/'); i++ {
			}
			i++
		case c == '}' || c == ']':
			// Drop a comma that only white space separates from this bracket.
			for j := len(out) - 1; j >= 0; j-- {
				if out[j] == ',' {
					out = append(out[:j], out[j+1:]...)
					break
				}
				if out[j] != ' ' && out[j] != '\t' && out[j] != '\n' && out[j] != '\r' {
					break
				}
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}
