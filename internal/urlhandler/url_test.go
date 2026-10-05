package urlhandler

import (
	"strings"
	"testing"
)

const goodID = "ws-0123456789abcdef"

func TestParseOpenURLAccepts(t *testing.T) {
	id, err := ParseOpenURL("wgo://open?ws=" + goodID)
	if err != nil || id != goodID {
		t.Fatalf("ParseOpenURL = %q, %v; want %q", id, err, goodID)
	}
	if got := OpenURL(goodID); got != "wgo://open?ws="+goodID {
		t.Fatalf("OpenURL = %q", got)
	}
}

func TestParseOpenURLRejects(t *testing.T) {
	cases := map[string]string{
		"empty":                   "",
		"percent dot":             "wgo://open?ws=%2e%2e",
		"percent-encoded w":       "wgo://open?ws=%77s-0123456789abcdef",
		"percent in host":         "wgo://%6fpen?ws=" + goodID,
		"uppercase id":            "wgo://open?ws=ws-0123456789ABCDEF",
		"uppercase prefix":        "wgo://open?ws=WS-0123456789abcdef",
		"empty ws":                "wgo://open?ws=",
		"ws without value":        "wgo://open?ws",
		"no query":                "wgo://open",
		"bare question mark":      "wgo://open?",
		"duplicate ws":            "wgo://open?ws=a&ws=b",
		"duplicate valid ws":      "wgo://open?ws=" + goodID + "&ws=" + goodID,
		"dotdot and extra":        "wgo://open?ws=..&x=1",
		"extra key":               "wgo://open?ws=" + goodID + "&x=1",
		"fragment":                "wgo://open?ws=" + goodID + "#frag",
		"empty fragment":          "wgo://open?ws=" + goodID + "#",
		"path":                    "wgo://open/path?ws=" + goodID,
		"path empty ws":           "wgo://open/path?ws=",
		"slash path":              "wgo://open/?ws=" + goodID,
		"userinfo":                "wgo://user@open?ws=" + goodID,
		"userinfo empty":          "wgo://user@open?ws=",
		"port":                    "wgo://open:1?ws=" + goodID,
		"empty port":              "wgo://open:?ws=" + goodID,
		"uppercase scheme":        "WGO://open?ws=" + goodID,
		"uppercase scheme bare":   "WGO://",
		"opaque":                  "wgo:open?ws=" + goodID,
		"single slash":            "wgo:/open?ws=" + goodID,
		"other scheme":            "https://open?ws=" + goodID,
		"file scheme":             "file:///etc/passwd",
		"other host":              "wgo://run?ws=" + goodID,
		"uppercase host":          "wgo://OPEN?ws=" + goodID,
		"empty host":              "wgo://?ws=" + goodID,
		"trailing ampersand":      "wgo://open?ws=" + goodID + "&",
		"leading ampersand":       "wgo://open?&ws=" + goodID,
		"semicolon":               "wgo://open?ws=" + goodID + ";",
		"semicolon separator":     "wgo://open?ws=" + goodID + ";x=1",
		"plus":                    "wgo://open?ws=+" + goodID,
		"command":                 "wgo://open?ws=" + goodID + "&command=x",
		"command only":            "wgo://open?command=rm%20-rf",
		"resume":                  "wgo://open?ws=" + goodID + "&resume=1",
		"resume first":            "wgo://open?resume=1&ws=" + goodID,
		"uppercase key":           "wgo://open?WS=" + goodID,
		"raw path value":          "wgo://open?ws=/Users/me/work",
		"short id":                "wgo://open?ws=ws-0123",
		"long id":                 "wgo://open?ws=" + goodID + "0",
		"non hex id":              "wgo://open?ws=ws-0123456789abcdeg",
		"space":                   "wgo://open?ws= " + goodID,
		"trailing space":          "wgo://open?ws=" + goodID + " ",
		"leading space":           " wgo://open?ws=" + goodID,
		"newline":                 "wgo://open?ws=" + goodID + "\n",
		"embedded newline":        "wgo://open?ws=" + goodID + "\nrm -rf ~",
		"nul":                     "wgo://open?ws=" + goodID + "\x00",
		"tab":                     "wgo://open?ws=\t" + goodID,
		"del":                     "wgo://open?ws=" + goodID + "\x7f",
		"non ascii":               "wgo://open?ws=ws-0123456789abcdeƒ",
		"very long":               "wgo://open?ws=" + goodID + "&" + strings.Repeat("a", 100000),
		"very long valid shaped":  "wgo://open?ws=" + strings.Repeat("0", MaxURLLen),
		"backslash":               "wgo://open?ws=" + goodID + "\\",
		"quote":                   "wgo://open?ws=" + goodID + "'",
		"double slash after host": "wgo://open//?ws=" + goodID,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if id, err := ParseOpenURL(raw); err == nil {
				t.Fatalf("ParseOpenURL(%q) = %q, want an error", raw, id)
			}
		})
	}
}

func FuzzParseOpenURL(f *testing.F) {
	f.Add("wgo://open?ws=" + goodID)
	f.Add("wgo://open?ws=%2e")
	f.Fuzz(func(t *testing.T, raw string) {
		id, err := ParseOpenURL(raw)
		if err != nil {
			return
		}
		if raw != "wgo://open?ws="+id || len(id) != len(goodID) {
			t.Fatalf("accepted noncanonical %q as %q", raw, id)
		}
	})
}
