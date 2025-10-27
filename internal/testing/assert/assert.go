// Package assert implements a set of basic test helpers.
//
// Lightly adapted from this blog post: https://antonz.org/do-not-testify/
package assert

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Equal asserts that got is equal to want.
func Equal[T any](tb testing.TB, got T, want T, customMsg ...any) {
	tb.Helper()
	if areEqual(got, want) {
		return
	}
	msg := formatMsg("expected values to be equal", customMsg)
	tb.Errorf("%s:\ngot:  %#v\nwant: %#v", msg, got, want)
}

// True asserts that got is true.
func True(tb testing.TB, got bool, customMsg ...any) {
	tb.Helper()
	if !got {
		tb.Error(formatMsg("expected value to be true", customMsg))
	}
}

// Error asserts that got matches want, which may be an error, an error type,
// an error string, or nil. If want is a string, it is considered a match if
// it is ia substring of got's string value.
func Error(tb testing.TB, got error, want any) {
	tb.Helper()

	if want != nil && got == nil {
		tb.Errorf("errors do not match:\ngot:  <nil>\nwant: %v", want)
		return
	}

	switch w := want.(type) {
	case nil:
		NilError(tb, got)
	case error:
		if !errors.Is(got, w) {
			tb.Errorf("errors do not match:\ngot:  %T(%v)\nwant: %T(%v)", got, got, w, w)
		}
	case string:
		if !strings.Contains(got.Error(), w) {
			tb.Errorf("error string does not match:\ngot:  %q\nwant: %q", got.Error(), w)
		}
	case reflect.Type:
		target := reflect.New(w).Interface()
		if !errors.As(got, target) {
			tb.Errorf("error type does not match:\ngot:  %T\nwant: %s", got, w)
		}
	default:
		tb.Errorf("unsupported want type: %T", want)
	}
}

// NilError asserts that got is nil.
func NilError(tb testing.TB, got error) {
	tb.Helper()
	if got != nil {
		tb.Fatalf("expected nil error, got %q (%T)", got, got)
	}
}

type equaler[T any] interface {
	Equal(T) bool
}

func areEqual[T any](a, b T) bool {
	if isNil(a) && isNil(b) {
		return true
	}
	// special case types with an Equal method
	if eq, ok := any(a).(equaler[T]); ok {
		return eq.Equal(b)
	}
	// special case byte slices
	if aBytes, ok := any(a).([]byte); ok {
		bBytes := any(b).([]byte)
		return bytes.Equal(aBytes, bBytes)
	}
	return reflect.DeepEqual(a, b)
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	// A non-nil interface can still hold a nil value, so we check the
	// underlying value.
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan,
		reflect.Func,
		reflect.Interface,
		reflect.Map,
		reflect.Pointer,
		reflect.Slice,
		reflect.UnsafePointer:
		return rv.IsNil()
	default:
		return false
	}
}

func formatMsg(defaultMsg string, customMsg []any) string {
	msg := defaultMsg
	if len(customMsg) > 0 {
		tmpl, ok := customMsg[0].(string)
		if !ok {
			tmpl = fmt.Sprintf("%v", customMsg[0])
		}
		msg = fmt.Sprintf(tmpl, customMsg[1:]...)
	}
	return msg
}
