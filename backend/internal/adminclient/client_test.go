package adminclient

import "testing"

func TestParseUser(t *testing.T) {
	cases := []string{
		`{"id":"123","username":"demo"}`,
		`{"data":{"id":"123","username":"demo"}}`,
		`{"data":{"user":{"id":"123","username":"demo"}}}`,
		`{"user_id":123,"name":"demo"}`,
	}
	for _, body := range cases {
		user, err := parseUser([]byte(body))
		if err != nil {
			t.Fatalf("parseUser(%s): %v", body, err)
		}
		if user.ID != "123" {
			t.Fatalf("parseUser(%s) id = %q, want 123", body, user.ID)
		}
	}
}

func TestParseAdminUser(t *testing.T) {
	cases := []string{
		`{"id":"123","username":"demo","is_admin":true}`,
		`{"id":"123","username":"demo","isAdmin":true}`,
		`{"id":"123","username":"demo","role":"admin"}`,
	}
	for _, body := range cases {
		user, err := parseUser([]byte(body))
		if err != nil {
			t.Fatalf("parseUser(%s): %v", body, err)
		}
		if !user.IsAdmin {
			t.Fatalf("parseUser(%s) is_admin = false, want true", body)
		}
	}
}
