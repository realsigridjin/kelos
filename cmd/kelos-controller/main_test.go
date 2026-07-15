package main

import "testing"

func TestValidateImageVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		version string
		wantErr bool
	}{
		{name: "release", version: "v1.2.3"},
		{name: "development", version: "main"},
		{name: "empty", wantErr: true},
		{name: "whitespace", version: "  ", wantErr: true},
		{name: "invalid tag", version: "release/candidate", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateImageVersion(tt.version)
			if tt.wantErr && err == nil {
				t.Fatal("validateImageVersion() error = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateImageVersion() error = %v", err)
			}
		})
	}
}

func TestImageForVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		image   string
		want    string
		wantErr bool
	}{
		{
			name:  "repository",
			image: "ghcr.io/kelos-dev/kelos-session-runtime",
			want:  "ghcr.io/kelos-dev/kelos-session-runtime:v1.2.3",
		},
		{name: "short repository", image: "runtime", want: "runtime:v1.2.3"},
		{name: "tagged override", image: "runtime:custom", want: "runtime:custom"},
		{
			name:  "digest override",
			image: "runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			want:  "runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{name: "invalid image", image: "https://ghcr.io/kelos-dev/runtime", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := imageForVersion(tt.image, "v1.2.3")
			if tt.wantErr {
				if err == nil {
					t.Fatal("imageForVersion() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("imageForVersion() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("imageForVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}
