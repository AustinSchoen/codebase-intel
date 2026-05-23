package postgres

import (
	"testing"
)

func TestNilIfEmpty(t *testing.T) {
	tests := []struct {
		input  string
		isNil  bool
		expect interface{}
	}{
		{"", true, nil},
		{"hello", false, "hello"},
		{" ", false, " "},
	}

	for _, tt := range tests {
		result := nilIfEmpty(tt.input)
		if tt.isNil {
			if result != nil {
				t.Errorf("nilIfEmpty(%q) = %v, want nil", tt.input, result)
			}
		} else {
			if result != tt.expect {
				t.Errorf("nilIfEmpty(%q) = %v, want %v", tt.input, result, tt.expect)
			}
		}
	}
}

func TestNilIfZero(t *testing.T) {
	tests := []struct {
		input  int
		isNil  bool
		expect interface{}
	}{
		{0, true, nil},
		{1, false, 1},
		{-1, false, -1},
		{100, false, 100},
	}

	for _, tt := range tests {
		result := nilIfZero(tt.input)
		if tt.isNil {
			if result != nil {
				t.Errorf("nilIfZero(%d) = %v, want nil", tt.input, result)
			}
		} else {
			if result != tt.expect {
				t.Errorf("nilIfZero(%d) = %v, want %v", tt.input, result, tt.expect)
			}
		}
	}
}

// Note: tests that merely instantiated a struct and asserted its fields were
// removed in the maintenance pass. They verified that Go's struct assignment
// works, not anything about this package. Real behavior is exercised by the
// integration tests in integration_test.go (build tag: integration).
