package links

import "fmt"

// Hyperlink wraps text in an OSC8 terminal hyperlink escape sequence with underline.
func Hyperlink(url, text string) string {
	return fmt.Sprintf("\033]8;;%s\033\\\033[4m%s\033[24m\033]8;;\033\\", url, text)
}

// Link returns an OSC8 hyperlink if isTTY is true, otherwise returns plain text.
func Link(url, text string, isTTY bool) string {
	if !isTTY || url == "" {
		return text
	}
	return Hyperlink(url, text)
}
