package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// styles is the whole visual vocabulary. Color is semantic only (spec §4.2):
// the accent marks NEEDS YOU, red marks errors, and everything else is the
// terminal's own foreground, bold, or faint. There are no background fills,
// no cards, and nothing animates. Colors are 256-palette indices chosen per
// background (light/dark); with NO_COLOR only bold / faint / reverse remain,
// so every distinction still reads (UNKNOWN is faint, NEEDS YOU is bold).
type styles struct {
	scope, counts, countsNeed lipgloss.Style // header
	rule                      lipgloss.Style
	section, sectionNeed      lipgloss.Style // section titles
	marker                    lipgloss.Style // the ">" of the selected row
	name, nameSelected        lipgloss.Style
	labelNeed                 lipgloss.Style // NEEDS YOU state text
	unknown                   lipgloss.Style // UNKNOWN rows: faint, never ready-looking
	muted                     lipgloss.Style // details, ages, field labels, hints
	text                      lipgloss.Style
	errText                   lipgloss.Style // banners and error flashes
	hintKey                   lipgloss.Style
	cursor                    lipgloss.Style // text input cursor (reverse video, no blink)
}

// palette indices (xterm 256): accent, error, muted for dark and light backgrounds.
const (
	accentDark, accentLight = "215", "166"
	errorDark, errorLight   = "203", "160"
	mutedDark, mutedLight   = "245", "242"
)

func newStyles(dark, noColor bool) styles {
	pick := func(darkIdx, lightIdx string) color.Color {
		if noColor {
			return nil
		}
		return lipgloss.LightDark(dark)(lipgloss.Color(lightIdx), lipgloss.Color(darkIdx))
	}
	fg := func(s lipgloss.Style, c color.Color) lipgloss.Style {
		if c == nil {
			return s
		}
		return s.Foreground(c)
	}
	plain := lipgloss.NewStyle()
	accent, errC, muted := pick(accentDark, accentLight), pick(errorDark, errorLight), pick(mutedDark, mutedLight)
	return styles{
		scope:        plain.Bold(true),
		counts:       fg(plain, muted),
		countsNeed:   fg(plain.Bold(true), accent),
		rule:         fg(plain.Faint(noColor), muted),
		section:      plain.Bold(true),
		sectionNeed:  fg(plain.Bold(true), accent),
		marker:       fg(plain.Bold(true), accent),
		name:         plain,
		nameSelected: plain.Bold(true),
		labelNeed:    fg(plain.Bold(noColor), accent),
		unknown:      fg(plain.Faint(true), muted),
		muted:        fg(plain.Faint(noColor), muted),
		text:         plain,
		errText:      fg(plain.Bold(true), errC),
		hintKey:      plain.Bold(true),
		cursor:       plain.Reverse(true),
	}
}
