package slate

import "strconv"

// Theme sets the editor's colours and fonts. Colours are CSS colour values
// such as "#1a1b26". An empty field keeps the default.
type Theme struct {
	Background     string
	Foreground     string
	Dim            string // syntax characters, line numbers, secondary text
	Accent         string // links, mentions, checked boxes
	CodeBackground string
	Border         string
	Selection      string

	// Font and MonoFont are CSS font-family lists. FontSize is in pixels.
	Font     string
	MonoFont string
	FontSize int

	// Vars sets further CSS custom properties by name without the "--sl-"
	// prefix, for example "syn-keyword" or "warning". See web/src/style.css.
	Vars map[string]string
}

// vars returns the theme as CSS custom properties.
func (t Theme) vars() map[string]string {
	out := map[string]string{}
	set := func(name, value string) {
		if value != "" {
			out[name] = value
		}
	}
	set("bg", t.Background)
	set("fg", t.Foreground)
	set("dim", t.Dim)
	set("accent", t.Accent)
	set("code-bg", t.CodeBackground)
	set("border", t.Border)
	set("selection", t.Selection)
	set("font", t.Font)
	set("mono", t.MonoFont)
	if t.FontSize > 0 {
		out["size"] = strconv.Itoa(t.FontSize) + "px"
	}
	for name, value := range t.Vars {
		set(name, value)
	}
	return out
}
