package main

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/dop251/goja"
)

// The upstream API sits behind an ArvanCloud bot check. A client it does not
// recognize gets an HTML interstitial instead of the API response — still
// HTTP 200 — carrying two cookie values encoded as JSFuck expressions. A
// browser evaluates them, sets the cookies, and reloads. There is no way to
// reach the API without doing the same, so solveChallenge does it in-process
// with a JavaScript interpreter rather than shipping a headless browser.
//
// The page also defines a decoder E that XORs every character with 6, then
// applies it twice — XOR twice is the identity — so each cookie value is
// exactly what its expression evaluates to. No post-processing needed.

const (
	// challengeTimeout bounds evaluation of one expression. The real ones
	// finish in microseconds; this only stops a hostile page from hanging a
	// request goroutine.
	challengeTimeout = 2 * time.Second

	// maxChallengeCookies and maxCookieValueLen bound what a challenge page
	// can talk this client into storing.
	maxChallengeCookies = 4
	maxCookieValueLen   = 256
)

// errNoChallenge means the body did not look like an interstitial after all.
var errNoChallenge = errors.New("no challenge found in response body")

var (
	// challengeEvalRe captures the JSFuck source inside eval("...").
	challengeEvalRe = regexp.MustCompile(`eval\("((?:[^"\\]|\\.)*)"\)`)

	// challengeCookieRe captures the name of each cookie the page assigns.
	challengeCookieRe = regexp.MustCompile(`document\.cookie\s*=\s*'([A-Za-z0-9_]+)='`)

	// cookieValueRe bounds an evaluated value to characters that are safe to
	// send back in a Cookie header.
	cookieValueRe = regexp.MustCompile(`^[A-Za-z0-9._~+/=:-]+$`)
)

// isChallenge reports whether a response is the interstitial rather than the
// API's own answer. The bot check replies 200 with HTML, so the status line
// alone says nothing.
func isChallenge(contentType string, body []byte) bool {
	return !isJSON(contentType) && challengeCookieRe.Match(body)
}

// solveChallenge evaluates the cookie expressions in an interstitial page and
// returns the cookies a browser would have set.
func solveChallenge(page []byte) ([]*http.Cookie, error) {
	names := challengeCookieRe.FindAllSubmatch(page, -1)
	exprs := challengeEvalRe.FindAllSubmatch(page, -1)

	if len(names) == 0 || len(exprs) == 0 {
		return nil, errNoChallenge
	}
	// The page assigns one cookie per expression, in order. A mismatch means
	// the generator changed shape and the pairing can no longer be trusted.
	if len(names) != len(exprs) {
		return nil, fmt.Errorf("challenge has %d cookies but %d expressions", len(names), len(exprs))
	}
	if len(names) > maxChallengeCookies {
		return nil, fmt.Errorf("challenge sets %d cookies, over the limit of %d", len(names), maxChallengeCookies)
	}

	cookies := make([]*http.Cookie, 0, len(names))
	for i, expr := range exprs {
		// The expression is a JavaScript string literal in the page source.
		src, err := strconv.Unquote(`"` + string(expr[1]) + `"`)
		if err != nil {
			return nil, fmt.Errorf("unquoting expression %d: %w", i+1, err)
		}

		value, err := evalJS(src)
		if err != nil {
			return nil, fmt.Errorf("evaluating expression %d: %w", i+1, err)
		}
		if len(value) > maxCookieValueLen || !cookieValueRe.MatchString(value) {
			return nil, fmt.Errorf("expression %d produced an unusable cookie value", i+1)
		}

		cookies = append(cookies, &http.Cookie{
			Name:  string(names[i][1]),
			Value: value,
			Path:  "/",
		})
	}
	return cookies, nil
}

// evalJS runs one expression in a bare interpreter. goja exposes only the
// JavaScript standard library — no host bindings, no network, no filesystem —
// so the page gets nothing but arithmetic and string builtins.
func evalJS(src string) (string, error) {
	vm := goja.New()

	timer := time.AfterFunc(challengeTimeout, func() {
		vm.Interrupt("challenge evaluation timed out")
	})
	defer timer.Stop()

	v, err := vm.RunString(src)
	if err != nil {
		return "", err
	}
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return "", errors.New("expression produced no value")
	}
	return v.String(), nil
}
