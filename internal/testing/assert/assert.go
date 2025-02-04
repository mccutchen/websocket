// Package assert implements common assertions used in go-httbin's unit tests.
package assert

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mccutchen/websocket/internal/testing/must"
)

// Equal asserts that two values are equal.
func Equal[T comparable](t testing.TB, got, want T, msg string, arg ...any) {
	t.Helper()
	if got != want {
		if msg == "" {
			msg = "expected values to match"
		}
		msg = fmt.Sprintf(msg, arg...)
		t.Fatalf("%s:\nwant: %v\n got: %v", msg, want, got)
	}
}

// DeepEqual asserts that two values are deeply equal.
func DeepEqual[T any](t testing.TB, got, want T, msg string, arg ...any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		if msg == "" {
			msg = "expected values to match"
		}
		msg = fmt.Sprintf(msg, arg...)
		t.Fatalf("%s:\nwant: %#v\n got: %#v", msg, want, got)
	}
}

// NilError asserts that an error is nil.
func NilError(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("expected nil error, got %q (%T)", err, err)
	}
}

// Error asserts that an error is not nil.
func Error(t testing.TB, got, expected error) {
	t.Helper()
	if !errorsMatch(t, got, expected) {
		t.Fatalf("expected error %q, got %q (%T vs %T)", expected, got, expected, got)
	}
}

func errorsMatch(t testing.TB, got, expected error) bool {
	t.Helper()
	switch {
	case got == expected:
		return true
	case errors.Is(got, expected):
		return true
	case got != nil && expected != nil:
		return got.Error() == expected.Error()
	default:
		return false
	}
}

// StatusCode asserts that a response has a specific status code.
func StatusCode(t testing.TB, resp *http.Response, code int) {
	t.Helper()
	if resp.StatusCode != code {
		t.Fatalf("expected status code %d, got %d", code, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		// Ensure our error responses are never served as HTML, so that we do
		// not need to worry about XSS or other attacks in error responses.
		if ct := resp.Header.Get("Content-Type"); !isSafeContentType(ct) {
			t.Errorf("HTTP %s error served with dangerous content type: %s", resp.Status, ct)
		}
	}
}

func isSafeContentType(ct string) bool {
	return strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/plain") || strings.HasPrefix(ct, "application/octet-stream")
}

// Header asserts that a header key has a specific value in a response.
func Header(t testing.TB, resp *http.Response, key, want string) {
	t.Helper()
	got := resp.Header.Get(key)
	if want != got {
		t.Fatalf("expected header %s=%#v, got %#v", key, want, got)
	}
}

// ContentType asserts that a response has a specific Content-Type header
// value.
func ContentType(t testing.TB, resp *http.Response, contentType string) {
	t.Helper()
	Header(t, resp, "Content-Type", contentType)
}

// Contains asserts that needle is found in the given string.
func Contains(t testing.TB, s string, needle string, description string) {
	t.Helper()
	if !strings.Contains(s, needle) {
		t.Fatalf("expected string %q in %s %q", needle, description, s)
	}
}

// BodyContains asserts that a response body contains a specific substring.
func BodyContains(t testing.TB, resp *http.Response, needle string) {
	t.Helper()
	body := must.ReadAll(t, resp.Body)
	Contains(t, body, needle, "body")
}

// BodyEquals asserts that a response body is equal to a specific string.
func BodyEquals(t testing.TB, resp *http.Response, want string) {
	t.Helper()
	got := must.ReadAll(t, resp.Body)
	Equal(t, got, want, "incorrect response body")
}

// BodySize asserts that a response body is a specific size.
func BodySize(t testing.TB, resp *http.Response, want int) {
	t.Helper()
	got := must.ReadAll(t, resp.Body)
	Equal(t, len(got), want, "incorrect response body size")
}

// DurationRange asserts that a duration is within a specific range.
func DurationRange(t testing.TB, got, minVal, maxVal time.Duration) {
	t.Helper()
	if got < minVal || got > maxVal {
		t.Fatalf("expected duration between %s and %s, got %s", minVal, maxVal, got)
	}
}

type number interface {
	~int64 | ~float64
}

// RoughlyEqual asserts that a numeric value is within a certain tolerance.
func RoughlyEqual[T number](t testing.TB, got, want T, epsilon T) {
	t.Helper()
	if got < want-epsilon || got > want+epsilon {
		t.Fatalf("expected value between %v and %v, got %v", want-epsilon, want+epsilon, got)
	}
}
