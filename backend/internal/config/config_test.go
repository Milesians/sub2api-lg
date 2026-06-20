package config

import "testing"

func TestValidatePublicPath(t *testing.T) {
	ok := []string{"/lg", "/tools/lg", "/__lg"}
	for _, value := range ok {
		if _, err := ValidatePublicPath(value); err != nil {
			t.Fatalf("%s should be valid: %v", value, err)
		}
	}
	bad := []string{"", "lg", "https://example.com/lg", "/../lg", "/lg?x=1", "/lg#x"}
	for _, value := range bad {
		if _, err := ValidatePublicPath(value); err == nil {
			t.Fatalf("%s should be invalid", value)
		}
	}
}
