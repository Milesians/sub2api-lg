package urlx

import "testing"

func TestJoin(t *testing.T) {
	cases := map[string]string{
		JoinMust("https://api.example.com/v1", "/lg"):    "https://api.example.com/v1/lg",
		JoinMust("https://api.example.com/v1/", "/lg"):   "https://api.example.com/v1/lg",
		JoinMust("https://api.example.com", "lg"):        "https://api.example.com/lg",
		JoinMust("https://api.example.com", "/tools/lg"): "https://api.example.com/tools/lg",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("got %s want %s", got, want)
		}
	}
}

func JoinMust(base, path string) string {
	got, err := Join(base, path)
	if err != nil {
		panic(err)
	}
	return got
}
