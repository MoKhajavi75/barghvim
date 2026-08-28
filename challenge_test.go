package main

import (
	"strings"
	"testing"
)

// TestSolveChallengeAgainstRealPage runs the solver over the interstitial the
// bot check actually served, and pins the values a browser derives from it.
// If ArvanCloud changes the generator this fails here rather than silently
// emptying every subscriber's calendar.
func TestSolveChallengeAgainstRealPage(t *testing.T) {
	cookies, err := solveChallenge(challengePage(t))
	if err != nil {
		t.Fatalf("solveChallenge() = %v", err)
	}

	want := map[string]string{
		"__arcsjs":  "7e30964c3c31bc2ee402413838fd87c7",
		"__arcsjsc": "arcookie-1787959246-de2771119bd1ba91ae749779cb7edc4f",
	}
	if len(cookies) != len(want) {
		t.Fatalf("got %d cookies, want %d", len(cookies), len(want))
	}
	for _, c := range cookies {
		if c.Value != want[c.Name] {
			t.Errorf("%s = %q, want %q", c.Name, c.Value, want[c.Name])
		}
		if c.Path != "/" {
			t.Errorf("%s path = %q, want /", c.Name, c.Path)
		}
	}
}

func TestIsChallenge(t *testing.T) {
	page := challengePage(t)

	tests := []struct {
		name        string
		contentType string
		body        []byte
		want        bool
	}{
		{"the interstitial", "text/html", page, true},
		// The bot check answers 200 with HTML, so the status line says nothing
		// and Content-Type is the only signal that this is not the API.
		{"the api", "application/json; charset=utf-8", []byte(`[{"billId":"1"}]`), false},
		{"unrelated html", "text/html", []byte("<html>maintenance</html>"), false},
		{"quota message", "text/plain", []byte("API calls quota exceeded!"), false},
		{"empty", "", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isChallenge(tt.contentType, tt.body); got != tt.want {
				t.Errorf("isChallenge(%q) = %v, want %v", tt.contentType, got, tt.want)
			}
		})
	}
}

func TestSolveChallengeRejects(t *testing.T) {
	tests := []struct {
		name string
		page string
	}{
		{"no challenge at all", "<html><body>hello</body></html>"},
		{
			name: "more cookies than expressions",
			page: `document.cookie = 'a=';document.cookie = 'b=';eval("'x'")`,
		},
		{
			name: "value with characters a cookie cannot carry",
			page: `document.cookie = 'a=';eval("'a; Domain=evil.test'")`,
		},
		{
			name: "expression that never finishes",
			page: `document.cookie = 'a=';eval("while(true){}")`,
		},
		{
			name: "expression that yields nothing",
			page: `document.cookie = 'a=';eval("undefined")`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := solveChallenge([]byte(tt.page)); err == nil {
				t.Errorf("solveChallenge() = %+v, want error", got)
			}
		})
	}
}

func TestSolveChallengeRejectsTooManyCookies(t *testing.T) {
	var page strings.Builder
	for i := range maxChallengeCookies + 1 {
		page.WriteString("document.cookie = 'c" + string(rune('0'+i)) + "=';")
		page.WriteString(`eval("'v'");`)
	}

	if got, err := solveChallenge([]byte(page.String())); err == nil {
		t.Errorf("solveChallenge() = %+v, want error", got)
	}
}

// TestEvalJSIsSandboxed pins that the interpreter hands the page nothing but
// the JavaScript standard library — no host bindings of any kind.
func TestEvalJSIsSandboxed(t *testing.T) {
	for _, global := range []string{"require", "process", "fetch", "XMLHttpRequest", "window", "document"} {
		t.Run(global, func(t *testing.T) {
			got, err := evalJS("typeof " + global)
			if err != nil {
				t.Fatalf("evalJS() = %v", err)
			}
			if got != "undefined" {
				t.Errorf("typeof %s = %q, want %q", global, got, "undefined")
			}
		})
	}
}
