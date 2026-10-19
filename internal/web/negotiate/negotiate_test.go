package negotiate

import "testing"

func TestNegotiateMatrix(t *testing.T) {
	cases := []struct {
		name   string
		accept string
		want   Media
	}{
		{"absent yields JSON", "", MediaJSON},
		{"bare star yields JSON", "*/*", MediaJSON},
		{"browser yields HTML", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", MediaHTML},
		{"json explicit", "application/json", MediaJSON},
		{"markdown explicit", "text/markdown", MediaMarkdown},
		{"tie prefers JSON over HTML", "text/html, application/json", MediaJSON},
		{"tie prefers markdown over HTML", "text/html, text/markdown", MediaMarkdown},
		{"q decides", "text/html;q=0.9, application/json;q=0.4", MediaHTML},
		{"q decides markdown", "text/html;q=0.2, text/markdown;q=0.7", MediaMarkdown},
		{"unknown falls back JSON", "application/xml", MediaJSON},
		{"zero q falls back JSON", "text/html;q=0", MediaJSON},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Negotiate(tc.accept); got != tc.want {
				t.Fatalf("Negotiate(%q) = %v, want %v", tc.accept, got, tc.want)
			}
		})
	}
}
